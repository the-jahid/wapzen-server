-- +goose Up
-- +goose StatementBegin
-- Migration 00015 changed the column default, but existing agents created
-- before that migration retained the legacy ElevenLabs provider value.
-- Normalize those legacy rows so the persisted default matches the product
-- default shown in the Voice & Live Audio editor.
UPDATE agents
SET voice_provider = 'openai'
WHERE voice_provider = '11labs';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Do not change existing provider selections on rollback.
-- +goose StatementEnd
