package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrToolNotFound is returned when an agent is attached to a tool that does not
// exist or is not owned by the authenticated user. The offending id is appended,
// since a request may carry several.
var ErrToolNotFound = errors.New("tool not found")

// ErrToolAttachmentConflict is returned when an agent is attached to a tool
// another agent already owns. A tool belongs to one agent, so honouring the
// request would take it away from that agent mid-conversation; it has to be
// detached there first.
var ErrToolAttachmentConflict = errors.New("tool is already attached to another agent")

// normalizeToolIDs trims the supplied ids, drops the blanks and collapses
// duplicates while preserving first-seen order.
func normalizeToolIDs(ids []string) []string {
	return normalizeAttachmentIDs(ids)
}

// resolveToolAttachments makes the agent's tool set exactly ids, inside the
// caller's transaction. An empty (or nil) list detaches everything. See
// attachmentTable.resolve for the checks it makes.
func (r *Repository) resolveToolAttachments(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	agentID string,
	ids []string,
) error {
	return toolAttachments.resolve(ctx, tx, userID, agentID, ids)
}

// getToolIDs reads one agent's tool ids. The slice is always non-nil so the
// resource renders [] rather than null for an agent that takes no actions.
func (r *Repository) getToolIDs(ctx context.Context, agentID string) ([]string, error) {
	return toolAttachments.idsForAgent(ctx, r.pool, agentID)
}

// getToolIDsForAgents loads the attachments for every agent id in one query, so
// listing a page of agents does not fan out into one query per agent.
func (r *Repository) getToolIDsForAgents(ctx context.Context, agentIDs []string) (map[string][]string, error) {
	return toolAttachments.idsForAgents(ctx, r.pool, agentIDs)
}

// ToolsForAgent loads the tools an agent may call, resolved down to what running
// one needs, in attachment order. It backs the live-call path: a call needs the
// function names and descriptions to declare to the model, and the configuration
// to execute when the model calls one.
//
// Like the live-agent lookups it runs alongside, it is unscoped by user: it is
// reached from call handling, which knows the agent but not who owns it, and an
// agent can only ever own its owner's tools (enforced on attachment by
// resolveToolAttachments).
//
// A row whose stored configuration cannot be decoded is skipped rather than
// failing the call: one broken tool must not cost the caller the whole
// conversation. It is logged by the caller, which has the call id.
func (r *Repository) ToolsForAgent(ctx context.Context, agentID string) ([]models.AgentTool, error) {
	query := fmt.Sprintf(`
		SELECT id, type, tool_name, description, config
		FROM tools
		WHERE agent_id = $1
		ORDER BY %s`, attachmentOrder)

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
