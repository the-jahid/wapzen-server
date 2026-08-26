-- +goose Up
-- +goose StatementBegin
-- The turn-by-turn transcript of a call: one row per message exchanged between
-- the AI agent (role 'assistant') and the caller (role 'user'), matching the
-- Role/Content conversation model used in internal/voicecall. Rows are
-- append-only, ordered within a call by seq, and deleted with the call.
CREATE TABLE IF NOT EXISTS call_messages (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
	-- Turn order within the call (0-based). created_at can collide between
	-- rapid turns, so ordering and de-duplication rely on seq instead.
	seq INTEGER NOT NULL CHECK (seq >= 0),
	role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
	content TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	-- One message per (call, turn). The backing index also serves lookups and
	-- ordering by call_id, since call_id is its leftmost column.
	UNIQUE (call_id, seq)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS call_messages;
-- +goose StatementEnd
