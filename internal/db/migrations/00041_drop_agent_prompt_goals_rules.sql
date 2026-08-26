-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
	DROP COLUMN IF EXISTS prompt_goals,
	DROP COLUMN IF EXISTS prompt_rules;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS prompt_goals TEXT[] DEFAULT ARRAY[]::TEXT[],
	ADD COLUMN IF NOT EXISTS prompt_rules TEXT[] DEFAULT ARRAY[]::TEXT[];
-- +goose StatementEnd
