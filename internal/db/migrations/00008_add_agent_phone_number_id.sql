-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS phone_number_id TEXT;

DO $$
BEGIN
	ALTER TABLE agents
		ADD CONSTRAINT agents_phone_number_id_fkey
		FOREIGN KEY (phone_number_id)
		REFERENCES phone_numbers(id)
		ON DELETE SET NULL;
EXCEPTION
	WHEN duplicate_object THEN NULL;
END $$;

CREATE INDEX IF NOT EXISTS idx_agents_phone_number_id
	ON agents (phone_number_id)
	WHERE phone_number_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_agents_phone_number_direction
	ON agents (phone_number_id, call_direction)
	WHERE phone_number_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_agents_phone_number_direction;
DROP INDEX IF EXISTS idx_agents_phone_number_id;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_phone_number_id_fkey;
ALTER TABLE agents DROP COLUMN IF EXISTS phone_number_id;
-- +goose StatementEnd
