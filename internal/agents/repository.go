package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// Repository is the data layer for agents: every SQL statement against the
// agents table and its attachments lives in this package's repository files. It
// enforces what has to hold inside a transaction — ownership of attached rows,
// one agent per phone number, the go-live rule on update — and translates
// Postgres errors into this package's domain errors. Request validation and the
// side effects of a change belong to Service.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates an agents repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// DefaultAgentName is the name of the starter agent every new account gets.
const DefaultAgentName = "My First Agent"

// CreateDefault inserts the starter agent for a brand-new user. It runs inside
// the transaction that creates the user (main registers it as a
// users.NewUserHook), so the user and the agent exist together or not at all.
// Every setting falls back to its schema default. It starts paused: a new
// account has no phone number yet, and an agent may only be live with one.
func (r *Repository) CreateDefault(ctx context.Context, tx pgx.Tx, userID string) error {
	const query = `INSERT INTO agents (user_id, agent_name, status) VALUES ($1, $2, 'inactive')`
	if _, err := tx.Exec(ctx, query, userID, DefaultAgentName); err != nil {
		return fmt.Errorf("insert default agent: %w", err)
	}
	return nil
}

// Create inserts a new agent owned by userID together with its knowledge base
// and tool attachments, in one transaction. Only the fields present in req are
// written; everything else falls back to its schema default (see migration
// 00002), so Go zero-values never clobber a DB default.
func (r *Repository) Create(ctx context.Context, userID string, req types.CreateAgentRequest) (types.AgentResource, error) {
	res, err := r.create(ctx, userID, req)
	return res, translateWriteError(err)
}

