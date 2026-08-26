-- +goose Up
-- +goose StatementBegin
-- An outbound call the callee cut while it was still ringing never became a
-- conversation, so recording it as "ended" makes it indistinguishable from a call
-- that connected and ran its course. Give that outcome its own status.
ALTER TABLE calls DROP CONSTRAINT IF EXISTS calls_status_check;
ALTER TABLE calls ADD CONSTRAINT calls_status_check
	CHECK (status IN ('received', 'answered', 'ended', 'declined', 'failed'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Fold declined calls back into "ended" before the constraint refuses them.
UPDATE calls SET status = 'ended', updated_at = now() WHERE status = 'declined';

ALTER TABLE calls DROP CONSTRAINT IF EXISTS calls_status_check;
ALTER TABLE calls ADD CONSTRAINT calls_status_check
	CHECK (status IN ('received', 'answered', 'ended', 'failed'));
-- +goose StatementEnd
