-- +goose Up
-- +goose StatementBegin
-- Chat agents answer with one model only, Claude Sonnet 4.6. Existing agents are
-- moved onto it and the column defaults follow, so a row written outside the
-- API cannot pick up the old OpenAI default.
UPDATE chat_agents
SET model_provider = 'anthropic', model_name = 'claude-sonnet-4-6'
WHERE model_provider <> 'anthropic' OR model_name <> 'claude-sonnet-4-6';

ALTER TABLE chat_agents
	ALTER COLUMN model_provider SET DEFAULT 'anthropic',
	ALTER COLUMN model_name SET DEFAULT 'claude-sonnet-4-6';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The models agents used before cannot be restored; only the defaults return.
ALTER TABLE chat_agents
	ALTER COLUMN model_provider SET DEFAULT 'openai',
	ALTER COLUMN model_name SET DEFAULT 'gpt-4.1-mini';
-- +goose StatementEnd
