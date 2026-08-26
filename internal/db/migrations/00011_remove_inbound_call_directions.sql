-- +goose Up
-- +goose StatementBegin
UPDATE agents
SET call_direction = 'outbound',
	updated_at = now()
WHERE call_direction <> 'outbound';

DO $$
BEGIN
	ALTER TABLE agents
		DROP CONSTRAINT IF EXISTS agents_call_direction_check;

	ALTER TABLE agents
		ALTER COLUMN call_direction SET DEFAULT 'outbound';

	ALTER TABLE agents
		ADD CONSTRAINT agents_call_direction_check
		CHECK (call_direction IN ('outbound'));
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
	ALTER TABLE agents
		DROP CONSTRAINT IF EXISTS agents_call_direction_check;

	ALTER TABLE agents
		ALTER COLUMN call_direction SET DEFAULT 'outbound';

	ALTER TABLE agents
		ADD CONSTRAINT agents_call_direction_check
		CHECK (call_direction IN ('outbound'));
END $$;
-- +goose StatementEnd
