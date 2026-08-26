-- +goose Up
-- +goose StatementBegin
-- The public API surface is /v1/calls, so the table follows: voice_calls →
-- calls. Indexes and constraints are renamed too so nothing keeps the stale
-- voice_calls_ prefix.
ALTER TABLE voice_calls RENAME TO calls;

ALTER INDEX idx_voice_calls_user_id RENAME TO idx_calls_user_id;
ALTER INDEX idx_voice_calls_phone_number_id RENAME TO idx_calls_phone_number_id;
ALTER INDEX idx_voice_calls_call_id RENAME TO idx_calls_call_id;

ALTER TABLE calls RENAME CONSTRAINT voice_calls_pkey TO calls_pkey;
ALTER TABLE calls RENAME CONSTRAINT voice_calls_user_id_fkey TO calls_user_id_fkey;
ALTER TABLE calls RENAME CONSTRAINT voice_calls_phone_number_id_fkey TO calls_phone_number_id_fkey;
ALTER TABLE calls RENAME CONSTRAINT voice_calls_status_check TO calls_status_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE calls RENAME CONSTRAINT calls_status_check TO voice_calls_status_check;
ALTER TABLE calls RENAME CONSTRAINT calls_phone_number_id_fkey TO voice_calls_phone_number_id_fkey;
ALTER TABLE calls RENAME CONSTRAINT calls_user_id_fkey TO voice_calls_user_id_fkey;
ALTER TABLE calls RENAME CONSTRAINT calls_pkey TO voice_calls_pkey;

ALTER INDEX idx_calls_call_id RENAME TO idx_voice_calls_call_id;
ALTER INDEX idx_calls_phone_number_id RENAME TO idx_voice_calls_phone_number_id;
ALTER INDEX idx_calls_user_id RENAME TO idx_voice_calls_user_id;

ALTER TABLE calls RENAME TO voice_calls;
-- +goose StatementEnd
