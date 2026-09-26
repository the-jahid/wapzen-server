package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// toolOrder is how an agent's tools are read back everywhere: in the order they
// were attached, ties broken by tool id so the order is stable.
const toolOrder = "at.created_at, at.tool_id"

// attachTools makes the agent's tool set exactly ids, inside the caller's
// transaction. An empty (or nil) list detaches everything.
//
// Unlike a knowledge base, a tool is shared: the same definition can be attached
// to any number of agents, voice and chat alike, so there is no ownership to
// take away and nothing to refuse. What is still checked is that every id exists
// and belongs to this user — the foreign key only proves the row exists, so
// without this an id guessed from another account would attach, and that
// account's endpoint would then be called with this agent's calls.
//
// Attachments that survive the change are left alone, so an attachment that did
// not move keeps the date it was made on.
func attachTools(ctx context.Context, tx pgx.Tx, userID, agentID string, ids []string) error {
	ids = normalizeIDs(ids)

	if len(ids) > 0 {
		if err := verifyToolsOwned(ctx, tx, userID, ids); err != nil {
			return err
		}
	}

	// `<> ALL` of an empty array is true for every row, so this detaches the lot
	// when ids is empty, which is the documented meaning of an empty list.
	const detachQuery = `DELETE FROM agent_tools WHERE agent_id = $1 AND tool_id <> ALL($2::text[])`
	if _, err := tx.Exec(ctx, detachQuery, agentID, ids); err != nil {
		return fmt.Errorf("detach tools: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	const attachQuery = `
		INSERT INTO agent_tools (agent_id, tool_id)
		SELECT $1, id FROM unnest($2::text[]) AS id
		ON CONFLICT (agent_id, tool_id) DO NOTHING`
	if _, err := tx.Exec(ctx, attachQuery, agentID, ids); err != nil {
		return fmt.Errorf("attach tools: %w", err)
	}
	return nil
}

// verifyToolsOwned reports the first requested id the user cannot attach — one
// that does not exist, or is somebody else's. The two read the same, so
// ownership never leaks. The rows are locked so a tool cannot be deleted between
// the check and the insert, which would fail on the foreign key instead.
func verifyToolsOwned(ctx context.Context, tx pgx.Tx, userID string, ids []string) error {
	const query = `SELECT id FROM tools WHERE id = ANY($1::text[]) AND user_id = $2 ORDER BY id FOR SHARE`
	rows, err := tx.Query(ctx, query, ids, userID)
	if err != nil {
		return fmt.Errorf("lock tools: %w", err)
	}
	ownedIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("scan tool owners: %w", err)
	}

	owned := make(map[string]struct{}, len(ownedIDs))
	for _, id := range ownedIDs {
		owned[id] = struct{}{}
	}
	// Walk the request rather than the result, so the id reported is the first
	// one the user cannot attach — which matters when a request carries several.
	for _, id := range ids {
		if _, ok := owned[id]; !ok {
			return fmt.Errorf("%w: %s", ErrToolNotFound, id)
		}
	}
	return nil
}

// toolIDsForAgent reads one agent's tool ids. The slice is always non-nil so the
// resource renders [] rather than null.
func toolIDsForAgent(ctx context.Context, q querier, agentID string) ([]string, error) {
	query := fmt.Sprintf(`SELECT at.tool_id FROM agent_tools at WHERE at.agent_id = $1 ORDER BY %s`, toolOrder)

	rows, err := q.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select tool attachments: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan tool attachments: %w", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// toolIDsForAgents loads the tool ids of every agent in one query, grouped by
// agent id, so listing a page of agents does not fan out into one query per
// agent. Agents with none are absent from the result.
func toolIDsForAgents(ctx context.Context, q querier, agentIDs []string) (map[string][]string, error) {
	query := fmt.Sprintf(`
		SELECT at.agent_id, at.tool_id
		FROM agent_tools at
		WHERE at.agent_id = ANY($1::text[])
		ORDER BY at.agent_id, %s`, toolOrder)
	return groupIDsByAgent(ctx, q, query, agentIDs, "tool")
}

// ToolsForAgent loads the tools an agent may call, resolved down to what running
// one needs, in attachment order. It backs the live-call path: a call needs the
// function names and descriptions to declare to the model, and the configuration
// to execute when the model calls one.
//
// It is unscoped by user, like the lookups in runtime.go: an agent can only ever
// hold its owner's tools (enforced by attachTools).
//
// A row whose stored configuration cannot be decoded is skipped rather than
// failing the call: one broken tool must not cost the caller the whole
// conversation.
func (r *Repository) ToolsForAgent(ctx context.Context, agentID string) ([]models.AgentTool, error) {
	query := fmt.Sprintf(`
		SELECT t.id, t.type, t.tool_name, t.description, t.config
		FROM agent_tools at
		JOIN tools t ON t.id = at.tool_id
		WHERE at.agent_id = $1
		ORDER BY %s`, toolOrder)

	rows, err := r.pool.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select agent tools: %w", err)
	}
	defer rows.Close()

	out := make([]models.AgentTool, 0)
	for rows.Next() {
		var (
			tool   models.Tool
			config []byte
		)
		if err := rows.Scan(&tool.ID, &tool.Type, &tool.Name, &tool.Description, &config); err != nil {
			return nil, fmt.Errorf("scan agent tool: %w", err)
		}
		if err := decodeToolConfig(&tool, config); err != nil {
			continue
		}
		out = append(out, models.AgentTool{
			ID:           tool.ID,
			Type:         tool.Type,
			Name:         tool.Name,
			Description:  tool.Description,
			APIRequest:   tool.APIRequest,
			TransferCall: tool.TransferCall,
			SendText:     tool.SendText,
			EndCall:      tool.EndCall,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent tools: %w", err)
	}
	return out, nil
}

// decodeToolConfig unmarshals a stored config column onto the block matching the
// tool's type. It duplicates internal/tools.DecodeConfig deliberately: importing
// that package here would make the agents repository depend on the tools
// repository purely to read four small structs, and this path only ever reads.
func decodeToolConfig(tool *models.Tool, config []byte) error {
	if len(config) == 0 {
		return nil
	}
	var target any
	switch tool.Type {
	case models.ToolTypeAPIRequest:
		tool.APIRequest = &models.ToolAPIRequestConfig{}
		target = tool.APIRequest
	case models.ToolTypeTransferCall:
		tool.TransferCall = &models.ToolTransferCallConfig{}
		target = tool.TransferCall
	case models.ToolTypeSendText:
		tool.SendText = &models.ToolSendTextConfig{}
		target = tool.SendText
	case models.ToolTypeEndCall:
		tool.EndCall = &models.ToolEndCallConfig{}
		target = tool.EndCall
	default:
		return nil
	}
	if err := json.Unmarshal(config, target); err != nil {
		return fmt.Errorf("decode %s configuration for tool %s: %w", tool.Type, tool.ID, err)
	}
	if tool.APIRequest != nil && tool.APIRequest.TimeoutSeconds <= 0 {
		tool.APIRequest.TimeoutSeconds = models.ToolDefaultTimeoutSeconds
	}
	return nil
}
