// Package broadcasts stores a user's voice broadcasts (the broadcasts table,
// whiskey migration 20261123000001_broadcasts). A user may create any number of
// broadcasts, each with one of their own voice agents selected. The agent is
// required when a broadcast is created or changed; it goes NULL only when the
// agent itself is deleted later.
package broadcasts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/httpx"
	"whatsapp-ai-caller-server/internal/models"
)

// MaxNameLength matches the broadcasts_name_check constraint.
const MaxNameLength = 80

// Broadcast is one broadcast as the API returns it. AgentName is read through
// the agent on every request, so renaming the agent shows up here at once.
type Broadcast struct {
	ID        string  `json:"id" example:"5c1f6f1e-8f0e-4a51-9b7e-2a3b4c5d6e7f"`
	Name      string  `json:"name" example:"December renewals"`
	AgentID   *string `json:"agent_id" example:"agent_12345"`
	AgentName *string `json:"agent_name" example:"Sales agent"`
	// Paused stops the auto-dialer for this broadcast. PausedReason is set when
	// the dialer paused it itself (number disconnected, minutes used up, ...).
	Paused       bool      `json:"paused" example:"false"`
	PausedReason *string   `json:"paused_reason" example:"phone number is not connected"`
	CreatedAt    time.Time `json:"created_at" example:"2026-11-23T10:00:00Z"`
	UpdatedAt    time.Time `json:"updated_at" example:"2026-11-23T10:00:00Z"`
}

// ErrNotFound is returned when the broadcast does not exist or belongs to
// someone else; the two are indistinguishable on purpose.
var ErrNotFound = errors.New("broadcast not found")

// ErrAgentNotFound is returned when the selected agent does not exist or is
// not the user's.
var ErrAgentNotFound = errors.New("agent not found")

// Repository persists broadcasts.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a broadcasts repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const selectColumns = `b.id, b.name, b.agent_id, a.agent_name, b.paused, b.paused_reason, b.created_at, b.updated_at`

func scan(row pgx.Row) (Broadcast, error) {
	var b Broadcast
	err := row.Scan(&b.ID, &b.Name, &b.AgentID, &b.AgentName, &b.Paused, &b.PausedReason, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// List returns all of the user's broadcasts, newest first.
func (r *Repository) List(ctx context.Context, userID string) ([]Broadcast, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+selectColumns+`
		FROM broadcasts b
		LEFT JOIN agents a ON a.id = b.agent_id
		WHERE b.user_id = $1
		ORDER BY b.created_at DESC, b.id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list broadcasts: %w", err)
	}
	defer rows.Close()
	out := make([]Broadcast, 0)
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan broadcast: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list broadcasts: %w", err)
	}
	return out, nil
}

// Get returns one of the user's broadcasts.
func (r *Repository) Get(ctx context.Context, userID, id string) (Broadcast, error) {
	b, err := scan(r.pool.QueryRow(ctx, `
		SELECT `+selectColumns+`
		FROM broadcasts b
		LEFT JOIN agents a ON a.id = b.agent_id
		WHERE b.user_id = $1 AND b.id = $2`, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Broadcast{}, ErrNotFound
	}
	if err != nil {
		return Broadcast{}, fmt.Errorf("get broadcast: %w", err)
	}
	return b, nil
}

// Create inserts a broadcast with the given agent, which must be the user's.
// The ownership check and the insert are one statement, so an agent of
// someone else's simply inserts nothing.
func (r *Repository) Create(ctx context.Context, userID, name, agentID string) (Broadcast, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO broadcasts (user_id, name, agent_id)
		SELECT $1, $2, a.id FROM agents a WHERE a.id = $3 AND a.user_id = $1
		RETURNING id`, userID, name, agentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Broadcast{}, ErrAgentNotFound
	}
	if err != nil {
		return Broadcast{}, fmt.Errorf("create broadcast: %w", err)
	}
	return r.Get(ctx, userID, id)
}

// Update changes the name, the agent and/or the paused switch. A nil field
// is left as it is. Resuming (paused=false) clears paused_reason.
func (r *Repository) Update(ctx context.Context, userID, id string, name, agentID *string, paused *bool) (Broadcast, error) {
	if agentID != nil {
		var owned bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1 AND user_id = $2)`,
			*agentID, userID).Scan(&owned); err != nil {
			return Broadcast{}, fmt.Errorf("check agent: %w", err)
		}
		if !owned {
			return Broadcast{}, ErrAgentNotFound
		}
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE broadcasts
		SET name = COALESCE($3, name),
		    agent_id = COALESCE($4, agent_id),
		    paused = COALESCE($5, paused),
		    paused_reason = CASE WHEN $5::boolean IS NULL THEN paused_reason ELSE NULL END,
		    updated_at = now()
		WHERE user_id = $1 AND id = $2`, userID, id, name, agentID, paused)
	if err != nil {
		return Broadcast{}, fmt.Errorf("update broadcast: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Broadcast{}, ErrNotFound
	}
	return r.Get(ctx, userID, id)
}

// Delete removes one of the user's broadcasts.
func (r *Repository) Delete(ctx context.Context, userID, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM broadcasts WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return fmt.Errorf("delete broadcast: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Controller serves broadcasts to the dashboard.
type Controller struct {
	repo *Repository
}

// NewController creates the broadcasts controller.
func NewController(repo *Repository) *Controller {
	return &Controller{repo: repo}
}

type createRequest struct {
	Name    string `json:"name"`
	AgentID string `json:"agent_id"`
}

type updateRequest struct {
	Name    *string `json:"name"`
	AgentID *string `json:"agent_id"`
	Paused  *bool   `json:"paused"`
}

// validateName trims the name and checks it fits the column constraint.
func validateName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "name is required"
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return "", fmt.Sprintf("name must be at most %d characters", MaxNameLength)
	}
	return name, ""
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (c *Controller) writeRepoError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "broadcast not found")
	case errors.Is(err, ErrAgentNotFound):
		httpx.WriteError(w, http.StatusBadRequest, "agent not found")
	default:
		httpx.WriteError(w, http.StatusInternalServerError, fallback)
	}
}

