-- +goose Up
-- +goose StatementBegin
-- A knowledge base groups the indexed sources an agent can retrieve from. Only
-- the container is stored here: the sources themselves (texts, scraped URLs,
-- fetched files) are indexed into the vector store under pinecone_namespace,
-- which stays NULL until indexing is wired up. The chunk-size bounds and the
-- name length mirror the documented Create Knowledge Base contract.
CREATE TABLE IF NOT EXISTS knowledge_bases (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	knowledge_base_name TEXT NOT NULL
		CHECK (char_length(knowledge_base_name) BETWEEN 1 AND 40),
	status TEXT NOT NULL DEFAULT 'in_progress'
		CHECK (status IN ('in_progress', 'complete', 'error', 'refreshing_in_progress')),
	-- Re-scrape the url sources every 12 hours. Only meaningful for url sources,
	-- so last_refreshed_at stays NULL until a refresh actually runs.
	enable_auto_refresh BOOLEAN NOT NULL DEFAULT false,
	last_refreshed_at TIMESTAMPTZ,
	max_chunk_size INTEGER NOT NULL DEFAULT 2000
		CHECK (max_chunk_size BETWEEN 600 AND 6000),
	min_chunk_size INTEGER NOT NULL DEFAULT 400
		CHECK (min_chunk_size BETWEEN 200 AND 2000),
	-- Vector-store namespace holding this knowledge base's chunks. Assigned when
	-- the sources are first indexed.
	pinecone_namespace TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	-- A chunk floor above the ceiling would merge every chunk into one.
	CHECK (min_chunk_size <= max_chunk_size)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_bases_user_id ON knowledge_bases (user_id);
-- Names are unique per owner, not globally: two users may both have a "Support"
-- knowledge base. This backs the 409 returned by Create.
CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_bases_user_name_unique
	ON knowledge_bases (user_id, knowledge_base_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_bases_pinecone_namespace_unique
	ON knowledge_bases (pinecone_namespace)
	WHERE pinecone_namespace IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS knowledge_bases;
-- +goose StatementEnd
