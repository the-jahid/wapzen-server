-- +goose Up
-- +goose StatementBegin
-- Every agent is paused while the next update is built (2026-10-30). The
-- dashboard stays usable and numbers can still be linked, but no voice or chat
-- agent may go live: each insert or update has its status forced to inactive,
-- whichever path set it (a QR scan assigning its number, the go-live-on-number
-- rule, an explicit status from the dashboard or the API). The live-agent
-- lookups only pick status = 'active', so nothing replies or answers calls.
-- Undo by migrating down past this file.
CREATE OR REPLACE FUNCTION force_agent_paused() RETURNS trigger AS $$
BEGIN
	NEW.status := 'inactive';
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS agents_force_paused ON agents;
CREATE TRIGGER agents_force_paused
	BEFORE INSERT OR UPDATE ON agents
	FOR EACH ROW EXECUTE FUNCTION force_agent_paused();

DROP TRIGGER IF EXISTS chat_agents_force_paused ON chat_agents;
CREATE TRIGGER chat_agents_force_paused
	BEFORE INSERT OR UPDATE ON chat_agents
	FOR EACH ROW EXECUTE FUNCTION force_agent_paused();

UPDATE agents SET status = 'inactive' WHERE status <> 'inactive';
UPDATE chat_agents SET status = 'inactive' WHERE status <> 'inactive';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS agents_force_paused ON agents;
DROP TRIGGER IF EXISTS chat_agents_force_paused ON chat_agents;
DROP FUNCTION IF EXISTS force_agent_paused();
-- +goose StatementEnd
