package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// Repository owns persistence for agents and their child tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates an agents repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// returningColumns is the full, ordered column list scanned back after INSERT so
// the response reflects the values Postgres actually persisted (including every
// column that fell back to its schema default).
const returningColumns = `
	id, created_at, updated_at,
	agent_name, language, timezone, phone_number_id, call_direction, status,
	model_provider, model_name, model_temperature,
	prompt_begin_message_mode, prompt_begin_message, prompt_begin_message_delay_ms,
	prompt_system_prompt,
	voice_provider,
	voice_elevenlabs_voice_id, voice_elevenlabs_voice_name, voice_elevenlabs_voice_model,
	voice_openai_voice_id, voice_openai_voice_name, voice_openai_voice_model, voice_openai_realtime_model, voice_openai_instructions,
	voice_openai_speed, voice_openai_volume,
	transcriber_provider, transcriber_language, transcriber_openai_model, transcriber_elevenlabs_model,
	post_call_analysis_provider, post_call_analysis_model
`

// Create inserts a new agent owned by userID together with its dynamic variables
// and post-call analysis fields, all within a single transaction. Only the
// columns present in req are written; everything else falls back to its schema
// default (see migration 00002), so Go zero-values never clobber a DB default.
func (r *Repository) Create(ctx context.Context, userID string, req types.CreateAgentRequest) (types.AgentResource, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return types.AgentResource{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Always-present columns; the rest are appended only when supplied.
	cols := []string{"user_id", "agent_name"}
	args := []any{userID, req.Agent.Name}
	add := func(col string, val any) {
		cols = append(cols, col)
		args = append(args, val)
	}

	if a := req.Agent; a != nil {
		if a.Language != "" {
			add("language", a.Language)
		}
		if a.Timezone != nil {
			add("timezone", a.Timezone)
		}
		if a.PhoneNumberID != nil {
			add("phone_number_id", normalizePhoneNumberID(a.PhoneNumberID))
		}
		if a.CallDirection != "" {
			add("call_direction", a.CallDirection)
		}
		// Go-live invariant: an agent may only start active with a phone number
		// assigned. Without one, force it to start paused — overriding a requested
		// active status and the column's 'active' default — so a new agent is never
		// born Live with no number to call from. With a number, honour the request
		// (or fall back to the schema default when status is omitted).
		if hasPhoneNumberAssigned(normalizePhoneNumberID(a.PhoneNumberID)) {
			if a.Status != "" {
				add("status", a.Status)
			}
		} else {
			add("status", "inactive")
		}
	}

	if m := req.Model; m != nil {
		if m.Provider != "" {
			add("model_provider", m.Provider)
		}
		if m.Name != "" {
			add("model_name", m.Name)
		}
		if m.Temperature != 0 {
			add("model_temperature", m.Temperature)
		}
	}

	if p := req.Prompt; p != nil {
		if p.BeginMessageMode != "" {
			add("prompt_begin_message_mode", p.BeginMessageMode)
		}
		if p.BeginMessage != "" {
			add("prompt_begin_message", p.BeginMessage)
		}
		if p.BeginMessageDelayMs != 0 {
			add("prompt_begin_message_delay_ms", p.BeginMessageDelayMs)
		}
		if p.SystemPrompt != "" {
			add("prompt_system_prompt", p.SystemPrompt)
		}
	}

	if v := req.Voice; v != nil {
		if v.Provider != "" {
			add("voice_provider", v.Provider)
		}
		if el := v.ElevenLabs; el != nil {
			if el.VoiceID != "" {
				add("voice_elevenlabs_voice_id", el.VoiceID)
			}
			if el.VoiceName != "" {
				add("voice_elevenlabs_voice_name", el.VoiceName)
			}
			if el.VoiceModel != "" {
				add("voice_elevenlabs_voice_model", el.VoiceModel)
			}
		}
		if oa := v.OpenAI; oa != nil {
			if oa.VoiceID != "" {
				add("voice_openai_voice_id", oa.VoiceID)
			}
			if oa.VoiceName != "" {
				add("voice_openai_voice_name", oa.VoiceName)
			}
			if oa.VoiceModel != "" {
				add("voice_openai_voice_model", oa.VoiceModel)
			}
			if oa.RealtimeModel != "" {
				add("voice_openai_realtime_model", oa.RealtimeModel)
			}
			if oa.Instructions != nil {
				add("voice_openai_instructions", oa.Instructions)
			}
			if oa.Speed != 0 {
				add("voice_openai_speed", oa.Speed)
			}
			if oa.Volume != 0 {
				add("voice_openai_volume", oa.Volume)
			}
		}
	}

	if t := req.Transcriber; t != nil {
		if t.Provider != "" {
			add("transcriber_provider", t.Provider)
		}
		if t.Language != "" {
			add("transcriber_language", t.Language)
		}
		if t.OpenAI != nil && t.OpenAI.Model != "" {
			add("transcriber_openai_model", t.OpenAI.Model)
		}
		if t.ElevenLabs != nil && t.ElevenLabs.Model != "" {
			add("transcriber_elevenlabs_model", t.ElevenLabs.Model)
		}
	}

	if pc := req.PostCall; pc != nil {
		if pc.AnalysisProvider != "" {
			add("post_call_analysis_provider", pc.AnalysisProvider)
		}
		if pc.AnalysisModel != nil {
			add("post_call_analysis_model", pc.AnalysisModel)
		}
	}

	if err := r.resolvePhoneNumberAssignment(ctx, tx, userID, "", requestPhoneNumberID(req)); err != nil {
		return types.AgentResource{}, err
	}

	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	query := fmt.Sprintf(
		"INSERT INTO agents (%s) VALUES (%s) RETURNING %s",
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		returningColumns,
	)

	var row agentRow
	if err := scanAgentRow(tx.QueryRow(ctx, query, args...), &row); err != nil {
		return types.AgentResource{}, fmt.Errorf("insert agent: %w", err)
	}

	if err := r.insertChildren(ctx, tx, row.ID, req); err != nil {
		return types.AgentResource{}, err
	}

	knowledgeBaseIDs := requestKnowledgeBaseIDs(req)
	if err := r.resolveKnowledgeBaseAttachments(ctx, tx, userID, row.ID, knowledgeBaseIDs); err != nil {
		return types.AgentResource{}, err
	}

	toolIDs := requestToolIDs(req)
	if err := r.resolveToolAttachments(ctx, tx, userID, row.ID, toolIDs); err != nil {
		return types.AgentResource{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return types.AgentResource{}, fmt.Errorf("commit tx: %w", err)
	}

	// The child collections were just persisted from req, so echo them back
	// rather than re-reading them.
	var dynamicVars map[string]string
	var postCallData []types.PostCallField
	if req.Prompt != nil {
		dynamicVars = req.Prompt.DynamicVariables
	}
	if req.PostCall != nil {
		postCallData = req.PostCall.PostCallAnalysisData
	}

	return buildResource(row, dynamicVars, postCallData, knowledgeBaseIDs, toolIDs), nil
}

// requestKnowledgeBaseIDs returns the knowledge base ids a create request
// attaches, normalized. An absent section attaches nothing.
func requestKnowledgeBaseIDs(req types.CreateAgentRequest) []string {
	if req.KnowledgeBase == nil {
		return nil
	}
	return normalizeKnowledgeBaseIDs(req.KnowledgeBase.KnowledgeBaseIDs)
}

// requestToolIDs returns the tool ids a create request attaches, normalized. An
// absent section attaches nothing.
func requestToolIDs(req types.CreateAgentRequest) []string {
	if req.Tools == nil {
		return nil
	}
	return normalizeToolIDs(req.Tools.ToolIDs)
}

// ErrAgentNotFound is returned by read/write operations when no agent matches
// the requested id.
var ErrAgentNotFound = errors.New("agent not found")

// ErrPhoneNumberNotFound is returned when an agent is assigned to a phone
// number that does not exist or is not owned by the authenticated user.
var ErrPhoneNumberNotFound = errors.New("phone number not found")

// ErrPhoneNumberAssignmentConflict is returned when another agent already uses
// the requested phone number.
var ErrPhoneNumberAssignmentConflict = errors.New("phone number already assigned for call direction")

// GetByID loads a single agent owned by userID together with its child
// collections (dynamic variables and post-call analysis fields). It returns
// ErrAgentNotFound when no agent has the given id — including when the agent
// exists but belongs to another user, so ownership never leaks.
func (r *Repository) GetByID(ctx context.Context, userID, agentID string) (types.AgentResource, error) {
	query := fmt.Sprintf("SELECT %s FROM agents WHERE id = $1 AND user_id = $2", returningColumns)

	var row agentRow
	if err := scanAgentRow(r.pool.QueryRow(ctx, query, agentID, userID), &row); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return types.AgentResource{}, ErrAgentNotFound
		}
		return types.AgentResource{}, fmt.Errorf("select agent: %w", err)
	}

	dynamicVars, err := r.getDynamicVariables(ctx, agentID)
	if err != nil {
		return types.AgentResource{}, err
	}

	postCallData, err := r.getPostCallFields(ctx, agentID)
	if err != nil {
		return types.AgentResource{}, err
	}

	knowledgeBaseIDs, err := r.getKnowledgeBaseIDs(ctx, agentID)
	if err != nil {
		return types.AgentResource{}, err
	}

	toolIDs, err := r.getToolIDs(ctx, agentID)
	if err != nil {
		return types.AgentResource{}, err
	}

	return buildResource(row, dynamicVars, postCallData, knowledgeBaseIDs, toolIDs), nil
}

