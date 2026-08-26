package apikeys

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
)

var (
	// ErrNotFound is returned when an API key does not exist, is revoked, or
	// does not belong to the authenticated user.
	ErrNotFound = errors.New("api key not found")

	// ErrLastActiveKey is returned when a user attempts to delete their only
	// remaining active API key.
	ErrLastActiveKey = errors.New("cannot delete the last api key")
)

// Repository owns persistence for user API keys.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates an API keys repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Create inserts a new user-owned API key and returns the one-time plaintext
// key together with its public metadata.
func (r *Repository) Create(ctx context.Context, userID, name string) (models.CreatedAPIKey, error) {
	return createWithQuerier(ctx, r.pool, userID, name, false)
}

// CreateDefaultWithQuerier creates the automatic default key for a user inside
// a caller-owned transaction. It is used by users.Repository when a user row is
// inserted for the first time.
func CreateDefaultWithQuerier(ctx context.Context, q rowQuerier, userID string) (models.CreatedAPIKey, error) {
	return createWithQuerier(ctx, q, userID, DefaultName, true)
}

// List returns active API keys for a user ordered newest-first.
func (r *Repository) List(ctx context.Context, userID string) ([]models.APIKey, error) {
	const query = `
		SELECT id, name, is_default, key_prefix, last4, last_used_at, created_at, updated_at
		FROM api_keys
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC
	`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	keys := make([]models.APIKey, 0)
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api keys: %w", err)
	}
	return keys, nil
}

