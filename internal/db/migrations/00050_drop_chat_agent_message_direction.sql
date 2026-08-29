-- +goose Up
-- +goose StatementBegin
-- A chat agent is only ever consulted for messages arriving on its number, so
-- the direction switch never selected anything: the API dropped the field and
-- inbound delivery now picks the active agent for the number regardless.
ALTER TABLE chat_agents DROP COLUMN IF EXISTS message_direction;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE chat_agents
	ADD COLUMN IF NOT EXISTS message_direction TEXT NOT NULL DEFAULT 'inbound'
		CHECK (message_direction IN ('inbound', 'outbound', 'both'));
-- +goose StatementEnd
