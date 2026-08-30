-- +goose Up
-- +goose StatementBegin
-- A tool is now shared: one definition can be attached to any number of agents,
-- voice and chat alike. The owner columns added in 00045 and 00047 could only
-- record one agent, which made attaching a tool a second time a conflict — and
-- a tool is a definition, not a possession: the same "look up a booking" call is
-- exactly what several agents want.
--
-- One join table per agent kind rather than one table with two nullable keys, so
-- each foreign key is NOT NULL and the primary key is the attachment itself.
CREATE TABLE IF NOT EXISTS agent_tools (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	tool_id TEXT NOT NULL REFERENCES tools(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (agent_id, tool_id)
);

CREATE TABLE IF NOT EXISTS chat_agent_tools (
	chat_agent_id TEXT NOT NULL REFERENCES chat_agents(id) ON DELETE CASCADE,
	tool_id TEXT NOT NULL REFERENCES tools(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (chat_agent_id, tool_id)
);

-- The primary keys serve the agent's own lookup; these serve the other
-- direction, which is what a tool's resource reads to say who uses it and what
-- the delete path walks.
CREATE INDEX IF NOT EXISTS idx_agent_tools_tool_id ON agent_tools (tool_id);
CREATE INDEX IF NOT EXISTS idx_chat_agent_tools_tool_id ON chat_agent_tools (tool_id);

-- Carry the existing attachments over. The join row is dated from the tool it
-- attaches, so the order an agent reads its tools back in does not change.
INSERT INTO agent_tools (agent_id, tool_id, created_at)
SELECT agent_id, id, created_at FROM tools WHERE agent_id IS NOT NULL
ON CONFLICT (agent_id, tool_id) DO NOTHING;

INSERT INTO chat_agent_tools (chat_agent_id, tool_id, created_at)
SELECT chat_agent_id, id, created_at FROM tools WHERE chat_agent_id IS NOT NULL
ON CONFLICT (chat_agent_id, tool_id) DO NOTHING;

-- The check constraint exists to stop a row being owned by a voice and a chat
-- agent at once. Sharing is the point now, and knowledge_bases keeps its own
-- constraint, so only the tools half goes.
ALTER TABLE tools DROP CONSTRAINT IF EXISTS tools_one_agent_owner;
DROP INDEX IF EXISTS idx_tools_agent_id;
DROP INDEX IF EXISTS idx_tools_chat_agent_id;
ALTER TABLE tools DROP COLUMN IF EXISTS agent_id;
ALTER TABLE tools DROP COLUMN IF EXISTS chat_agent_id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tools
	ADD COLUMN IF NOT EXISTS agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE tools
	ADD COLUMN IF NOT EXISTS chat_agent_id TEXT REFERENCES chat_agents(id) ON DELETE CASCADE;

-- A tool attached to several agents can only keep one under the old shape, so
-- the oldest attachment wins and a voice agent outranks a chat agent, which is
-- what the restored check constraint requires. The attachments this drops are
-- gone for good: the rollback restores ownership, not history.
UPDATE tools t
SET agent_id = first_attachment.agent_id
FROM (
	SELECT DISTINCT ON (tool_id) tool_id, agent_id
	FROM agent_tools
	ORDER BY tool_id, created_at, agent_id
) AS first_attachment
WHERE t.id = first_attachment.tool_id;

UPDATE tools t
SET chat_agent_id = first_attachment.chat_agent_id
FROM (
	SELECT DISTINCT ON (tool_id) tool_id, chat_agent_id
	FROM chat_agent_tools
	ORDER BY tool_id, created_at, chat_agent_id
) AS first_attachment
WHERE t.id = first_attachment.tool_id
	AND t.agent_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_tools_agent_id
	ON tools (agent_id) WHERE agent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tools_chat_agent_id
	ON tools (chat_agent_id) WHERE chat_agent_id IS NOT NULL;

ALTER TABLE tools
	ADD CONSTRAINT tools_one_agent_owner
	CHECK (num_nonnulls(agent_id, chat_agent_id) <= 1);

DROP TABLE IF EXISTS chat_agent_tools;
DROP TABLE IF EXISTS agent_tools;
-- +goose StatementEnd
