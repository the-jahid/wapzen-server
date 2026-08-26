-- +goose Up
-- +goose StatementBegin
-- Uploaded files are now indexed as sources. The server extracts their text on
-- upload, so a file source is stored exactly like a raw text one — same content
-- column, same chunking, same vectors — and the type is what records that the
-- text came out of a document rather than out of the request body.
--
-- Keeping the distinction is what lets a listing show a source for what it is,
-- and what will let a re-index know a file's text is a derived copy the original
-- upload can no longer be recovered from.
ALTER TABLE knowledge_base_sources DROP CONSTRAINT IF EXISTS knowledge_base_sources_type_check;

ALTER TABLE knowledge_base_sources
	ADD CONSTRAINT knowledge_base_sources_type_check
	CHECK (type IN ('text', 'file'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- File sources become plain text sources rather than being deleted: their
-- extracted content is indexed and still perfectly usable, and dropping the rows
-- would orphan the vectors those sources wrote.
UPDATE knowledge_base_sources
SET type = 'text', updated_at = now()
WHERE type <> 'text';

ALTER TABLE knowledge_base_sources DROP CONSTRAINT IF EXISTS knowledge_base_sources_type_check;

ALTER TABLE knowledge_base_sources
	ADD CONSTRAINT knowledge_base_sources_type_check
	CHECK (type IN ('text'));
-- +goose StatementEnd
