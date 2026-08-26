-- +goose Up
-- +goose StatementBegin
-- Video calls are not answered by the agent, so whether the offer was video
-- carries no useful history; drop the flag.
ALTER TABLE voice_calls
	DROP COLUMN IF EXISTS is_video;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE voice_calls
	ADD COLUMN IF NOT EXISTS is_video BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd
