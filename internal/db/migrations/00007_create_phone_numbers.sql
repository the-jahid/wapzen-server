-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS phone_numbers (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	phone_number TEXT,
	label TEXT,
	wa_jid TEXT,
	status TEXT NOT NULL DEFAULT 'pending_qr'
		CHECK (status IN ('pending_qr', 'connected', 'disconnected', 'failed', 'expired')),
	qr_code TEXT,
	last_connected_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK (phone_number IS NULL OR phone_number ~ '^\+[1-9][0-9]{1,14}$'),
	CHECK (label IS NULL OR char_length(label) <= 80)
);

CREATE INDEX IF NOT EXISTS idx_phone_numbers_user_id ON phone_numbers (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_phone_numbers_wa_jid_unique
	ON phone_numbers (wa_jid)
	WHERE wa_jid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_phone_numbers_user_phone_unique
	ON phone_numbers (user_id, phone_number)
	WHERE phone_number IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS phone_numbers;
-- +goose StatementEnd
