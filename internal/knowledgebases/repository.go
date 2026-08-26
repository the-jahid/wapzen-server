// Package knowledgebases persists knowledge bases into the knowledge_bases
// table and their sources into knowledge_base_sources. A knowledge base is the
// container an agent retrieves from: Postgres owns the container row, its
// indexing status and the source text, while the embeddings that text is
// actually retrieved by live in the vector store, under the row's namespace.
//
// Indexer is the piece that spans the two, turning a stored source into the
// vectors of its namespace.
package knowledgebases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrNotFound is returned when a knowledge base does not exist or is owned by
// another user.
var ErrNotFound = errors.New("knowledge base not found")

// ErrNameTaken is returned when the owner already has a knowledge base with the
// requested name. Names are unique per user, not globally.
var ErrNameTaken = errors.New("knowledge base name already in use")

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-index conflict.
const uniqueViolation = "23505"

// knowledgeBaseColumns is the column list of the knowledge_bases table, in the
// order scanKnowledgeBase reads them. Shared by every query that returns a row
// so a schema change is made in one place.
const knowledgeBaseColumns = `
	id, user_id, knowledge_base_name, status, pinecone_namespace,
	enable_auto_refresh, last_refreshed_at, max_chunk_size, min_chunk_size
`

type dbQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Repository owns persistence for the knowledge_bases table and the
// knowledge_base_sources rows hanging off it.
type Repository struct {
	db dbQuerier
	// pool is kept alongside db for the writes that span several statements and
	// therefore need a transaction, which the query interface cannot open.
	pool *pgxpool.Pool
}

// NewRepository creates a knowledge base repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{db: pool, pool: pool}
}

// namespacePrefix labels the vector-store namespaces this table owns, so a
// namespace read out of the vector store is recognisable as a knowledge base's
// without a lookup.
const namespacePrefix = "kb_"

// Create inserts a knowledge base owned by params.UserID and returns the stored
// row. Nil settings are left to the column defaults rather than guessed here,
// so the schema stays the single source of truth for them. The row starts in
// the in_progress status; indexing advances it later.
//
// The vector-store namespace is assigned here rather than by the indexing
// pipeline, so a knowledge base has a stable namespace from the moment it
// exists. It is derived from the row id, which is why the id is generated in a
// CTE instead of being left to the column default: that makes the namespace
// unique for the same reason the id is, and lets either be traced back to the
// other. The CTE is MATERIALIZED so the uuid is generated once and both columns
// see the same value.
func (r *Repository) Create(ctx context.Context, params models.NewKnowledgeBase) (models.KnowledgeBase, error) {
	query := fmt.Sprintf(`
		WITH new_row AS MATERIALIZED (SELECT gen_random_uuid()::text AS id)
		INSERT INTO knowledge_bases (
			id, user_id, knowledge_base_name, pinecone_namespace,
			enable_auto_refresh, max_chunk_size, min_chunk_size
		)
		SELECT
			new_row.id, $1, $2, '%s' || replace(new_row.id, '-', ''),
			COALESCE($3, false), COALESCE($4, %d), COALESCE($5, %d)
		FROM new_row
		RETURNING %s
	`, namespacePrefix, models.KnowledgeBaseDefaultMaxChunkSize, models.KnowledgeBaseDefaultMinChunkSize, knowledgeBaseColumns)

	row, err := scanKnowledgeBase(r.db.QueryRow(
		ctx,
		query,
		params.UserID,
		params.Name,
		params.EnableAutoRefresh,
		params.MaxChunkSize,
		params.MinChunkSize,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.KnowledgeBase{}, ErrNameTaken
		}
		return models.KnowledgeBase{}, fmt.Errorf("create knowledge base: %w", err)
	}
	return row, nil
}

