package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// InvalidRequestError signals a malformed PATCH payload: either the body is not
// a JSON object, or a supplied field has the wrong JSON type for its column. The
// handler maps it to 400 and surfaces the message.
type InvalidRequestError struct{ msg string }

func (e *InvalidRequestError) Error() string { return e.msg }

// Update applies a partial update to the agent with the given id owned by
// userID. Only the fields present in body are changed; absent fields keep
// their stored value. The three child collections (dynamic variables,
// post-call analysis fields and knowledge base attachments) are replaced
// wholesale when their key is present, and left untouched when it is absent. It
// returns ErrAgentNotFound when no agent has the given id — including when the
// agent belongs to another user — and an *InvalidRequestError when the body
// cannot be interpreted.
func (r *Repository) Update(ctx context.Context, userID, agentID string, body []byte) (types.AgentResource, error) {
	set, dynVars, postCall, knowledgeBases, toolIDs, err := parseAgentUpdate(body)
	if err != nil {
		return types.AgentResource{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return types.AgentResource{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	childChanged := dynVars != nil || postCall != nil || knowledgeBases != nil || toolIDs != nil

	// Load the persisted scalar row (post-update) and confirm the agent exists.
	var row agentRow
	switch {
	case len(set.cols) > 0:
		// Write the supplied scalar columns and bump updated_at, returning the
		// row Postgres actually persisted.
		assignments := make([]string, len(set.cols))
		for i, c := range set.cols {
			assignments[i] = fmt.Sprintf("%s = $%d", c, i+1)
		}
		args := append(append([]any{}, set.args...), agentID, userID)
		query := fmt.Sprintf(
			"UPDATE agents SET %s, updated_at = now() WHERE id = $%d AND user_id = $%d RETURNING %s",
			strings.Join(assignments, ", "), len(args)-1, len(args), returningColumns,
		)
		if err := scanAgentRow(tx.QueryRow(ctx, query, args...), &row); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return types.AgentResource{}, ErrAgentNotFound
			}
			return types.AgentResource{}, fmt.Errorf("update agent: %w", err)
		}
	case childChanged:
		// No scalar columns changed, but a child collection did: bump updated_at
		// (and confirm existence) without touching any other column.
		query := fmt.Sprintf(
			"UPDATE agents SET updated_at = now() WHERE id = $1 AND user_id = $2 RETURNING %s",
			returningColumns,
		)
		if err := scanAgentRow(tx.QueryRow(ctx, query, agentID, userID), &row); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return types.AgentResource{}, ErrAgentNotFound
			}
			return types.AgentResource{}, fmt.Errorf("update agent: %w", err)
		}
	default:
		// Empty patch: nothing to change. Return the current resource unchanged.
		query := fmt.Sprintf("SELECT %s FROM agents WHERE id = $1 AND user_id = $2", returningColumns)
		if err := scanAgentRow(tx.QueryRow(ctx, query, agentID, userID), &row); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return types.AgentResource{}, ErrAgentNotFound
			}
			return types.AgentResource{}, fmt.Errorf("select agent: %w", err)
		}
	}

	// Enforce the go-live invariant: an agent may only be active while it has a
	// phone number assigned — without one it has no number to place or answer
	// calls on. Guard only when this request touched the status or the phone
	// assignment, so unrelated edits to a pre-existing row are never blocked.
	if columnsInclude(set.cols, "status") || columnsInclude(set.cols, "phone_number_id") {
		if row.Status == "active" && !hasPhoneNumberAssigned(row.PhoneNumberID) {
			return types.AgentResource{}, &InvalidRequestError{
				"assign a phone number before taking this agent live",
			}
		}
	}

	if dynVars != nil {
		if err := replaceDynamicVariables(ctx, tx, agentID, *dynVars); err != nil {
			return types.AgentResource{}, err
		}
	}
	if postCall != nil {
		if err := replacePostCallFields(ctx, tx, agentID, *postCall); err != nil {
			return types.AgentResource{}, err
		}
	}
	if knowledgeBases != nil {
		if err := r.resolveKnowledgeBaseAttachments(ctx, tx, userID, agentID, *knowledgeBases); err != nil {
			return types.AgentResource{}, err
		}
	}
	if toolIDs != nil {
		if err := r.resolveToolAttachments(ctx, tx, userID, agentID, *toolIDs); err != nil {
			return types.AgentResource{}, err
		}
	}

	if err := r.resolvePhoneNumberAssignment(ctx, tx, userID, row.ID, row.PhoneNumberID); err != nil {
		return types.AgentResource{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return types.AgentResource{}, fmt.Errorf("commit tx: %w", err)
	}

	// Re-read the child collections so the response reflects their persisted
	// state whether or not this request replaced them.
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
	attachedToolIDs, err := r.getToolIDs(ctx, agentID)
	if err != nil {
		return types.AgentResource{}, err
	}

	return buildResource(row, dynamicVars, postCallData, knowledgeBaseIDs, attachedToolIDs), nil
}