// InboundAgent is the minimal agent configuration needed to answer an inbound
// call: the prompt fields that shape the AI's behaviour — the General Prompt
// (SystemPrompt) — the welcome-message settings that
// control how the call opens, and the transcriber/voice settings that control
// how the call sounds. It is populated only for an agent that is live and
// inbound-capable.
type InboundAgent struct {
	ID               string
	ModelProvider    string
	ModelName        string
	ModelTemperature float64
	SystemPrompt     string
	Language         string

	// BeginMessageMode is one of the constants.BeginMessageModeOptions values:
	// "agent_speaks_first" (speak WelcomeMessage verbatim), "agent_waits_for_user"
	// (say nothing until the caller speaks), or
	// "agent_speaks_first_with_model_generated_message" (speak an AI-generated
	// opening line).
	BeginMessageMode string
	WelcomeMessage   string
	WelcomeDelayMs   int

	// The provider-specific model is passed to the matching live-call backend.
	TranscriberProvider        string
	TranscriberOpenAIModel     string
	TranscriberElevenLabsModel string

	// VoiceProvider selects the live backend and its voice settings.
	VoiceProvider            string
	VoiceElevenLabsVoiceID   string
	VoiceElevenLabsModel     string
	VoiceOpenAIVoiceID       string
	VoiceOpenAIVoiceModel    string
	VoiceOpenAIRealtimeModel string
	VoiceOpenAIInstructions  string

	// VoiceOpenAISpeed is the spoken rate (1 = normal) and VoiceOpenAIVolume the
	// playback gain (1 = unchanged) for the call.
	VoiceOpenAISpeed  float64
	VoiceOpenAIVolume float64
}

