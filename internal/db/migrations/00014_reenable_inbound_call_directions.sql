-- +goose Up
-- +goose StatementBegin
-- Re-enable inbound (and both) call directions now that inbound voice calling is
-- supported server-side. Migration 00011 had narrowed this to outbound-only
-- while inbound was broken, but the dashboard has always offered all three, so
-- selecting Inbound/Both failed the check constraint. Default stays 'outbound'.
DO $$
BEGIN
	ALTER TABLE agents
		DROP CONSTRAINT IF EXISTS agents_call_direction_check;

	ALTER TABLE agents
		ADD CONSTRAINT agents_call_direction_check
		CHECK (call_direction IN ('inbound', 'outbound', 'both'));
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Revert to outbound-only, coercing any inbound/both rows back to outbound first
-- so the narrower constraint can be re-applied.
UPDATE agents
SET call_direction = 'outbound',
	updated_at = now()
WHERE call_direction <> 'outbound';

DO $$
BEGIN
	ALTER TABLE agents
		DROP CONSTRAINT IF EXISTS agents_call_direction_check;

	ALTER TABLE agents
		ADD CONSTRAINT agents_call_direction_check
		CHECK (call_direction IN ('outbound'));
END $$;
-- +goose StatementEnd
