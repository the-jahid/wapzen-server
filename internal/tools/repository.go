// Package tools persists the actions an agent can take during a call into the
// tools table. A tool row only defines the action — which endpoint to hit, which
// number to hand the caller to, what message to send. It does nothing until an
// agent is attached to it, which is the agents package's half of the feature
// (agent_tools); this package owns the definitions themselves.
//
// The type-specific settings are stored as JSON in one column. The variants
// share almost no fields and are replaced wholesale rather than merged, so the
// shape is validated on the way in (handlers.validateTool...) and decoded back
// onto the block matching the row's type on the way out.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrNotFound is returned when a tool does not exist or is owned by another
// user. The two read the same so ownership never leaks.
var ErrNotFound = errors.New("tool not found")

// ErrNameTaken is returned when the owner already has a tool with the requested
// name. Names are unique per user, not globally: two tools sharing a name on one
// call would be ambiguous to the model.
var ErrNameTaken = errors.New("tool name already in use")

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-index conflict.
const uniqueViolation = "23505"

// toolColumns is the column list of the tools table, in the order scanTool
// reads them. Shared by every query that returns a row so a schema change is
// made in one place.
const toolColumns = `
	id, user_id, type, tool_name, description, config, created_at, updated_at
`

type dbQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Repository owns persistence for the tools table.
type Repository struct {
	db dbQuerier
}

// NewRepository creates a tool repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{db: pool}
}

// Create inserts a tool owned by params.UserID and returns the stored row. Only
// the configuration block matching params.Type is stored, so a request that
// supplies several cannot leave a tool carrying settings its type never reads.
func (r *Repository) Create(ctx context.Context, params models.NewTool) (models.Tool, error) {
	staged := models.Tool{
		Type:         params.Type,
		APIRequest:   params.APIRequest,
		TransferCall: params.TransferCall,
		SendText:     params.SendText,
		EndCall:      params.EndCall,
	}
	config, err := marshalConfig(staged.Config())
	if err != nil {
		return models.Tool{}, err
	}

	query := fmt.Sprintf(`
		INSERT INTO tools (user_id, type, tool_name, description, config)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING %s
	`, toolColumns)

	tool, err := scanTool(r.db.QueryRow(ctx, query, params.UserID, params.Type, params.Name, params.Description, config))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.Tool{}, ErrNameTaken
		}
		return models.Tool{}, fmt.Errorf("create tool: %w", err)
	}
	return tool, nil
}

