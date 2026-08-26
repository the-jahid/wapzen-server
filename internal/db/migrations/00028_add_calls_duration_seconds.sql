-- +goose Up
-- +goose StatementBegin
-- How long the call was actually connected, in whole seconds (answered_at →
-- ended_at). Null while a call is still live or when it was never answered, so
-- "no duration yet" is distinguishable from a real zero.
ALTER TABLE calls
	ADD COLUMN IF NOT EXISTS duration_seconds INTEGER
		CHECK (duration_seconds IS NULL OR duration_seconds >= 0);

-- Backfill calls that already ended before this column existed.
UPDATE calls
SET duration_seconds = GREATEST(0, EXTRACT(EPOCH FROM (ended_at - answered_at))::int)
WHERE duration_seconds IS NULL
	AND answered_at IS NOT NULL
	AND ended_at IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE calls
	DROP COLUMN IF EXISTS duration_seconds;
-- +goose StatementEnd