func (r *Repository) create(ctx context.Context, userID string, req types.CreateAgentRequest) (types.AgentResource, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return types.AgentResource{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := resolvePhoneNumberAssignment(ctx, tx, userID, "", requestPhoneNumberID(req)); err != nil {
		return types.AgentResource{}, err
	}

	columns := insertColumns(userID, req)
	query := fmt.Sprintf(
		"INSERT INTO agents (%s) VALUES (%s) RETURNING %s",
		strings.Join(columns.cols, ", "), placeholders(len(columns.cols)), returningColumns,
	)
	var row agentRow
	if err := scanAgentRow(tx.QueryRow(ctx, query, columns.args...), &row); err != nil {
		return types.AgentResource{}, fmt.Errorf("insert agent: %w", err)
	}

	knowledgeBaseIDs := requestKnowledgeBaseIDs(req)
	if err := attachKnowledgeBases(ctx, tx, userID, row.ID, knowledgeBaseIDs); err != nil {
		return types.AgentResource{}, err
	}
	toolIDs := requestToolIDs(req)
	if err := attachTools(ctx, tx, userID, row.ID, toolIDs); err != nil {
		return types.AgentResource{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return types.AgentResource{}, fmt.Errorf("commit tx: %w", err)
	}
	return buildResource(row, knowledgeBaseIDs, toolIDs), nil
}

// insertColumns lists the columns a create request supplies. user_id and
// agent_name are always written; every other column only when the request
// carries a non-zero value for it.
func insertColumns(userID string, req types.CreateAgentRequest) columnSet {
	var s columnSet
	s.add("user_id", userID)

	agent := req.Agent
	if agent == nil {
		agent = &types.AgentSection{}
	}
	s.add("agent_name", agent.Name)
	addNonZero(&s, "language", agent.Language)
	addNonNil(&s, "timezone", agent.Timezone)
	addNonNil(&s, "phone_number_id", normalizePhoneNumberID(agent.PhoneNumberID))
	addNonZero(&s, "call_direction", agent.CallDirection)
	addNonZero(&s, "status", agent.Status)

	if m := req.Model; m != nil {
		addNonZero(&s, "model_provider", m.Provider)
		addNonZero(&s, "model_name", m.Name)
		addNonZero(&s, "model_temperature", m.Temperature)
	}

	if p := req.Prompt; p != nil {
		addNonZero(&s, "prompt_begin_message_mode", p.BeginMessageMode)
		addNonZero(&s, "prompt_begin_message", p.BeginMessage)
		addNonZero(&s, "prompt_begin_message_delay_ms", p.BeginMessageDelayMs)
		addNonZero(&s, "prompt_system_prompt", p.SystemPrompt)
	}

	if v := req.Voice; v != nil {
		addNonZero(&s, "voice_provider", v.Provider)
		if el := v.ElevenLabs; el != nil {
			addNonZero(&s, "voice_elevenlabs_voice_id", el.VoiceID)
			addNonZero(&s, "voice_elevenlabs_voice_name", el.VoiceName)
			addNonZero(&s, "voice_elevenlabs_voice_model", el.VoiceModel)
		}
		if oa := v.OpenAI; oa != nil {
			addNonZero(&s, "voice_openai_voice_id", oa.VoiceID)
			addNonZero(&s, "voice_openai_voice_name", oa.VoiceName)
			addNonZero(&s, "voice_openai_voice_model", oa.VoiceModel)
			addNonZero(&s, "voice_openai_realtime_model", oa.RealtimeModel)
			addNonNil(&s, "voice_openai_instructions", oa.Instructions)
			addNonZero(&s, "voice_openai_speed", oa.Speed)
			addNonZero(&s, "voice_openai_volume", oa.Volume)
		}
	}

	if t := req.Transcriber; t != nil {
		addNonZero(&s, "transcriber_provider", t.Provider)
		addNonZero(&s, "transcriber_language", t.Language)
		if t.OpenAI != nil {
			addNonZero(&s, "transcriber_openai_model", t.OpenAI.Model)
		}
		if t.ElevenLabs != nil {
			addNonZero(&s, "transcriber_elevenlabs_model", t.ElevenLabs.Model)
		}
	}

	if pc := req.PostCall; pc != nil {
		addNonZero(&s, "post_call_analysis_provider", pc.AnalysisProvider)
		addNonNil(&s, "post_call_analysis_model", pc.AnalysisModel)
	}
	return s
}

// GetByID loads a single agent owned by userID together with its attachments.
// It returns ErrAgentNotFound when no agent has the given id — including when
// the agent exists but belongs to another user, so ownership never leaks.
func (r *Repository) GetByID(ctx context.Context, userID, agentID string) (types.AgentResource, error) {
	query := fmt.Sprintf("SELECT %s FROM agents WHERE id = $1 AND user_id = $2", returningColumns)

	var row agentRow
	if err := scanAgentRow(r.pool.QueryRow(ctx, query, agentID, userID), &row); err != nil {
		return types.AgentResource{}, notFoundOr(err, "select agent")
	}
	return r.withAttachments(ctx, row)
}

// List returns a page of the agents owned by userID ordered newest-first,
// together with the total number of matching agents (for pagination metadata).
// Attachments are loaded in bulk for the whole page to avoid a per-agent query
// fan-out.
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

	knowledgeBaseIDs, err := knowledgeBaseIDsForAgents(ctx, r.pool, ids)
	if err != nil {
		return nil, 0, err
	}
	toolIDs, err := toolIDsForAgents(ctx, r.pool, ids)
	if err != nil {
		return nil, 0, err
	}

	resources := make([]types.AgentResource, len(agentRows))
	for i, row := range agentRows {
		resources[i] = buildResource(row, knowledgeBaseIDs[row.ID], toolIDs[row.ID])
	}
	return resources, total, nil
}

// Update applies a parsed partial update to the agent with the given id owned by
// userID, in one transaction. Only the patch's columns are written; each
// attachment set is replaced wholesale when the patch carries it and left alone
// otherwise. It returns ErrAgentNotFound when no agent has the given id —
// including when the agent belongs to another user.
func (r *Repository) Update(ctx context.Context, userID, agentID string, patch agentPatch) (types.AgentResource, error) {
	res, err := r.update(ctx, userID, agentID, patch)
	return res, translateWriteError(err)
}

func (r *Repository) update(ctx context.Context, userID, agentID string, patch agentPatch) (types.AgentResource, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return types.AgentResource{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	row, err := updateRow(ctx, tx, userID, agentID, patch)
	if err != nil {
		return types.AgentResource{}, err
	}

	// The go-live rule: an agent may only be active while it has a phone number,
	// since without one it has nothing to place or answer calls on. It is checked
	// here, against the row as persisted inside this transaction, and only when
	// this request touched the status or the number — so unrelated edits to an
	// older row that predates the rule are never blocked. (Create applies the
	// same rule up front; see Service.Create.)
	if patch.columns.has("status") || patch.columns.has("phone_number_id") {
		if row.Status == "active" && !hasPhoneNumberAssigned(row.PhoneNumberID) {
			return types.AgentResource{}, invalid("assign a phone number before taking this agent live")
		}
	}

	if patch.knowledgeBaseIDs != nil {
		if err := attachKnowledgeBases(ctx, tx, userID, agentID, *patch.knowledgeBaseIDs); err != nil {
			return types.AgentResource{}, err
		}
	}
	if patch.toolIDs != nil {
		if err := attachTools(ctx, tx, userID, agentID, *patch.toolIDs); err != nil {
			return types.AgentResource{}, err
		}
	}

	if err := resolvePhoneNumberAssignment(ctx, tx, userID, row.ID, row.PhoneNumberID); err != nil {
		return types.AgentResource{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return types.AgentResource{}, fmt.Errorf("commit tx: %w", err)
	}

	// Re-read the attachments so the response reflects their persisted state
	// whether or not this request replaced them.
	return r.withAttachments(ctx, row)
}

// updateRow writes the patch's scalar columns and returns the persisted row. A
// patch that only replaces attachments still bumps updated_at; an empty patch
// just reads the row back unchanged. A missing agent is ErrAgentNotFound.
func updateRow(ctx context.Context, tx pgx.Tx, userID, agentID string, patch agentPatch) (agentRow, error) {
	var (
		query string
		args  []any
	)
	switch cols := patch.columns.cols; {
	case len(cols) > 0:
		assignments := make([]string, len(cols))
		for i, col := range cols {
			assignments[i] = fmt.Sprintf("%s = $%d", col, i+1)
		}
		args = append(append([]any{}, patch.columns.args...), agentID, userID)
		query = fmt.Sprintf(
			"UPDATE agents SET %s, updated_at = now() WHERE id = $%d AND user_id = $%d RETURNING %s",
			strings.Join(assignments, ", "), len(args)-1, len(args), returningColumns,
		)
	case patch.changesAttachments():
		args = []any{agentID, userID}
		query = fmt.Sprintf(
			"UPDATE agents SET updated_at = now() WHERE id = $1 AND user_id = $2 RETURNING %s",
			returningColumns,
		)
	default:
		args = []any{agentID, userID}
		query = fmt.Sprintf("SELECT %s FROM agents WHERE id = $1 AND user_id = $2", returningColumns)
	}

	var row agentRow
	if err := scanAgentRow(tx.QueryRow(ctx, query, args...), &row); err != nil {
		return agentRow{}, notFoundOr(err, "update agent")
	}
	return row, nil
}

// Delete removes the agent with the given id owned by userID. It returns
// ErrAgentNotFound when no agent has the given id — including when the agent
// belongs to another user.
//
// The knowledge bases it owns go with it, by the ON DELETE CASCADE on their
// agent_id: they are its property, not links to shared rows, so this destroys
// them rather than detaching them. What the cascade cannot reach is the vector
// store — see Service.Delete, which reads the doomed namespaces first and purges
// them after this succeeds.
//
// Tools survive: they are shared, so the cascade reaches only this agent's rows
// in agent_tools. A phone number is only referenced, so it is released (ON
// DELETE SET NULL) and survives too.
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

// withAttachments loads the agent's attachment ids and assembles its resource.
func (r *Repository) withAttachments(ctx context.Context, row agentRow) (types.AgentResource, error) {
	knowledgeBaseIDs, err := knowledgeBaseIDsForAgent(ctx, r.pool, row.ID)
	if err != nil {
		return types.AgentResource{}, err
	}
	toolIDs, err := toolIDsForAgent(ctx, r.pool, row.ID)
	if err != nil {
		return types.AgentResource{}, err
	}
	return buildResource(row, knowledgeBaseIDs, toolIDs), nil
}

// translateWriteError turns the Postgres errors a create or update can hit into
// domain errors. Anything else is returned unchanged.
func translateWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23503": // foreign_key_violation
		if pgErr.ConstraintName == "agents_phone_number_id_fkey" {
			return ErrPhoneNumberNotFound
		}
		return ErrUserNotFound // the owning user was deleted mid-request
	case "23505": // unique_violation: the only one on agents is (user_id, agent_name)
		return ErrAgentNameTaken
	case "23514", "23502", "22P02": // check / not-null / invalid input
		return invalid("%s", pgErr.Message)
	}
	return err
}

// notFoundOr maps a missing row to ErrAgentNotFound and wraps anything else.
func notFoundOr(err error, action string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAgentNotFound
	}
	return fmt.Errorf("%s: %w", action, err)
}

