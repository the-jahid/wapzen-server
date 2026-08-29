-- +goose Up
-- +goose StatementBegin
-- Existing rows may predate the activation guard. Pause them before adding the
-- invariant so the migration is safe on a populated database.
UPDATE chat_agents
SET status = 'inactive', updated_at = now()
WHERE status = 'active' AND phone_number_id IS NULL;

ALTER TABLE chat_agents
	ADD CONSTRAINT chat_agents_active_requires_phone
	CHECK (status <> 'active' OR phone_number_id IS NOT NULL);

-- phone_numbers uses ON DELETE SET NULL for chat-agent assignments. Pause the
-- affected agent first so a device logout can clear that foreign key without
-- violating the active-agent invariant.
CREATE OR REPLACE FUNCTION pause_chat_agents_before_phone_number_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
	UPDATE chat_agents
	SET status = 'inactive', updated_at = now()
	WHERE phone_number_id = OLD.id AND status = 'active';
	RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS pause_chat_agents_before_phone_number_delete ON phone_numbers;
CREATE TRIGGER pause_chat_agents_before_phone_number_delete
	BEFORE DELETE ON phone_numbers
	FOR EACH ROW
	EXECUTE FUNCTION pause_chat_agents_before_phone_number_delete();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS pause_chat_agents_before_phone_number_delete ON phone_numbers;
DROP FUNCTION IF EXISTS pause_chat_agents_before_phone_number_delete();
ALTER TABLE chat_agents DROP CONSTRAINT IF EXISTS chat_agents_active_requires_phone;
-- +goose StatementEnd