// ListByUser returns one page of the user's knowledge bases, newest first,
// together with the total number that user owns so the caller can build
// pagination metadata. limit and offset page the result; callers are expected to
// clamp them to sane bounds. The returned slice is always non-nil so an empty
// page serializes as [] rather than null.
func (r *Repository) ListByUser(ctx context.Context, userID string, limit, offset int) ([]models.KnowledgeBase, int, error) {
	var total int
	if err := r.db.QueryRow(ctx, "SELECT count(*) FROM knowledge_bases WHERE user_id = $1", userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count knowledge bases: %w", err)
	}

	out := make([]models.KnowledgeBase, 0)
	if total == 0 {
		return out, 0, nil
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM knowledge_bases
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, knowledgeBaseColumns)

	rows, err := r.db.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list knowledge bases: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		kb, err := scanKnowledgeBase(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan knowledge base: %w", err)
		}
		out = append(out, kb)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate knowledge bases: %w", err)
	}
	return out, total, nil
}

// GetByUser loads one knowledge base by id, scoped to the authenticated user.
// It returns ErrNotFound when no such knowledge base exists for that user, so a
// row owned by somebody else is indistinguishable from one that never existed.
func (r *Repository) GetByUser(ctx context.Context, userID, id string) (models.KnowledgeBase, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM knowledge_bases
		WHERE id = $1 AND user_id = $2
	`, knowledgeBaseColumns)

	kb, err := scanKnowledgeBase(r.db.QueryRow(ctx, query, id, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.KnowledgeBase{}, ErrNotFound
		}
		return models.KnowledgeBase{}, fmt.Errorf("get knowledge base: %w", err)
	}
	return kb, nil
}

// UpdateByUser applies a partial update to one knowledge base owned by the
// authenticated user and returns the stored row. Only the non-nil fields of
// params are written, so an untouched setting keeps its value. It returns
// ErrNotFound when no such knowledge base exists for that user, and ErrNameTaken
// when the new name collides with another of the owner's knowledge bases.
//
// The chunk sizes are guarded by a CHECK that min never exceeds max, which spans
// both columns: callers changing only one of them are expected to validate it
// against the stored counterpart first, so the constraint is a backstop rather
// than the primary check.
func (r *Repository) UpdateByUser(ctx context.Context, userID, id string, params models.KnowledgeBaseUpdate) (models.KnowledgeBase, error) {
	if params.IsEmpty() {
		return models.KnowledgeBase{}, fmt.Errorf("update knowledge base: no fields to update")
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
		addField("knowledge_base_name", *params.Name)
	}
	if params.EnableAutoRefresh != nil {
		addField("enable_auto_refresh", *params.EnableAutoRefresh)
	}
	if params.MaxChunkSize != nil {
		addField("max_chunk_size", *params.MaxChunkSize)
	}
	if params.MinChunkSize != nil {
		addField("min_chunk_size", *params.MinChunkSize)
	}

	query := fmt.Sprintf(`
		UPDATE knowledge_bases
		SET %s
		WHERE id = $1 AND user_id = $2
		RETURNING %s
	`, strings.Join(set, ", "), knowledgeBaseColumns)

	row, err := scanKnowledgeBase(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.KnowledgeBase{}, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.KnowledgeBase{}, ErrNameTaken
		}
		return models.KnowledgeBase{}, fmt.Errorf("update knowledge base: %w", err)
	}
	return row, nil
}

// DeleteByUser removes one knowledge base owned by the authenticated user. It
// returns ErrNotFound when no such knowledge base exists for that user, so
// deleting somebody else's reads the same as deleting one that never existed.
//
// The indexed sources live in the vector store rather than in Postgres, so
// dropping the row is all this does; purging the namespace follows the indexing
// pipeline.
func (r *Repository) DeleteByUser(ctx context.Context, userID, id string) error {
	const query = `DELETE FROM knowledge_bases WHERE id = $1 AND user_id = $2`
	tag, err := r.db.Exec(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("delete knowledge base: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanKnowledgeBase(row rowScanner) (models.KnowledgeBase, error) {
	var (
		kb            models.KnowledgeBase
		lastRefreshed *time.Time
	)
	if err := row.Scan(
		&kb.ID,
		&kb.UserID,
		&kb.Name,
		&kb.Status,
		&kb.NamespaceID,
		&kb.EnableAutoRefresh,
		&lastRefreshed,
		&kb.MaxChunkSize,
		&kb.MinChunkSize,
	); err != nil {
		return models.KnowledgeBase{}, err
	}
	kb.SetLastRefreshed(lastRefreshed)
	return kb, nil
}