// placeholders renders "$1, $2, …, $n".
func placeholders(n int) string {
	ps := make([]string, n)
	for i := range ps {
		ps[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(ps, ", ")
}

// requestPhoneNumberID returns the phone number a create request assigns, or nil.
func requestPhoneNumberID(req types.CreateAgentRequest) *string {
	if req.Agent == nil {
		return nil
	}
	return normalizePhoneNumberID(req.Agent.PhoneNumberID)
}

// requestKnowledgeBaseIDs returns the knowledge base ids a create request
// attaches, normalized. An absent section attaches nothing.
func requestKnowledgeBaseIDs(req types.CreateAgentRequest) []string {
	if req.KnowledgeBase == nil {
		return nil
	}
	return normalizeIDs(req.KnowledgeBase.KnowledgeBaseIDs)
}

// requestToolIDs returns the tool ids a create request attaches, normalized. An
// absent section attaches nothing.
func requestToolIDs(req types.CreateAgentRequest) []string {
	if req.Tools == nil {
		return nil
	}
	return normalizeIDs(req.Tools.ToolIDs)
}

// normalizeIDs trims attachment ids, drops the blanks and collapses duplicates
// while preserving first-seen order. Attaching the same row twice is the same
// state as attaching it once.
func normalizeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// querier is the read half of pgxpool.Pool and pgx.Tx, so the attachment
// readers can run on either.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
