// Package voiceusage tracks how many voice call minutes each account has used
// against its allowance (the voice_usage table, whiskey migration
// 20261121000005_voice_usage). Every account starts with DefaultLimitSeconds
// free; more minutes are bought by message and added by raising limit_seconds
// by hand.
//
// used_seconds is advanced by calls.Repository.MarkEnded in the same statement
// that ends a call, so it is a counter rather than a sum over the calls table:
// deleting calls from history never hands minutes back. This package only
// reads it — for the dashboard, and to decide whether a call may start and how
// long it may run.
package voiceusage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/httpx"
	"whatsapp-ai-caller-server/internal/models"
)

// DefaultLimitSeconds is the free allowance of every account: 10 minutes. It
// matches the column default of voice_usage.limit_seconds and applies to an
// account that has no row yet (nothing used).
const DefaultLimitSeconds = 600

// Usage is one account's voice minutes, in seconds.
type Usage struct {
	UsedSeconds      int  `json:"used_seconds" example:"125"`
	LimitSeconds     int  `json:"limit_seconds" example:"600"`
	RemainingSeconds int  `json:"remaining_seconds" example:"475"`
	Exhausted        bool `json:"exhausted" example:"false"`
}

// Remaining is how long the account may still talk, zero once used up.
func (u Usage) Remaining() time.Duration {
	return time.Duration(u.RemainingSeconds) * time.Second
}

func newUsage(used, limit int) Usage {
	remaining := max(limit-used, 0)
	return Usage{
		UsedSeconds:      used,
		LimitSeconds:     limit,
		RemainingSeconds: remaining,
		Exhausted:        remaining <= 0,
	}
}

// Repository reads voice_usage.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a voice usage repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Get returns the account's usage. An account with no row has used nothing and
// has the default allowance.
func (r *Repository) Get(ctx context.Context, userID string) (Usage, error) {
	const q = `SELECT used_seconds, limit_seconds FROM voice_usage WHERE user_id = $1`
	var used, limit int
	err := r.pool.QueryRow(ctx, q, userID).Scan(&used, &limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return newUsage(0, DefaultLimitSeconds), nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("get voice usage: %w", err)
	}
	return newUsage(used, limit), nil
}

// Controller serves the signed-in user's usage to the dashboard.
type Controller struct {
	repo *Repository
}

// NewController creates the usage controller.
func NewController(repo *Repository) *Controller {
	return &Controller{repo: repo}
}

// Get handles GET /v1/voice/usage.
func (c *Controller) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	usage, err := c.repo.Get(r.Context(), user.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to load voice usage")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "Voice usage retrieved successfully",
		Data:    usage,
	})
}
