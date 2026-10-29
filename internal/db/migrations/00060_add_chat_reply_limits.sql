-- +goose Up
-- +goose StatementBegin
-- Chat agents answer a capped number of WhatsApp messages over their lifetime,
-- counted twice: per agent, and per WhatsApp number. Both are counters of
-- replies actually sent rather than counts over chat_messages, because a
-- conversation can be deleted (which would hand the quota back) and the
-- dashboard's own human-written messages are saved as assistant turns too.
ALTER TABLE chat_agents ADD COLUMN IF NOT EXISTS ai_reply_count INTEGER NOT NULL DEFAULT 0
	CHECK (ai_reply_count >= 0);

-- The per-number count is keyed on the phone number itself (E.164), not the
-- phone_numbers row: unlinking and relinking a number creates a new row, and
-- that must not start the count again. A row without a known number falls back
-- to its id.
CREATE TABLE IF NOT EXISTS chat_reply_usage (
	phone_key TEXT PRIMARY KEY,
	ai_reply_count INTEGER NOT NULL DEFAULT 0 CHECK (ai_reply_count >= 0),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS chat_reply_usage;
ALTER TABLE chat_agents DROP COLUMN IF EXISTS ai_reply_count;
-- +goose StatementEnd