// LiveInboundAgentForPhoneNumber returns the active, inbound-capable agent
// assigned to phoneNumberID, or (nil, nil) when none exists — no agent is
// assigned, the assigned agent is paused (status != active), or it only handles
// outbound calls. A phone number can be assigned to at most one agent, so the
// direction filter simply excludes the outbound-only case.
//
// It is unscoped by user on purpose: it runs from the call-handling path, which
// only knows the phone number the call arrived on, and every agent referencing a
// phone number is owned by that number's owner anyway (enforced on assignment).
func (r *Repository) LiveInboundAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (*InboundAgent, error) {
	return r.liveAgentForPhoneNumber(ctx, phoneNumberID, "inbound")
}

// LiveOutboundAgentForPhoneNumber returns the active, outbound-capable agent
// assigned to phoneNumberID, or (nil, nil) when none exists — no agent is
// assigned, the assigned agent is paused (status != active), or it only handles
// inbound calls. It is the outbound-direction twin of
// LiveInboundAgentForPhoneNumber and backs the Create Call endpoint, which
// derives the answering agent from the number the call is placed from.
func (r *Repository) LiveOutboundAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (*InboundAgent, error) {
	return r.liveAgentForPhoneNumber(ctx, phoneNumberID, "outbound")
}

// liveAgentForPhoneNumber loads the live agent assigned to phoneNumberID that
// handles calls in direction ("inbound" or "outbound"). An agent set to "both"
// matches either direction. It is unscoped by user for the reason given on
// LiveInboundAgentForPhoneNumber.
func (r *Repository) liveAgentForPhoneNumber(ctx context.Context, phoneNumberID, direction string) (*InboundAgent, error) {
	const q = `
		SELECT id, model_provider, model_name, model_temperature,
			prompt_system_prompt, language,
			prompt_begin_message_mode, prompt_begin_message, prompt_begin_message_delay_ms,
			transcriber_provider, transcriber_openai_model, transcriber_elevenlabs_model,
			voice_provider, voice_elevenlabs_voice_id, voice_elevenlabs_voice_model,
			voice_openai_voice_id, voice_openai_voice_model, voice_openai_realtime_model, voice_openai_instructions,
			voice_openai_speed, voice_openai_volume
		FROM agents
		WHERE phone_number_id = $1
			AND status = 'active'
			AND call_direction IN ($2, 'both')
		LIMIT 1`

	var (
		agent               InboundAgent
		systemPrompt        *string
		welcomeMessage      *string
		welcomeDelayMs      *int
		voiceOpenAIInstruct *string
	)
	err := r.pool.QueryRow(ctx, q, phoneNumberID, direction).Scan(
		&agent.ID, &agent.ModelProvider, &agent.ModelName, &agent.ModelTemperature,
		&systemPrompt, &agent.Language,
		&agent.BeginMessageMode, &welcomeMessage, &welcomeDelayMs,
		&agent.TranscriberProvider, &agent.TranscriberOpenAIModel, &agent.TranscriberElevenLabsModel,
		&agent.VoiceProvider, &agent.VoiceElevenLabsVoiceID, &agent.VoiceElevenLabsModel,
		&agent.VoiceOpenAIVoiceID, &agent.VoiceOpenAIVoiceModel, &agent.VoiceOpenAIRealtimeModel, &voiceOpenAIInstruct,
		&agent.VoiceOpenAISpeed, &agent.VoiceOpenAIVolume,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select live %s agent: %w", direction, err)
	}
	agent.SystemPrompt = deref(systemPrompt)
	agent.WelcomeMessage = deref(welcomeMessage)
	agent.VoiceOpenAIInstructions = deref(voiceOpenAIInstruct)
	if welcomeDelayMs != nil {
		agent.WelcomeDelayMs = *welcomeDelayMs
	}
	return &agent, nil
}

