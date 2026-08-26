-- +goose Up
-- +goose StatementBegin
-- A tool now belongs to one agent, recorded on the tool itself, replacing the
-- agent_tools join table — the same shape, and for the same reasons, as
-- knowledge_bases.agent_id in the previous migration.
--
-- Nullable, because a tool exists before any agent calls it: it is defined,
-- configured, and only then attached.
ALTER TABLE tools
	ADD COLUMN IF NOT EXISTS agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE;

-- Carry the existing attachments over. A tool attached to several agents can
-- only keep one under the new shape, so the oldest attachment wins. Tools
-- attached to nothing stay NULL.
DO $$
BEGIN
	-- A database created directly from the latest schema already has the direct
	-- column and never had the legacy join table. In that case there is nothing
	-- to backfill.
	IF to_regclass('agent_tools') IS NOT NULL THEN
		UPDATE tools t
		SET agent_id = first_attachment.agent_id,
			updated_at = now()
		FROM (
			SELECT DISTINCT ON (tool_id) tool_id, agent_id
			FROM agent_tools
			ORDER BY tool_id, created_at, agent_id
		) AS first_attachment
		WHERE t.id = first_attachment.tool_id
			AND t.agent_id IS NULL;
	END IF;
END $$;

-- Backs both directions the server reads: an agent's tools (the resource's
-- tool_ids, and the definitions a live call declares to the model) and the
-- cascade's lookup when an agent is deleted.
CREATE INDEX IF NOT EXISTS idx_tools_agent_id
	ON tools (agent_id)
	WHERE agent_id IS NOT NULL;

DROP TABLE IF EXISTS agent_tools;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS agent_tools (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	tool_id TEXT NOT NULL REFERENCES tools(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (agent_id, tool_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_tools_tool_id ON agent_tools (tool_id);

-- One row per owned tool. The attachments this migration collapsed on the way up
-- are gone for good; the rollback restores the ownership that survived, not the
-- history.
INSERT INTO agent_tools (agent_id, tool_id)
SELECT agent_id, id FROM tools WHERE agent_id IS NOT NULL
ON CONFLICT (agent_id, tool_id) DO NOTHING;

DROP INDEX IF EXISTS idx_tools_agent_id;
ALTER TABLE tools DROP COLUMN IF EXISTS agent_id;
-- +goose StatementEnd
