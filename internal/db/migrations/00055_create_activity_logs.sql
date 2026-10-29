-- +goose Up
-- +goose StatementBegin
-- What each user did, as the application log reports it: when they signed in,
-- and every time they were shown a QR code to link a WhatsApp number and how
-- that scan ended. The log itself scrolls away on Render; this keeps the same
-- lines per user so "did this person ever manage to connect?" is one query.
--
-- event is one of signed_in, qr_scan_started, qr_scan_worked, qr_scan_failed,
-- qr_scan_cancelled. It is not a CHECK constraint so a new kind of event needs
-- no migration. detail is the event's one variable part: the failure reason,
-- or the number a successful scan linked.
--
-- email is copied from the user at the time, so the table reads on its own.
-- user_id cascades: a deleted account takes its activity with it.
CREATE TABLE IF NOT EXISTS activity_logs (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	event TEXT NOT NULL CHECK (char_length(event) > 0),
	message TEXT NOT NULL,
	detail TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_activity_logs_user_created
	ON activity_logs (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_logs_event_created
	ON activity_logs (event, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS activity_logs;
-- +goose StatementEnd