// SetDefault makes one active user-owned key the default key.
func (r *Repository) SetDefault(ctx context.Context, userID, keyID string) (models.APIKey, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return models.APIKey{}, fmt.Errorf("begin set default api key tx: %w", err)
	}
	defer tx.Rollback(ctx)

	active, err := lockActiveKeys(ctx, tx, userID)
	if err != nil {
		return models.APIKey{}, err
	}
	if !active.has(keyID) {
		return models.APIKey{}, ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		UPDATE api_keys
		SET is_default = false, updated_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL AND is_default AND id <> $2
	`, userID, keyID); err != nil {
		return models.APIKey{}, fmt.Errorf("clear previous default api key: %w", err)
	}

	const updateQuery = `
		UPDATE api_keys
		SET is_default = true, updated_at = now()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		RETURNING id, name, is_default, key_prefix, last4, last_used_at, created_at, updated_at
	`

	key, err := scanAPIKey(tx.QueryRow(ctx, updateQuery, keyID, userID))
	if err != nil {
		return models.APIKey{}, fmt.Errorf("set default api key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.APIKey{}, fmt.Errorf("commit set default api key tx: %w", err)
	}
	return key, nil
}

// Revoke soft-deletes a user-owned key. Revoked keys no longer authenticate but
// remain in the database for audit history. A user must always retain at least
// one active key; if the revoked key was default, another active key is promoted.
func (r *Repository) Revoke(ctx context.Context, userID, keyID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin revoke api key tx: %w", err)
	}
	defer tx.Rollback(ctx)

	active, err := lockActiveKeys(ctx, tx, userID)
	if err != nil {
		return err
	}

	selected, ok := active.get(keyID)
	if !ok {
		return ErrNotFound
	}
	if len(active) == 1 {
		return ErrLastActiveKey
	}

	const revokeQuery = `
		UPDATE api_keys
		SET revoked_at = now(), is_default = false, updated_at = now()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
	`

	tag, err := tx.Exec(ctx, revokeQuery, keyID, userID)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	if selected.IsDefault || !active.hasDefaultBesides(keyID) {
		if err := promoteNewestActiveKey(ctx, tx, userID); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit revoke api key tx: %w", err)
	}
	return nil
}

// Authenticate verifies a bearer API key, marks it used, and returns the owner
// user row. It intentionally returns ErrNotFound for both unknown and revoked
// keys so callers never learn which keys exist.
func (r *Repository) Authenticate(ctx context.Context, bearerKey string) (models.User, error) {
	const query = `
		WITH touched AS (
			UPDATE api_keys
			SET last_used_at = now(), updated_at = now()
			WHERE key_hash = $1 AND revoked_at IS NULL
			RETURNING user_id
		)
		SELECT u.id, u.email, u.oauth_id, u.username, u.created_at, u.updated_at
		FROM touched t
		JOIN users u ON u.id = t.user_id
	`

	var user models.User
	if err := r.pool.QueryRow(ctx, query, Hash(bearerKey)).Scan(
		&user.ID,
		&user.Email,
		&user.OAuthID,
		&user.Username,
		&user.CreatedAt,
		&user.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.User{}, ErrNotFound
		}
		return models.User{}, fmt.Errorf("authenticate api key: %w", err)
	}
	return user, nil
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type rowScanner interface {
	Scan(dest ...any) error
}

func createWithQuerier(ctx context.Context, q rowQuerier, userID, name string, isDefault bool) (models.CreatedAPIKey, error) {
	key, err := Generate()
	if err != nil {
		return models.CreatedAPIKey{}, err
	}

	const query = `
		INSERT INTO api_keys (user_id, name, is_default, key_hash, key_prefix, last4)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, is_default, key_prefix, last4, last_used_at, created_at, updated_at
	`

	meta, err := scanAPIKey(q.QueryRow(ctx, query, userID, normalizeName(name), isDefault, Hash(key), publicPrefix(key), last4(key)))
	if err != nil {
		return models.CreatedAPIKey{}, fmt.Errorf("create api key: %w", err)
	}

	return models.CreatedAPIKey{
		APIKey: meta,
		Key:    key,
	}, nil
}

type activeKeyState struct {
	ID        string
	IsDefault bool
}

type activeKeyStates []activeKeyState

func (s activeKeyStates) get(id string) (activeKeyState, bool) {
	for _, key := range s {
		if key.ID == id {
			return key, true
		}
	}
	return activeKeyState{}, false
}

func (s activeKeyStates) has(id string) bool {
	_, ok := s.get(id)
	return ok
}

func (s activeKeyStates) hasDefaultBesides(id string) bool {
	for _, key := range s {
		if key.ID != id && key.IsDefault {
			return true
		}
	}
	return false
}

func lockActiveKeys(ctx context.Context, tx pgx.Tx, userID string) (activeKeyStates, error) {
	const query = `
		SELECT id, is_default
		FROM api_keys
		WHERE user_id = $1 AND revoked_at IS NULL
		FOR UPDATE
	`

	rows, err := tx.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("lock active api keys: %w", err)
	}
	defer rows.Close()

	keys := make(activeKeyStates, 0)
	for rows.Next() {
		var key activeKeyState
		if err := rows.Scan(&key.ID, &key.IsDefault); err != nil {
			return nil, fmt.Errorf("scan active api key lock: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active api key locks: %w", err)
	}
	return keys, nil
}

func promoteNewestActiveKey(ctx context.Context, tx pgx.Tx, userID string) error {
	var replacementID string
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM api_keys
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, userID).Scan(&replacementID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLastActiveKey
		}
		return fmt.Errorf("select replacement default api key: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE api_keys
		SET is_default = false, updated_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL AND is_default AND id <> $2
	`, userID, replacementID); err != nil {
		return fmt.Errorf("clear replacement default api key conflicts: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE api_keys
		SET is_default = true, updated_at = now()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
	`, replacementID, userID)
	if err != nil {
		return fmt.Errorf("promote replacement default api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLastActiveKey
	}
	return nil
}

func scanAPIKey(row rowScanner) (models.APIKey, error) {
	var key models.APIKey
	if err := row.Scan(
		&key.ID,
		&key.Name,
		&key.IsDefault,
		&key.KeyPrefix,
		&key.Last4,
		&key.LastUsedAt,
		&key.CreatedAt,
		&key.UpdatedAt,
	); err != nil {
		return models.APIKey{}, err
	}
	return key, nil
}
