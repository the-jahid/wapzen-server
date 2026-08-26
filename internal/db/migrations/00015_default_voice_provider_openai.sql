-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
	ALTER COLUMN voice_provider SET DEFAULT 'openai';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	ALTER COLUMN voice_provider SET DEFAULT '11labs';
-- +goose StatementEnd
