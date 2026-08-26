package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/apikeys"
	"whatsapp-ai-caller-server/internal/models"
)

// UpsertParams describes the Clerk user fields mirrored into Postgres.
type UpsertParams struct {
	ClerkID  string
	Email    string
	Username *string
}

// Repository owns persistence for application users.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a users repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Upsert creates or updates a user by Clerk user ID. A newly inserted user also
// receives one automatic default API key; updates never create extra keys.
func (r *Repository) Upsert(ctx context.Context, params UpsertParams) (models.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return models.User{}, fmt.Errorf("begin user upsert tx: %w", err)
	}
	defer tx.Rollback(ctx)

	const insertQuery = `
		INSERT INTO users (email, oauth_id, username)
		VALUES ($1, $2, $3)
		ON CONFLICT (oauth_id) DO NOTHING
		RETURNING id, email, oauth_id, username, created_at, updated_at
	`

	user, err := scanUser(tx.QueryRow(ctx, insertQuery, params.Email, params.ClerkID, params.Username))
	if err == nil {
		if _, err := apikeys.CreateDefaultWithQuerier(ctx, tx, user.ID); err != nil {
			return models.User{}, fmt.Errorf("create default api key: %w", err)
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		const updateQuery = `
			UPDATE users
			SET email = $1,
				username = $3,
				updated_at = now()
			WHERE oauth_id = $2
			RETURNING id, email, oauth_id, username, created_at, updated_at
		`
		user, err = scanUser(tx.QueryRow(ctx, updateQuery, params.Email, params.ClerkID, params.Username))
		if err != nil {
			return models.User{}, fmt.Errorf("update user: %w", err)
		}
	} else {
		return models.User{}, fmt.Errorf("insert user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.User{}, fmt.Errorf("commit user upsert tx: %w", err)
	}
	return user, nil
}

// GetByClerkID returns the application user for a Clerk user ID.
func (r *Repository) GetByClerkID(ctx context.Context, clerkID string) (models.User, error) {
	const query = `
		SELECT id, email, oauth_id, username, created_at, updated_at
		FROM users
		WHERE oauth_id = $1
	`

	user, err := scanUser(r.pool.QueryRow(ctx, query, clerkID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.User{}, ErrNotFound
		}
		return models.User{}, fmt.Errorf("get user by clerk id: %w", err)
	}
	return user, nil
}

// DeleteByClerkID removes the application user for a Clerk user ID.
func (r *Repository) DeleteByClerkID(ctx context.Context, clerkID string) error {
	const query = `DELETE FROM users WHERE oauth_id = $1`

	if _, err := r.pool.Exec(ctx, query, clerkID); err != nil {
		return fmt.Errorf("delete user by clerk id: %w", err)
	}
	return nil
}

// ErrNotFound is returned when a user row does not exist.
var ErrNotFound = errors.New("user not found")

type userScanner interface {
	Scan(dest ...interface{}) error
}

func scanUser(row userScanner) (models.User, error) {
	var user models.User
	if err := row.Scan(
		&user.ID,
		&user.Email,
		&user.OAuthID,
		&user.Username,
		&user.CreatedAt,
		&user.UpdatedAt,
	); err != nil {
		return models.User{}, err
	}
	return user, nil
}
