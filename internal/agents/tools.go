package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrToolNotFound is returned when an agent is attached to a tool that does not
// exist or is not owned by the authenticated user. The offending id is appended,
// since a request may carry several.
var ErrToolNotFound = errors.New("tool not found")

// normalizeToolIDs trims the supplied ids, drops the blanks and collapses
// duplicates while preserving first-seen order. Attaching the same tool twice is
// the same state as attaching it once, so the duplicate is dropped here rather
// than left to the primary key.
func normalizeToolIDs(ids []string) []string {
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

// resolveToolAttachments makes the agent's attachment set exactly ids, inside
// the caller's transaction. An empty (or nil) list detaches everything.
//
// Every id is first checked against the user's own tools: the join table's
// foreign key only proves a tool exists, not that this user owns it, so without
// this check an id guessed from another account would attach — and that account's
// endpoint would then be called with this agent's calls. A missing or foreign id
// fails the whole write with ErrToolNotFound, which reads the same either way so
// ownership never leaks.
//
// Attachments that survive the change are left in place rather than deleted and
// re-inserted, so created_at keeps meaning "attached since".
func (r *Repository) resolveToolAttachments(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	agentID string,
	ids []string,
) error {
	ids = normalizeToolIDs(ids)

	if len(ids) > 0 {
		// Report the first id the user cannot attach rather than a bare count, so
		// a request carrying several says which one was wrong.
		const missingQuery = `
			SELECT requested.id
			FROM unnest($1::text[]) AS requested(id)
			WHERE NOT EXISTS (
				SELECT 1 FROM tools t
				WHERE t.id = requested.id AND t.user_id = $2
			)
			LIMIT 1`

		var missingID string
		err := tx.QueryRow(ctx, missingQuery, ids, userID).Scan(&missingID)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s", ErrToolNotFound, missingID)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check tool ownership: %w", err)
		}
	}

	const deleteQuery = `
		DELETE FROM agent_tools
		WHERE agent_id = $1 AND tool_id <> ALL($2::text[])`
	if _, err := tx.Exec(ctx, deleteQuery, agentID, ids); err != nil {
		return fmt.Errorf("detach tools: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	const insertQuery = `
		INSERT INTO agent_tools (agent_id, tool_id)
		SELECT $1, requested.id FROM unnest($2::text[]) AS requested(id)
		ON CONFLICT (agent_id, tool_id) DO NOTHING`
	if _, err := tx.Exec(ctx, insertQuery, agentID, ids); err != nil {
		return fmt.Errorf("attach tools: %w", err)
	}
	return nil
}

// toolIDOrder is how attachments are read back everywhere: oldest attachment
// first, ties broken by id so a batch attached in one request has a stable order
// rather than whatever the table hands back.
const toolIDOrder = "created_at, tool_id"

// getToolIDs reads one agent's attached tool ids. The slice is always non-nil so
// the resource renders [] rather than null for an agent that takes no actions.
func (r *Repository) getToolIDs(ctx context.Context, agentID string) ([]string, error) {
	query := fmt.Sprintf(`
		SELECT tool_id
		FROM agent_tools
		WHERE agent_id = $1
		ORDER BY %s`, toolIDOrder)

	rows, err := r.pool.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select tool attachments: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tool attachment: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool attachments: %w", err)
	}
	return ids, nil
}

// getToolIDsForAgents loads the attachments for every agent id in one query,
// grouped by agent id, so listing a page of agents does not fan out into one
// query per agent. Agents with no attachments are absent from the result;
// buildResource turns that into an empty list.
func (r *Repository) getToolIDsForAgents(ctx context.Context, agentIDs []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(agentIDs) == 0 {
		return out, nil
	}

	query := fmt.Sprintf(`
		SELECT agent_id, tool_id
		FROM agent_tools
		WHERE agent_id = ANY($1)
		ORDER BY agent_id, %s`, toolIDOrder)

	rows, err := r.pool.Query(ctx, query, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("select tool attachments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var agentID, toolID string
		if err := rows.Scan(&agentID, &toolID); err != nil {
			return nil, fmt.Errorf("scan tool attachment: %w", err)
		}
		out[agentID] = append(out[agentID], toolID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool attachments: %w", err)
	}
	return out, nil
}

// ToolsForAgent loads the tools an agent may call, resolved down to what running
// one needs, in attachment order. It backs the live-call path: a call needs the
// function names and descriptions to declare to the model, and the configuration
// to execute when the model calls one.
//
// Like the live-agent lookups it runs alongside, it is unscoped by user: it is
// reached from call handling, which knows the agent but not who owns it, and an
// agent can only ever be attached to its owner's tools (enforced on attachment
// by resolveToolAttachments).
//
// A row whose stored configuration cannot be decoded is skipped rather than
// failing the call: one broken tool must not cost the caller the whole
// conversation. It is logged by the caller, which has the call id.
func (r *Repository) ToolsForAgent(ctx context.Context, agentID string) ([]models.AgentTool, error) {
	const query = `
		SELECT t.id, t.type, t.tool_name, t.description, t.config
		FROM agent_tools at
		JOIN tools t ON t.id = at.tool_id
		WHERE at.agent_id = $1
		ORDER BY at.created_at, at.tool_id`

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
