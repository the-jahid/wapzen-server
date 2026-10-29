package activity

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository saves activity entries to the activity_logs table.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Insert saves one entry. An empty Detail is stored as NULL.
func (r *Repository) Insert(ctx context.Context, entry Entry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO activity_logs (user_id, email, event, message, detail)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
		entry.UserID, entry.Email, string(entry.Event), entry.Message, entry.Detail,
	)
	return err
}
