-- +goose Up
-- +goose StatementBegin
-- Every knowledge base now gets its vector-store namespace when it is created
-- rather than when its sources are first indexed, so the namespace is a stable
-- handle callers can see and reference from the moment the base exists. The
-- namespace is derived from the row id ('kb_' + the id without dashes), which
-- keeps it unique for free and makes a namespace traceable back to its base.
--
-- This backfills the rows created before that, which were left NULL. Rows whose
-- namespace was already assigned are untouched.
UPDATE knowledge_bases
SET pinecone_namespace = 'kb_' || replace(id, '-', ''),
	updated_at = now()
WHERE pinecone_namespace IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Only clears the namespaces this migration derived, so one assigned by any
-- other path survives the rollback.
UPDATE knowledge_bases
SET pinecone_namespace = NULL,
	updated_at = now()
WHERE pinecone_namespace = 'kb_' || replace(id, '-', '');
-- +goose StatementEnd