// HasAgentForPhoneNumber reports whether any agent is assigned to phoneNumberID.
// It is the coarse gate for inbound-call handling: a number with no agent runs no
// call handler at all (see whatsapplogin.Manager.shouldAttachInbound), while the
// finer active/inbound-direction check happens per call.
func (r *Repository) HasAgentForPhoneNumber(ctx context.Context, phoneNumberID string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM agents WHERE phone_number_id = $1)`
	var exists bool
	if err := r.pool.QueryRow(ctx, q, phoneNumberID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check agent assignment: %w", err)
	}
	return exists, nil
}

// AssignPhoneNumberIfUnassigned links phoneNumberID to the agent, but only while
// that agent still carries no number of its own. It backs the QR login started
// from an agent's editor: the number that login produces belongs to the agent
// that asked for it, so the scan alone finishes the setup. An assignment the
// owner made by hand in the meantime wins, which is why this never overwrites a
// number already on the agent.
//
// The bool reports whether an assignment was written. false with a nil error
// means there was nothing to do — no such agent for this user, or it already has
// a number. A number another agent holds is ErrPhoneNumberAssignmentConflict.
func (r *Repository) AssignPhoneNumberIfUnassigned(
	ctx context.Context,
	userID, agentID, phoneNumberID string,
) (bool, error) {
	userID = strings.TrimSpace(userID)
	agentID = strings.TrimSpace(agentID)
	phoneNumberID = strings.TrimSpace(phoneNumberID)
	if userID == "" || agentID == "" || phoneNumberID == "" {
		return false, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// The same ownership and exclusivity rules a PATCH goes through, taken under
	// the same row lock, so a scan landing while the owner assigns the number by
	// hand serializes instead of handing it to two agents.
	if err := r.resolvePhoneNumberAssignment(ctx, tx, userID, agentID, &phoneNumberID); err != nil {
		return false, err
	}

	const q = `
		UPDATE agents
		SET phone_number_id = $1, updated_at = now()
		WHERE id = $2
			AND user_id = $3
			AND (phone_number_id IS NULL OR btrim(phone_number_id) = '')`
	tag, err := tx.Exec(ctx, q, phoneNumberID, agentID, userID)
	if err != nil {
		return false, fmt.Errorf("assign phone number: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit tx: %w", err)
	}
	return true, nil
}

// PhoneNumberIDForAgent returns the phone_number_id currently assigned to the
// agent (nil when none), scoped to userID, or (nil, nil) when no such agent
// exists. It lets the API layer learn which number to reconnect when an agent's
// assignment changes, without loading the agent's full resource.
//
// Callers that have to tell "no such agent" apart from "an agent with no
// number" want LookupPhoneNumberForAgent, which reports the two separately.
func (r *Repository) PhoneNumberIDForAgent(ctx context.Context, userID, agentID string) (*string, error) {
	phoneNumberID, _, err := r.LookupPhoneNumberForAgent(ctx, userID, agentID)
	return phoneNumberID, err
}

// LookupPhoneNumberForAgent returns the agent's assigned phone_number_id, and
// whether the agent exists at all for userID.
//
// The two answers are separate because they mean different things to a caller
// attaching an agent to something: an unknown agent is a 404, while a known one
// with no number is a validation error the owner fixes by assigning a number.
// Reading them off a single nil would collapse both into the same case.
func (r *Repository) LookupPhoneNumberForAgent(ctx context.Context, userID, agentID string) (*string, bool, error) {
	const q = `SELECT phone_number_id FROM agents WHERE id = $1 AND user_id = $2`
	var phoneNumberID *string
	if err := r.pool.QueryRow(ctx, q, agentID, userID).Scan(&phoneNumberID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("select agent phone number: %w", err)
	}
	return phoneNumberID, true, nil
}

// getDynamicVariables reads the string→string map stored in
// agent_dynamic_variables. It returns nil (not an empty map) when the agent has
// no variables, matching how Create echoes an absent map.
func (r *Repository) getDynamicVariables(ctx context.Context, agentID string) (map[string]string, error) {
	const q = `SELECT name, value FROM agent_dynamic_variables WHERE agent_id = $1`
	rows, err := r.pool.Query(ctx, q, agentID)
	if err != nil {
		return nil, fmt.Errorf("select dynamic variables: %w", err)
	}
	defer rows.Close()

	var vars map[string]string
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("scan dynamic variable: %w", err)
		}
		if vars == nil {
			vars = make(map[string]string)
		}
		vars[name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dynamic variables: %w", err)
	}
	return vars, nil
}

// getPostCallFields reads the ordered list of post-call analysis fields from
// agent_post_call_fields. Nullable columns scan through pointers so SQL NULLs
// map to the zero value rather than erroring.
func (r *Repository) getPostCallFields(ctx context.Context, agentID string) ([]types.PostCallField, error) {
	const q = `
		SELECT type, name, description, examples, required, enum_values, conditional_prompt
		FROM agent_post_call_fields
		WHERE agent_id = $1
		ORDER BY id`
	rows, err := r.pool.Query(ctx, q, agentID)
	if err != nil {
		return nil, fmt.Errorf("select post-call fields: %w", err)
	}
	defer rows.Close()

	var fields []types.PostCallField
	for rows.Next() {
		var (
			f           types.PostCallField
			fieldType   string
			description *string
		)
		if err := rows.Scan(
			&fieldType, &f.Name, &description, &f.Examples, &f.Required, &f.EnumValues, &f.ConditionalPrompt,
		); err != nil {
			return nil, fmt.Errorf("scan post-call field: %w", err)
		}
		f.Type = constants.PostCallFieldType(fieldType)
		f.Description = deref(description)
		fields = append(fields, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate post-call fields: %w", err)
	}
	return fields, nil
}

// List returns a page of the agents owned by userID ordered newest-first,
// together with the total number of matching agents (for pagination metadata).
// Child collections are loaded in bulk for the whole page to avoid a per-agent
// query fan-out.
func (r *Repository) List(ctx context.Context, userID string, limit, offset int) ([]types.AgentResource, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM agents WHERE user_id = $1", userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count agents: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	query := fmt.Sprintf(
		"SELECT %s FROM agents WHERE user_id = $1 ORDER BY created_at DESC, id LIMIT $2 OFFSET $3",
		returningColumns,
	)

	rows, err := r.pool.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("select agents: %w", err)
	}
	defer rows.Close()

	var (
		agentRows []agentRow
		ids       []string
	)
	for rows.Next() {
		var row agentRow
		if err := scanAgentRow(rows, &row); err != nil {
			return nil, 0, fmt.Errorf("scan agent: %w", err)
		}
		agentRows = append(agentRows, row)
		ids = append(ids, row.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate agents: %w", err)
	}

	dynamicVars, err := r.getDynamicVariablesForAgents(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	postCallData, err := r.getPostCallFieldsForAgents(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	knowledgeBaseIDs, err := r.getKnowledgeBaseIDsForAgents(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	toolIDs, err := r.getToolIDsForAgents(ctx, ids)
	if err != nil {
		return nil, 0, err
	}

	resources := make([]types.AgentResource, len(agentRows))
	for i, row := range agentRows {
		resources[i] = buildResource(row, dynamicVars[row.ID], postCallData[row.ID], knowledgeBaseIDs[row.ID], toolIDs[row.ID])
	}
	return resources, total, nil
}

// getDynamicVariablesForAgents loads the dynamic-variable maps for every agent
// id in one query, grouped by agent id. Agents with no variables are simply
// absent from the result (buildResource then receives a nil map, matching the
// single-agent read path).
func (r *Repository) getDynamicVariablesForAgents(ctx context.Context, agentIDs []string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	if len(agentIDs) == 0 {
		return out, nil
	}
	const q = `SELECT agent_id, name, value FROM agent_dynamic_variables WHERE agent_id = ANY($1)`
	rows, err := r.pool.Query(ctx, q, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("select dynamic variables: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var agentID, name, value string
		if err := rows.Scan(&agentID, &name, &value); err != nil {
			return nil, fmt.Errorf("scan dynamic variable: %w", err)
		}
		vars := out[agentID]
		if vars == nil {
			vars = make(map[string]string)
			out[agentID] = vars
		}
		vars[name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dynamic variables: %w", err)
	}
	return out, nil
}

// getPostCallFieldsForAgents loads the post-call analysis fields for every agent
// id in one query, grouped by agent id. Ordering by (agent_id, id) keeps each
// agent's fields in the same stable order as the single-agent read path.
func (r *Repository) getPostCallFieldsForAgents(ctx context.Context, agentIDs []string) (map[string][]types.PostCallField, error) {
	out := make(map[string][]types.PostCallField)
	if len(agentIDs) == 0 {
		return out, nil
	}
	const q = `
		SELECT agent_id, type, name, description, examples, required, enum_values, conditional_prompt
		FROM agent_post_call_fields
		WHERE agent_id = ANY($1)
		ORDER BY agent_id, id`
	rows, err := r.pool.Query(ctx, q, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("select post-call fields: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			agentID     string
			f           types.PostCallField
			fieldType   string
			description *string
		)
		if err := rows.Scan(
			&agentID, &fieldType, &f.Name, &description, &f.Examples, &f.Required, &f.EnumValues, &f.ConditionalPrompt,
		); err != nil {
			return nil, fmt.Errorf("scan post-call field: %w", err)
		}
		f.Type = constants.PostCallFieldType(fieldType)
		f.Description = deref(description)
		out[agentID] = append(out[agentID], f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate post-call fields: %w", err)
	}
	return out, nil
}

// Delete removes the agent with the given id owned by userID. Its child rows
// (dynamic variables and post-call analysis fields) are removed by the
// schema's ON DELETE CASCADE. It returns ErrAgentNotFound when no agent has
// the given id — including when the agent belongs to another user.
func (r *Repository) Delete(ctx context.Context, userID, agentID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM agents WHERE id = $1 AND user_id = $2`, agentID, userID)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAgentNotFound
	}
	return nil
}

