-- +goose Up
-- +goose StatementBegin
-- One indexed source of a knowledge base. Only the raw-text variant exists so
-- far; the url and document variants widen the type CHECK when their fetchers
-- land, which is why type is constrained rather than free-form.
--
-- The content is kept here as well as in the vector store because the vector
-- store is not readable as text: keeping it means a source can be re-chunked
-- after the knowledge base's chunk sizes change without asking the caller to
-- send it again. chunk_count is how many vectors the source produced; their ids
-- are derived from the title, the id and the chunk index, so the row carries
-- everything needed to address them again.
CREATE TABLE IF NOT EXISTS knowledge_base_sources (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	knowledge_base_id TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
	type TEXT NOT NULL DEFAULT 'text' CHECK (type IN ('text')),
	title TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
	content TEXT NOT NULL CHECK (char_length(content) > 0),
	chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sources are always read for one knowledge base at a time, oldest first, so
-- the ordering is part of the index.
CREATE INDEX IF NOT EXISTS idx_knowledge_base_sources_kb_id
	ON knowledge_base_sources (knowledge_base_id, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS knowledge_base_sources;
-- +goose StatementEnd