// ListByUser returns one page of the user's tools, newest first, together with
// the total number that user owns so the caller can build pagination metadata.
// A non-empty toolType narrows the page to that type. limit and offset page the
// result; callers are expected to clamp them to sane bounds. The returned slice
// is always non-nil so an empty page serializes as [] rather than null.
func (r *Repository) ListByUser(ctx context.Context, userID, toolType string, limit, offset int) ([]models.Tool, int, error) {
	// The type filter is folded into the predicate with a NULL sentinel rather
	// than by building two query strings, so the count and the page can never
	// drift apart.
	var filter *string
	if trimmed := strings.TrimSpace(toolType); trimmed != "" {
		filter = &trimmed
	}

	const countQuery = `SELECT count(*) FROM tools WHERE user_id = $1 AND ($2::text IS NULL OR type = $2)`
	var total int
	if err := r.db.QueryRow(ctx, countQuery, userID, filter).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tools: %w", err)
	}

	out := make([]models.Tool, 0)
	if total == 0 {
		return out, 0, nil
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM tools
		WHERE user_id = $1 AND ($2::text IS NULL OR type = $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3 OFFSET $4
	`, toolColumns)

	rows, err := r.db.Query(ctx, query, userID, filter, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list tools: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		tool, err := scanTool(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan tool: %w", err)
		}
		out = append(out, tool)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate tools: %w", err)
	}
	return out, total, nil
}

// GetByUser loads one tool by id, scoped to the authenticated user. It returns
// ErrNotFound when no such tool exists for that user, so a tool owned by
// somebody else is indistinguishable from one that never existed.
func (r *Repository) GetByUser(ctx context.Context, userID, id string) (models.Tool, error) {
	query := fmt.Sprintf(`SELECT %s FROM tools WHERE id = $1 AND user_id = $2`, toolColumns)

	tool, err := scanTool(r.db.QueryRow(ctx, query, id, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Tool{}, ErrNotFound
		}
		return models.Tool{}, fmt.Errorf("get tool: %w", err)
	}
	return tool, nil
}

// UpdateByUser applies a partial update to one tool owned by the authenticated
// user and returns the stored row. Only the non-nil fields of params are
// written, so an untouched setting keeps its value.
//
// A supplied configuration block replaces the stored one wholesale, and only the
// block matching the tool's stored type is honoured — type cannot be changed, so
// a block for another type has nothing to describe. Callers are expected to
// reject that up front; ignoring it here keeps a mismatched block from silently
// overwriting the real configuration.
func (r *Repository) UpdateByUser(ctx context.Context, userID, id string, params models.ToolUpdate) (models.Tool, error) {
	if params.IsEmpty() {
		return models.Tool{}, fmt.Errorf("update tool: no fields to update")
	}

	set := []string{"updated_at = now()"}
	args := []any{id, userID}
	next := 3
	addField := func(column string, value any) {
		set = append(set, fmt.Sprintf("%s = $%d", column, next))
		args = append(args, value)
		next++
	}
	if params.Name != nil {
		addField("tool_name", *params.Name)
	}
	if params.Description != nil {
		addField("description", *params.Description)
	}

	// The stored type decides which block is written, and it is only known here
	// per row — hence the CASE-free approach of reading the type first. The read
	// also gives the 404 its own query, so an update against a missing tool never
	// depends on how many fields it happened to set.
	current, err := r.GetByUser(ctx, userID, id)
	if err != nil {
		return models.Tool{}, err
	}
	if block := updatedConfig(current.Type, params); block != nil {
		config, err := marshalConfig(block)
		if err != nil {
			return models.Tool{}, err
		}
		addField("config", config)
	}

	query := fmt.Sprintf(`
		UPDATE tools
		SET %s
		WHERE id = $1 AND user_id = $2
		RETURNING %s
	`, strings.Join(set, ", "), toolColumns)

	tool, err := scanTool(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Tool{}, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.Tool{}, ErrNameTaken
		}
		return models.Tool{}, fmt.Errorf("update tool: %w", err)
	}
	return tool, nil
}

// updatedConfig picks the configuration block an update writes, given the tool's
// stored type. It returns nil when the update carries no block for that type,
// which leaves the stored configuration untouched.
func updatedConfig(toolType string, params models.ToolUpdate) any {
	switch toolType {
	case models.ToolTypeAPIRequest:
		if params.APIRequest != nil {
			return params.APIRequest
		}
	case models.ToolTypeTransferCall:
		if params.TransferCall != nil {
			return params.TransferCall
		}
	case models.ToolTypeSendText:
		if params.SendText != nil {
			return params.SendText
		}
	case models.ToolTypeEndCall:
		if params.EndCall != nil {
			return params.EndCall
		}
	}
	return nil
}

// DeleteByUser removes one tool owned by the authenticated user. It returns
// ErrNotFound when no such tool exists for that user, so deleting somebody
// else's reads the same as deleting one that never existed.
//
// The agent_tools rows pointing at it cascade, so the tool is detached from
// every agent using it by the same statement. A call already in progress keeps
// the definitions it started with — it resolved them when it was answered.
func (r *Repository) DeleteByUser(ctx context.Context, userID, id string) error {
	const query = `DELETE FROM tools WHERE id = $1 AND user_id = $2`
	tag, err := r.db.Exec(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("delete tool: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// marshalConfig renders a configuration block for the config column. A type that
// takes no configuration stores an empty object rather than SQL NULL, so the
// column is never null and readers do not have to handle both.
func marshalConfig(block any) ([]byte, error) {
	if block == nil {
		return []byte("{}"), nil
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return nil, fmt.Errorf("encode tool configuration: %w", err)
	}
	return encoded, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTool(row rowScanner) (models.Tool, error) {
	var (
		tool   models.Tool
		config []byte
	)
	if err := row.Scan(
		&tool.ID,
		&tool.UserID,
		&tool.Type,
		&tool.Name,
		&tool.Description,
		&config,
		&tool.CreatedAt,
		&tool.UpdatedAt,
	); err != nil {
		return models.Tool{}, err
	}
	if err := DecodeConfig(&tool, config); err != nil {
		return models.Tool{}, err
	}
	return tool, nil
}

// DecodeConfig unmarshals a stored config column onto the block matching the
// tool's type, leaving the others nil. It is exported because the agents package
// reads the same column when it resolves an agent's tools for a call.
//
// A block that fails to decode is a stored row the server itself wrote, so it is
// a bug rather than user input — it is reported as an error rather than silently
// dropped, which would leave a tool that looks configured and does nothing.
func DecodeConfig(tool *models.Tool, config []byte) error {
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
	// Arrays default to nil when the stored block predates them; normalizing to
	// empty slices keeps the resource rendering [] rather than null.
	if tool.APIRequest != nil {
		if tool.APIRequest.Headers == nil {
			tool.APIRequest.Headers = []models.ToolHeader{}
		}
		if tool.APIRequest.Parameters == nil {
			tool.APIRequest.Parameters = []models.ToolParameter{}
		}
		if tool.APIRequest.TimeoutSeconds <= 0 {
			tool.APIRequest.TimeoutSeconds = models.ToolDefaultTimeoutSeconds
		}
	}
	return nil
}
