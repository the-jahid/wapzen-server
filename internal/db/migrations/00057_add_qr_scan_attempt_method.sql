-- +goose Up
-- +goose StatementBegin
-- method is how the user was asked to link the number: 'qr' (scan the code) or
-- 'phone_code' (type an 8-character pairing code into WhatsApp, for users who
-- have the dashboard open on the phone they are linking). user_agent is the
-- browser that asked, so an attempt that expired unscanned shows whether the
-- user was on a phone or a computer.
ALTER TABLE qr_scan_attempts
	ADD COLUMN IF NOT EXISTS method TEXT NOT NULL DEFAULT 'qr'
		CHECK (method IN ('qr', 'phone_code')),
	ADD COLUMN IF NOT EXISTS user_agent TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE qr_scan_attempts
	DROP COLUMN IF EXISTS user_agent,
	DROP COLUMN IF EXISTS method;
-- +goose StatementEnd
