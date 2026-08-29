-- +goose Up
-- +goose StatementBegin
-- The chat agent's reply language now comes from its system prompt alone, and
-- nothing in the message path ever read the timezone, so both settings left the
-- dashboard and the API contract.
ALTER TABLE chat_agents DROP COLUMN IF EXISTS language;
ALTER TABLE chat_agents DROP COLUMN IF EXISTS timezone;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE chat_agents ADD COLUMN IF NOT EXISTS language TEXT NOT NULL DEFAULT 'en';
ALTER TABLE chat_agents ADD COLUMN IF NOT EXISTS timezone TEXT;
-- +goose StatementEnd