// replaceDynamicVariables swaps an agent's dynamic-variable set for the supplied
// map, deleting any previous entries first. An empty map clears them all.
func replaceDynamicVariables(ctx context.Context, tx pgx.Tx, agentID string, vars map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM agent_dynamic_variables WHERE agent_id = $1`, agentID); err != nil {
		return fmt.Errorf("delete dynamic variables: %w", err)
	}
	for name, value := range vars {
		if err := insertDynamicVariable(ctx, tx, agentID, name, value); err != nil {
			return err
		}
	}
	return nil
}

// replacePostCallFields swaps an agent's post-call analysis fields for the
// supplied list, deleting any previous fields first. An empty list clears them.
func replacePostCallFields(ctx context.Context, tx pgx.Tx, agentID string, fields []types.PostCallField) error {
	if _, err := tx.Exec(ctx, `DELETE FROM agent_post_call_fields WHERE agent_id = $1`, agentID); err != nil {
		return fmt.Errorf("delete post-call fields: %w", err)
	}
	for _, f := range fields {
		if err := insertPostCallField(ctx, tx, agentID, f); err != nil {
			return err
		}
	}
	return nil
}

// rawFields is one JSON object level left undecoded, so presence of a key can be
// distinguished from a key set to its type's zero value — the crux of PATCH
// semantics that a typed struct cannot express.
type rawFields map[string]json.RawMessage

// updateSet accumulates the "column = $n" assignments for the scalar UPDATE. The
// first decoding error is retained so callers can run a straight-line sequence of
// setScalar calls and check err once at the end.
type updateSet struct {
	cols []string
	args []any
	err  error
}

func (u *updateSet) add(col string, val any) {
	u.cols = append(u.cols, col)
	u.args = append(u.args, val)
}

// sub descends into a nested JSON object. A missing key or explicit null yields a
// nil rawFields, which every subsequent setScalar treats as "nothing to set".
func (u *updateSet) sub(fields rawFields, key string) rawFields {
	if u.err != nil || fields == nil {
		return nil
	}
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var sub rawFields
	if err := json.Unmarshal(raw, &sub); err != nil {
		u.err = &InvalidRequestError{fmt.Sprintf("invalid value for %q", key)}
		return nil
	}
	return sub
}

// setScalar records "col = value" when key is present in fields. T is chosen per
// column: a plain type for NOT NULL columns, and a pointer type for nullable
// columns so an explicit JSON null decodes to a nil pointer and clears the
// column. An absent key changes nothing.
func setScalar[T any](u *updateSet, fields rawFields, key, col string) {
	if u.err != nil || fields == nil {
		return
	}
	raw, ok := fields[key]
	if !ok {
		return
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		u.err = &InvalidRequestError{fmt.Sprintf("invalid value for %q", key)}
		return
	}
	u.add(col, v)
}

// columnsInclude reports whether the scalar UPDATE touched the given column.
func columnsInclude(cols []string, col string) bool {
	for _, c := range cols {
		if c == col {
			return true
		}
	}
	return false
}

// hasPhoneNumberAssigned reports whether a persisted phone_number_id holds a
// real assignment rather than NULL or a blank string.
func hasPhoneNumberAssigned(id *string) bool {
	return id != nil && strings.TrimSpace(*id) != ""
}

func setOptionalTrimmedString(u *updateSet, fields rawFields, key, col string) {
	if u.err != nil || fields == nil {
		return
	}
	raw, ok := fields[key]
	if !ok {
		return
	}
	var v *string
	if err := json.Unmarshal(raw, &v); err != nil {
		u.err = &InvalidRequestError{fmt.Sprintf("invalid value for %q", key)}
		return
	}
	u.add(col, normalizePhoneNumberID(v))
}

// parseAgentUpdate maps a partial-update JSON body onto scalar column
// assignments plus the four optional child collections. A non-nil dynVars,
// postCall, knowledgeBases or tools means "replace this collection" (even when
// the map/list is empty); nil means "leave it unchanged".
func parseAgentUpdate(body []byte) (*updateSet, *map[string]string, *[]types.PostCallField, *[]string, *[]string, error) {
	var top rawFields
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, nil, nil, nil, nil, &InvalidRequestError{"request body must be a JSON object"}
	}

	u := &updateSet{}

	agent := u.sub(top, "agent")
	setScalar[string](u, agent, "name", "agent_name")
	setScalar[string](u, agent, "language", "language")
	setScalar[*string](u, agent, "timezone", "timezone")
	setOptionalTrimmedString(u, agent, "phone_number_id", "phone_number_id")
	setScalar[string](u, agent, "call_direction", "call_direction")
	setScalar[string](u, agent, "status", "status")

	model := u.sub(top, "model")
	setScalar[string](u, model, "provider", "model_provider")
	setScalar[string](u, model, "name", "model_name")
	setScalar[*float64](u, model, "temperature", "model_temperature")

	prompt := u.sub(top, "prompt")
	setScalar[string](u, prompt, "begin_message_mode", "prompt_begin_message_mode")
	setScalar[*string](u, prompt, "begin_message", "prompt_begin_message")
	setScalar[*int](u, prompt, "begin_message_delay_ms", "prompt_begin_message_delay_ms")
	setScalar[*string](u, prompt, "system_prompt", "prompt_system_prompt")

	voice := u.sub(top, "voice")
	setScalar[*string](u, voice, "provider", "voice_provider")
	el := u.sub(voice, "elevenlabs")
	setScalar[string](u, el, "voice_id", "voice_elevenlabs_voice_id")
	setScalar[string](u, el, "voice_name", "voice_elevenlabs_voice_name")
	setScalar[string](u, el, "voice_model", "voice_elevenlabs_voice_model")
	oa := u.sub(voice, "openai")
	setScalar[string](u, oa, "voice_id", "voice_openai_voice_id")
	setScalar[string](u, oa, "voice_name", "voice_openai_voice_name")
	setScalar[string](u, oa, "voice_model", "voice_openai_voice_model")
	setScalar[string](u, oa, "realtime_model", "voice_openai_realtime_model")
	setScalar[*string](u, oa, "instructions", "voice_openai_instructions")
	setScalar[float64](u, oa, "speed", "voice_openai_speed")
	setScalar[float64](u, oa, "volume", "voice_openai_volume")

	transcriber := u.sub(top, "transcriber")
	setScalar[string](u, transcriber, "provider", "transcriber_provider")
	setScalar[string](u, transcriber, "language", "transcriber_language")
	setScalar[string](u, u.sub(transcriber, "openai"), "model", "transcriber_openai_model")
	setScalar[string](u, u.sub(transcriber, "elevenlabs"), "model", "transcriber_elevenlabs_model")

	postCallSec := u.sub(top, "post_call")
	setScalar[string](u, postCallSec, "analysis_provider", "post_call_analysis_provider")
	setScalar[*string](u, postCallSec, "analysis_model", "post_call_analysis_model")

	knowledgeBaseSec := u.sub(top, "knowledge_base")
	toolsSec := u.sub(top, "tools")

	// Child collections: a present key (even null) means "replace"; absent means
	// "leave unchanged".
	dynVars := parseChild[map[string]string](u, prompt, "dynamic_variables")
	postCall := parseChild[[]types.PostCallField](u, postCallSec, "post_call_analysis_data")
	knowledgeBases := parseChild[[]string](u, knowledgeBaseSec, "knowledge_base_ids")
	toolIDs := parseChild[[]string](u, toolsSec, "tool_ids")

	if u.err != nil {
		return nil, nil, nil, nil, nil, u.err
	}
	return u, dynVars, postCall, knowledgeBases, toolIDs, nil
}

// parseChild decodes an optional child collection. It returns a non-nil pointer
// whenever key is present (so an empty or null value still means "replace with
// nothing"), and nil when the key is absent.
func parseChild[T any](u *updateSet, fields rawFields, key string) *T {
	if u.err != nil || fields == nil {
		return nil
	}
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		u.err = &InvalidRequestError{fmt.Sprintf("invalid value for %q", key)}
		return nil
	}
	return &v
}