// insertChildren writes the two normalized child collections: the dynamic
// variables map and the post-call analysis fields list.
func (r *Repository) insertChildren(ctx context.Context, tx pgx.Tx, agentID string, req types.CreateAgentRequest) error {
	if req.Prompt != nil {
		for name, value := range req.Prompt.DynamicVariables {
			if err := insertDynamicVariable(ctx, tx, agentID, name, value); err != nil {
				return err
			}
		}
	}

	if req.PostCall != nil {
		for _, f := range req.PostCall.PostCallAnalysisData {
			if err := insertPostCallField(ctx, tx, agentID, f); err != nil {
				return err
			}
		}
	}

	return nil
}

// insertDynamicVariable writes a single row into agent_dynamic_variables.
func insertDynamicVariable(ctx context.Context, tx pgx.Tx, agentID, name, value string) error {
	const q = `INSERT INTO agent_dynamic_variables (agent_id, name, value) VALUES ($1, $2, $3)`
	if _, err := tx.Exec(ctx, q, agentID, name, value); err != nil {
		return fmt.Errorf("insert dynamic variable %q: %w", name, err)
	}
	return nil
}

// insertPostCallField writes a single row into agent_post_call_fields.
func insertPostCallField(ctx context.Context, tx pgx.Tx, agentID string, f types.PostCallField) error {
	const q = `
		INSERT INTO agent_post_call_fields
			(agent_id, type, name, description, examples, required, enum_values, conditional_prompt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	if _, err := tx.Exec(ctx, q,
		agentID, f.Type, f.Name, f.Description, f.Examples, f.Required, f.EnumValues, f.ConditionalPrompt,
	); err != nil {
		return fmt.Errorf("insert post-call field %q: %w", f.Name, err)
	}
	return nil
}

// agentRow mirrors returningColumns. Columns that are nullable in the schema use
// pointer types so a SQL NULL scans cleanly.
type agentRow struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time

	AgentName     string
	Language      string
	Timezone      *string
	PhoneNumberID *string
	CallDirection string
	Status        string

	ModelProvider    string
	ModelName        string
	ModelTemperature *float64

	PromptBeginMessageMode    string
	PromptBeginMessage        *string
	PromptBeginMessageDelayMs *int
	PromptSystemPrompt        *string

	VoiceProvider             *string
	VoiceElevenlabsVoiceID    string
	VoiceElevenlabsVoiceName  string
	VoiceElevenlabsVoiceModel string
	VoiceOpenAIVoiceID        string
	VoiceOpenAIVoiceName      string
	VoiceOpenAIVoiceModel     string
	VoiceOpenAIRealtimeModel  string
	VoiceOpenAIInstructions   *string
	VoiceOpenAISpeed          float64
	VoiceOpenAIVolume         float64

	TranscriberProvider        string
	TranscriberLanguage        string
	TranscriberOpenAIModel     string
	TranscriberElevenlabsModel string

	PostCallAnalysisProvider string
	PostCallAnalysisModel    *string
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgentRow(row rowScanner, dst *agentRow) error {
	return row.Scan(
		&dst.ID, &dst.CreatedAt, &dst.UpdatedAt,
		&dst.AgentName, &dst.Language, &dst.Timezone, &dst.PhoneNumberID, &dst.CallDirection, &dst.Status,
		&dst.ModelProvider, &dst.ModelName, &dst.ModelTemperature,
		&dst.PromptBeginMessageMode, &dst.PromptBeginMessage, &dst.PromptBeginMessageDelayMs,
		&dst.PromptSystemPrompt,
		&dst.VoiceProvider,
		&dst.VoiceElevenlabsVoiceID, &dst.VoiceElevenlabsVoiceName, &dst.VoiceElevenlabsVoiceModel,
		&dst.VoiceOpenAIVoiceID, &dst.VoiceOpenAIVoiceName, &dst.VoiceOpenAIVoiceModel, &dst.VoiceOpenAIRealtimeModel, &dst.VoiceOpenAIInstructions,
		&dst.VoiceOpenAISpeed, &dst.VoiceOpenAIVolume,
		&dst.TranscriberProvider, &dst.TranscriberLanguage, &dst.TranscriberOpenAIModel, &dst.TranscriberElevenlabsModel,
		&dst.PostCallAnalysisProvider, &dst.PostCallAnalysisModel,
	)
}

// buildResource assembles the documented AgentResource from the persisted
// scalar row plus its child collections (dynamic variables, post-call analysis
// fields, knowledge base attachments and tool attachments).
//
// knowledgeBaseIDs and toolIDs are normalized to empty slices so an agent with
// nothing attached renders [] rather than null — the difference would otherwise
// read as "unknown" to a client, and there is no such state.
func buildResource(
	row agentRow,
	dynamicVars map[string]string,
	postCallData []types.PostCallField,
	knowledgeBaseIDs []string,
	toolIDs []string,
) types.AgentResource {
	if knowledgeBaseIDs == nil {
		knowledgeBaseIDs = []string{}
	}
	if toolIDs == nil {
		toolIDs = []string{}
	}
	return types.AgentResource{
		ID:        row.ID,
		CreatedAt: row.CreatedAt.Format(time.RFC3339),
		UpdatedAt: row.UpdatedAt.Format(time.RFC3339),
		CreateAgentRequest: types.CreateAgentRequest{
			Agent: &types.AgentSection{
				Name:          row.AgentName,
				Language:      row.Language,
				Timezone:      row.Timezone,
				PhoneNumberID: row.PhoneNumberID,
				CallDirection: row.CallDirection,
				Status:        row.Status,
			},
			Model: &types.ModelSection{
				Provider:    row.ModelProvider,
				Name:        row.ModelName,
				Temperature: deref(row.ModelTemperature),
			},
			Prompt: &types.PromptSection{
				BeginMessageMode:    row.PromptBeginMessageMode,
				BeginMessage:        deref(row.PromptBeginMessage),
				BeginMessageDelayMs: deref(row.PromptBeginMessageDelayMs),
				SystemPrompt:        deref(row.PromptSystemPrompt),
				DynamicVariables:    dynamicVars,
			},
			Voice: &types.VoiceSection{
				Provider: deref(row.VoiceProvider),
				ElevenLabs: &types.ElevenLabsVoice{
					VoiceID:    row.VoiceElevenlabsVoiceID,
					VoiceName:  row.VoiceElevenlabsVoiceName,
					VoiceModel: row.VoiceElevenlabsVoiceModel,
				},
				OpenAI: &types.OpenAIVoice{
					VoiceID:       row.VoiceOpenAIVoiceID,
					VoiceName:     row.VoiceOpenAIVoiceName,
					VoiceModel:    row.VoiceOpenAIVoiceModel,
					RealtimeModel: row.VoiceOpenAIRealtimeModel,
					Instructions:  row.VoiceOpenAIInstructions,
					Speed:         row.VoiceOpenAISpeed,
					Volume:        row.VoiceOpenAIVolume,
				},
			},
			Transcriber: &types.TranscriberSection{
				Provider:   row.TranscriberProvider,
				Language:   row.TranscriberLanguage,
				OpenAI:     &types.TranscriberOpenAI{Model: row.TranscriberOpenAIModel},
				ElevenLabs: &types.TranscriberElevenLabs{Model: row.TranscriberElevenlabsModel},
			},
			PostCall: &types.PostCallSection{
				AnalysisProvider:     row.PostCallAnalysisProvider,
				AnalysisModel:        row.PostCallAnalysisModel,
				PostCallAnalysisData: postCallData,
			},
			KnowledgeBase: &types.KnowledgeBaseSection{
				KnowledgeBaseIDs: knowledgeBaseIDs,
			},
			Tools: &types.ToolsSection{
				ToolIDs: toolIDs,
			},
		},
	}
}

func requestPhoneNumberID(req types.CreateAgentRequest) *string {
	if req.Agent == nil || req.Agent.PhoneNumberID == nil {
		return nil
	}
	return normalizePhoneNumberID(req.Agent.PhoneNumberID)
}

func normalizePhoneNumberID(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// resolvePhoneNumberAssignment reconciles the phone-number assignment for the
// agent identified by agentID (empty during Create, since the row does not yet
// exist) against the user's other agents, inside the caller's transaction.
//
// A phone number can be assigned to only one outbound agent. A nil
// phoneNumberID clears the agent's own assignment and needs no reconciliation.
func (r *Repository) resolvePhoneNumberAssignment(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	agentID string,
	phoneNumberID *string,
) error {
	if phoneNumberID == nil {
		return nil
	}

	// Lock the phone number row so concurrent assignments to the same number
	// serialize, and confirm it belongs to this user (which also guarantees
	// every other agent referencing it is owned by the same user).
	var lockedID string
	if err := tx.QueryRow(
		ctx,
		`SELECT id FROM phone_numbers WHERE id = $1 AND user_id = $2 FOR UPDATE`,
		*phoneNumberID,
		userID,
	).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPhoneNumberNotFound
		}
		return fmt.Errorf("lock phone number: %w", err)
	}

	const conflictQuery = `
		SELECT id
		FROM agents
		WHERE phone_number_id = $1
			AND ($2 = '' OR id <> $2)
		LIMIT 1`

	var conflictID string
	if err := tx.QueryRow(ctx, conflictQuery, *phoneNumberID, agentID).Scan(&conflictID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("check phone number assignment: %w", err)
	}
	return ErrPhoneNumberAssignmentConflict
}

// deref returns the pointed-to value or the zero value when the pointer is nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
