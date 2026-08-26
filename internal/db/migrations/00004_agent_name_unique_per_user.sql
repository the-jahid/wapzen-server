-- +goose Up
-- +goose StatementBegin
-- An agent name must be unique within a single owner's set of agents. Different
-- users may still reuse the same name, so the constraint is on (user_id,
-- agent_name) rather than agent_name alone.
ALTER TABLE agents
	ADD CONSTRAINT agents_user_id_agent_name_key UNIQUE (user_id, agent_name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_user_id_agent_name_key;
-- +goose StatementEnd
