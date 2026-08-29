-- +goose Up
-- +goose StatementBegin
-- A QR login no longer writes a phone_numbers row when it starts: an offered QR
-- code is not a phone number, and the row is created only once a scan pairs a
-- device. Rows left over from when every offered code inserted one — nothing
-- ever paired to them, no number, never connected — are that old behaviour's
-- litter, and there is nothing to keep in them. Every foreign key that can
-- reference a phone number is ON DELETE SET NULL, and an unpaired row is not
-- assignable in the first place, so this detaches nothing real.
DELETE FROM phone_numbers
WHERE status <> 'connected'
	AND wa_jid IS NULL
	AND phone_number IS NULL
	AND last_connected_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Deleted placeholders cannot be restored, and recreating empty rows would only
-- put the litter back.
SELECT 1;
-- +goose StatementEnd
