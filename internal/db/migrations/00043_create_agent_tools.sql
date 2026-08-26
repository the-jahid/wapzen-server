-- +goose Up
-- +goose StatementBegin
-- Which tools an agent may call during a call. An agent can attach several (an
-- availability lookup and a transfer are two tools, not one) and a tool can
-- serve several agents, so the link is its own table — the same shape, and for
-- the same reasons, as agent_knowledge_bases.
--
-- Both foreign keys cascade: the row only means "this agent may call that
-- tool", which is meaningless once either end is gone.
--
-- Ownership is deliberately not expressed here. Both ends reference users(id),
-- but nothing in this table forces them to be the *same* user; the write path
-- checks that (see resolveToolAttachments), because the check needs the
-- authenticated user's id and a constraint cannot see it.
CREATE TABLE IF NOT EXISTS agent_tools (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	tool_id TEXT NOT NULL REFERENCES tools(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	-- One attachment per (agent, tool): attaching twice is the same state as
	-- attaching once, and the primary key also backs the per-agent read.
	PRIMARY KEY (agent_id, tool_id)
);

-- The primary key covers agent -> tools. This is the other direction: deleting
-- a tool has to find its attachments to cascade them, and "which agents use
-- this tool" is what a delete confirmation needs to answer.
CREATE INDEX IF NOT EXISTS idx_agent_tools_tool_id ON agent_tools (tool_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_tools;
-- +goose StatementEnd
