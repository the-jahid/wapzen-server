-- +goose Up
-- +goose StatementBegin
-- Which knowledge bases an agent may answer from. An agent can attach several
-- (a product handbook and an opening-hours note are two bases, not one), and a
-- knowledge base can serve several agents, so the link is its own table rather
-- than a column on either side.
--
-- Both foreign keys cascade: the link only means "this agent quotes that base",
-- so it is meaningless once either end is gone, and leaving the row behind would
-- let a recycled id resurrect an attachment nobody asked for.
--
-- Ownership is deliberately not expressed here. Both ends already reference
-- users(id), but nothing in this table forces them to be the *same* user; the
-- write path checks that (see resolveKnowledgeBaseAttachments), the way the
-- phone-number assignment does, because the check needs the authenticated
-- user's id and a constraint cannot see it.
CREATE TABLE IF NOT EXISTS agent_knowledge_bases (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	knowledge_base_id TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	-- One attachment per (agent, knowledge base): attaching twice is the same
	-- state as attaching once, and the primary key also backs the per-agent read.
	PRIMARY KEY (agent_id, knowledge_base_id)
);

-- The primary key covers agent -> bases. This is the other direction: deleting a
-- knowledge base has to find its attachments to cascade them, and "which agents
-- use this base" is what a delete confirmation needs to answer.
CREATE INDEX IF NOT EXISTS idx_agent_knowledge_bases_knowledge_base_id
	ON agent_knowledge_bases (knowledge_base_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_knowledge_bases;
-- +goose StatementEnd
