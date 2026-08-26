-- +goose Up
-- +goose StatementBegin
-- Calls will flow in both directions once the /v1/calls outbound API lands, so
-- each row records which kind it is. Everything logged so far was inbound,
-- hence the backfill default.
ALTER TABLE calls
	ADD COLUMN IF NOT EXISTS call_type TEXT NOT NULL DEFAULT 'inbound'
		CHECK (call_type IN ('inbound', 'outbound'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE calls
	DROP COLUMN IF EXISTS call_type;
-- +goose StatementEnd
