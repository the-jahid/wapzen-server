-- +goose Up
-- +goose StatementBegin
-- A source's title is its record id in the vector store, so two sources of one
-- knowledge base sharing a title would write over each other's vectors — the
-- second source would silently take the first one's chunks. Titles are
-- therefore unique per knowledge base, and adding a duplicate is a 409.
--
-- Uniqueness is case-sensitive because vector-store ids are: "Refund policy"
-- and "refund policy" are two different records, so they are two allowed titles.

-- Existing duplicates are numbered rather than dropped: they hold indexed text,
-- and losing it to a schema change would be worse than an altered title. The
-- title is truncated first so the suffix cannot push it past its length CHECK.
WITH ranked AS (
	SELECT
		id,
		row_number() OVER (
			PARTITION BY knowledge_base_id, title
			ORDER BY created_at, id
		) AS position
	FROM knowledge_base_sources
)
UPDATE knowledge_base_sources AS s
SET title = left(s.title, 190) || ' (' || ranked.position || ')'
FROM ranked
WHERE s.id = ranked.id AND ranked.position > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_base_sources_title_unique
	ON knowledge_base_sources (knowledge_base_id, title);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_knowledge_base_sources_title_unique;
-- +goose StatementEnd