// List handles GET /v1/dashboard/voice/broadcasts.
func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	list, err := c.repo.List(r.Context(), user.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to list broadcasts")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Broadcasts retrieved successfully", Data: list})
}

// Get handles GET /v1/dashboard/voice/broadcasts/{broadcast_id}.
func (c *Controller) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	b, err := c.repo.Get(r.Context(), user.ID, chi.URLParam(r, "broadcast_id"))
	if err != nil {
		c.writeRepoError(w, err, "failed to load broadcast")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Broadcast retrieved successfully", Data: b})
}

// Create handles POST /v1/dashboard/voice/broadcasts.
func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	var req createRequest
	if !decode(w, r, &req) {
		return
	}
	name, msg := validateName(req.Name)
	if msg != "" {
		httpx.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	b, err := c.repo.Create(r.Context(), user.ID, name, agentID)
	if err != nil {
		c.writeRepoError(w, err, "failed to create broadcast")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, models.APIResponse{Success: true, Message: "Broadcast created successfully", Data: b})
}

// Update handles PATCH /v1/dashboard/voice/broadcasts/{broadcast_id}.
func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	var req updateRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil {
		name, msg := validateName(*req.Name)
		if msg != "" {
			httpx.WriteError(w, http.StatusBadRequest, msg)
			return
		}
		req.Name = &name
	}
	if req.AgentID != nil {
		agentID := strings.TrimSpace(*req.AgentID)
		if agentID == "" {
			httpx.WriteError(w, http.StatusBadRequest, "agent_id cannot be empty")
			return
		}
		req.AgentID = &agentID
	}
	b, err := c.repo.Update(r.Context(), user.ID, chi.URLParam(r, "broadcast_id"), req.Name, req.AgentID, req.Paused)
	if err != nil {
		c.writeRepoError(w, err, "failed to update broadcast")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Broadcast updated successfully", Data: b})
}

// Delete handles DELETE /v1/dashboard/voice/broadcasts/{broadcast_id}.
func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	if err := c.repo.Delete(r.Context(), user.ID, chi.URLParam(r, "broadcast_id")); err != nil {
		c.writeRepoError(w, err, "failed to delete broadcast")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Broadcast deleted successfully"})
}
