package knowledgebases

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrSourceTitleTaken is returned when a knowledge base already has a source
// with the requested title. Titles are the vector store's record ids, so a
// duplicate would overwrite the existing source's chunks rather than add to
// them.
var ErrSourceTitleTaken = errors.New("knowledge base source title already in use")

// ErrSourceNotFound is returned when a knowledge base has no source with the
// requested id. It is distinct from ErrNotFound so a caller can tell a missing
// source apart from a missing knowledge base.
var ErrSourceNotFound = errors.New("knowledge base source not found")

// knowledgeBaseSourceColumns is the projection every source query returns, in
// the order scanSource reads them. The content is deliberately not part of it:
// it is stored to be re-chunked, not to be listed, and is loaded explicitly by
// the one caller that needs it.
const knowledgeBaseSourceColumns = `id, type, title, chunk_count`

// InsertSources stores one source per entry and returns them in the order
// supplied, each with the id its vector ids are built from. The rows go in
// atomically, so a failure part-way leaves the knowledge base with none of them
// rather than a prefix.
//
// The type travels with the entry rather than being fixed here: a raw text and
// an uploaded file are stored and indexed identically — the file was extracted
// into text before it got here — and only the type says which one a source was.
//
// chunk_count starts at 0 and is set by SetSourceChunkCount once the source's
// vectors are actually in the vector store, so a source that was stored but
// never indexed is recognisable as one.
//
// It returns ErrSourceTitleTaken when a title collides with one the knowledge
// base already has, or with another title in the same call.
func (r *Repository) InsertSources(ctx context.Context, knowledgeBaseID string, entries []models.NewKnowledgeBaseSource) ([]models.KnowledgeBaseSourceWithContent, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	query := fmt.Sprintf(`
		INSERT INTO knowledge_base_sources (knowledge_base_id, type, title, content)
		VALUES ($1, $2, $3, $4)
		RETURNING %s
	`, knowledgeBaseSourceColumns)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("insert knowledge base sources: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	out := make([]models.KnowledgeBaseSourceWithContent, 0, len(entries))
	for _, entry := range entries {
		sourceType := entry.Type
		if sourceType == "" {
			sourceType = models.KnowledgeBaseSourceTypeText
		}
		source, err := scanSource(tx.QueryRow(ctx, query, knowledgeBaseID, sourceType, entry.Title, entry.Text))
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return nil, fmt.Errorf("%w: %s", ErrSourceTitleTaken, entry.Title)
			}
			return nil, fmt.Errorf("insert knowledge base source: %w", err)
		}
		out = append(out, models.KnowledgeBaseSourceWithContent{
			KnowledgeBaseSource: source,
			Content:             entry.Text,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("insert knowledge base sources: commit: %w", err)
	}
	return out, nil
}

// SetSourceChunkCount records how many vectors a source produced.
func (r *Repository) SetSourceChunkCount(ctx context.Context, sourceID string, chunkCount int) error {
	const query = `
		UPDATE knowledge_base_sources
		SET chunk_count = $2, updated_at = now()
		WHERE id = $1
	`
	if _, err := r.db.Exec(ctx, query, sourceID, chunkCount); err != nil {
		return fmt.Errorf("set source chunk count: %w", err)
	}
	return nil
}

// DeleteSources removes source rows by id. It is the rollback for an add that
// failed to index: the rows are dropped so a retry does not leave a duplicate
// source behind, and the caller purges the matching vectors separately.
func (r *Repository) DeleteSources(ctx context.Context, sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	const query = `DELETE FROM knowledge_base_sources WHERE id = ANY($1)`
	if _, err := r.db.Exec(ctx, query, sourceIDs); err != nil {
		return fmt.Errorf("delete knowledge base sources: %w", err)
	}
	return nil
}

// GetSource returns one source of a knowledge base. The lookup is scoped to the
// knowledge base, so a source id belonging to another one reads as missing
// rather than as somebody else's row.
//
// It returns the title and chunk count, which together are what the vector ids
// of the source's chunks are rebuilt from — deleting a source needs both.
func (r *Repository) GetSource(ctx context.Context, knowledgeBaseID, sourceID string) (models.KnowledgeBaseSource, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM knowledge_base_sources
		WHERE id = $1 AND knowledge_base_id = $2
	`, knowledgeBaseSourceColumns)

	source, err := scanSource(r.db.QueryRow(ctx, query, sourceID, knowledgeBaseID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.KnowledgeBaseSource{}, ErrSourceNotFound
		}
		return models.KnowledgeBaseSource{}, fmt.Errorf("get knowledge base source: %w", err)
	}
	return source, nil
}

// DeleteSource removes one source row, scoped to its knowledge base for the same
// reason GetSource is. It returns ErrSourceNotFound when the row is already
// gone, so a caller never reports a delete it did not make.
//
// The source's vectors are the caller's to remove: they live in the vector
// store, which this repository does not reach.
func (r *Repository) DeleteSource(ctx context.Context, knowledgeBaseID, sourceID string) error {
	const query = `DELETE FROM knowledge_base_sources WHERE id = $1 AND knowledge_base_id = $2`

	tag, err := r.db.Exec(ctx, query, sourceID, knowledgeBaseID)
	if err != nil {
		return fmt.Errorf("delete knowledge base source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSourceNotFound
	}
	return nil
}

// ListSources returns the sources of one knowledge base, oldest first.
func (r *Repository) ListSources(ctx context.Context, knowledgeBaseID string) ([]models.KnowledgeBaseSource, error) {
	byKnowledgeBase, err := r.SourcesByKnowledgeBase(ctx, []string{knowledgeBaseID})
	if err != nil {
		return nil, err
	}
	return byKnowledgeBase[knowledgeBaseID], nil
}

// SourcesByKnowledgeBase returns the sources of several knowledge bases at once,
// keyed by knowledge base id and oldest first within each. Listing a page of
// knowledge bases uses it to attach their sources in one query rather than one
// per row.
func (r *Repository) SourcesByKnowledgeBase(ctx context.Context, knowledgeBaseIDs []string) (map[string][]models.KnowledgeBaseSource, error) {
	out := make(map[string][]models.KnowledgeBaseSource, len(knowledgeBaseIDs))
	if len(knowledgeBaseIDs) == 0 {
		return out, nil
	}

	query := fmt.Sprintf(`
		SELECT knowledge_base_id, %s
		FROM knowledge_base_sources
		WHERE knowledge_base_id = ANY($1)
		ORDER BY created_at, id
	`, knowledgeBaseSourceColumns)

	rows, err := r.db.Query(ctx, query, knowledgeBaseIDs)
	if err != nil {
		return nil, fmt.Errorf("list knowledge base sources: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			knowledgeBaseID string
			source          models.KnowledgeBaseSource
		)
		if err := rows.Scan(
			&knowledgeBaseID,
			&source.SourceID,
			&source.Type,
			&source.Title,
			&source.ChunkCount,
		); err != nil {
			return nil, fmt.Errorf("scan knowledge base source: %w", err)
		}
		out[knowledgeBaseID] = append(out[knowledgeBaseID], source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge base sources: %w", err)
	}
	return out, nil
}

// SetStatus moves a knowledge base's indexing status. It is scoped to the owner
// like every other write, so a status can never be moved on somebody else's
// knowledge base.
func (r *Repository) SetStatus(ctx context.Context, userID, knowledgeBaseID, status string) error {
	const query = `
		UPDATE knowledge_bases
		SET status = $3, updated_at = now()
		WHERE id = $1 AND user_id = $2
	`
	tag, err := r.db.Exec(ctx, query, knowledgeBaseID, userID, status)
	if err != nil {
		return fmt.Errorf("set knowledge base status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSource(row rowScanner) (models.KnowledgeBaseSource, error) {
	var source models.KnowledgeBaseSource
	if err := row.Scan(&source.SourceID, &source.Type, &source.Title, &source.ChunkCount); err != nil {
		return models.KnowledgeBaseSource{}, err
	}
	return source, nil
}
