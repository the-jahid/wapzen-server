-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS voice_calls (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	call_id TEXT NOT NULL,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	-- Keep call history when its phone number is later deleted.
	phone_number_id TEXT REFERENCES phone_numbers(id) ON DELETE SET NULL,
	peer TEXT NOT NULL,
	is_video BOOLEAN NOT NULL DEFAULT false,
	status TEXT NOT NULL DEFAULT 'received'
		CHECK (status IN ('received', 'answered', 'ended', 'failed')),
	end_reason TEXT,
	answered_at TIMESTAMPTZ,
	ended_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_voice_calls_user_id ON voice_calls (user_id);
CREATE INDEX IF NOT EXISTS idx_voice_calls_phone_number_id ON voice_calls (phone_number_id);
CREATE INDEX IF NOT EXISTS idx_voice_calls_call_id ON voice_calls (call_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS voice_calls;
-- +goose StatementEnd
