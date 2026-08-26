-- +goose Up
-- +goose StatementBegin
-- Record which agent handled the call. Nullable: a call can fail before any
-- agent is routed, and history must survive the agent being deleted later.
ALTER TABLE calls
	ADD COLUMN IF NOT EXISTS agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_calls_agent_id ON calls (agent_id)
	WHERE agent_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_calls_agent_id;
ALTER TABLE calls
	DROP COLUMN IF EXISTS agent_id;
-- +goose StatementEnd
