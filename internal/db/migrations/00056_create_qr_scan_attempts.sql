-- +goose Up
-- +goose StatementBegin
-- One row per time a user was shown a QR code to link a WhatsApp number, and
-- how that attempt ended. activity_logs has the same events as separate lines;
-- this keeps each attempt as a single row so "how many times did this person
-- try, and how often did it fail?" is a count.
--
-- status starts as started and moves once to worked, failed or cancelled. A row
-- left at started means the attempt never reported an outcome (the user left
-- the page, or the server restarted mid-scan). phone_number is the number a
-- worked scan linked; failure_reason says why a failed one did not.
--
-- phone_number_id is the login's id (a draft id until the scan works), and
-- agent_id the agent whose editor started it, if any. Neither is a foreign key:
-- the draft never becomes a row when the scan fails, and the row should outlive
-- a deleted agent. email is copied from the user at the time.
CREATE TABLE IF NOT EXISTS qr_scan_attempts (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	phone_number_id TEXT,
	agent_id TEXT,
	status TEXT NOT NULL DEFAULT 'started'
		CHECK (status IN ('started', 'worked', 'failed', 'cancelled')),
	phone_number TEXT,
	failure_reason TEXT,
	started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_qr_scan_attempts_user_started
	ON qr_scan_attempts (user_id, started_at DESC);

-- Per-user totals: SELECT * FROM qr_scan_attempt_summary WHERE email = '...'.
CREATE OR REPLACE VIEW qr_scan_attempt_summary AS
SELECT
	user_id,
	max(email) AS email,
	count(*) AS attempts,
	count(*) FILTER (WHERE status = 'worked') AS worked,
	count(*) FILTER (WHERE status = 'failed') AS failed,
	count(*) FILTER (WHERE status = 'cancelled') AS cancelled,
	count(*) FILTER (WHERE status = 'started') AS no_outcome,
	min(started_at) AS first_attempt_at,
	max(started_at) AS last_attempt_at
FROM qr_scan_attempts
GROUP BY user_id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS qr_scan_attempt_summary;
DROP TABLE IF EXISTS qr_scan_attempts;
-- +goose StatementEnd
