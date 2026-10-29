-- +goose Up
-- +goose StatementBegin
-- An operator can block an account. A blocked user's requests are refused and
-- their agents stop answering messages and calls; the rows stay, so unblocking
-- (setting this back to NULL) restores everything as it was.
ALTER TABLE users ADD COLUMN IF NOT EXISTS blocked_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS blocked_at;
-- +goose StatementEnd
