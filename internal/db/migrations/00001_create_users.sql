-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	email TEXT NOT NULL UNIQUE,
	oauth_id TEXT NOT NULL UNIQUE,
	username TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
