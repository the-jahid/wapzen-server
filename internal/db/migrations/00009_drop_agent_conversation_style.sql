-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
	DROP COLUMN IF EXISTS prompt_style_tone,
	DROP COLUMN IF EXISTS prompt_style_response_length,
	DROP COLUMN IF EXISTS prompt_style_empathy_level;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS prompt_style_tone TEXT DEFAULT 'warm and professional',
	ADD COLUMN IF NOT EXISTS prompt_style_response_length TEXT DEFAULT 'medium',
	ADD COLUMN IF NOT EXISTS prompt_style_empathy_level TEXT DEFAULT 'high';
-- +goose StatementEnd
