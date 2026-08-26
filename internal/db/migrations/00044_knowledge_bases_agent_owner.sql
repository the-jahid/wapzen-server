-- +goose Up
-- +goose StatementBegin
-- A knowledge base now belongs to one agent, recorded on the knowledge base
-- itself, replacing the agent_knowledge_bases join table.
--
-- The join table modelled a knowledge base serving several agents. That never
-- happened in practice and it made the base's lifetime ambiguous: nothing owned
-- it, so deleting the agent that used it left the base and its vector namespace
-- behind with no way to reach them from the dashboard. A direct column makes the
-- agent the owner, which is what ON DELETE CASCADE below then means.
--
-- Nullable, because a knowledge base exists before any agent uses it: it is
-- created, indexed, and only then attached.
ALTER TABLE knowledge_bases
	ADD COLUMN IF NOT EXISTS agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE;

-- Carry the existing attachments over. A base attached to several agents can
-- only keep one under the new shape, so the oldest attachment wins — that is the
-- agent it was built for; the later ones borrowed it. Bases attached to nothing
-- stay NULL.
DO $$
BEGIN
	-- A database created directly from the latest schema already has the direct
	-- column and never had the legacy join table. In that case there is nothing
	-- to backfill.
	IF to_regclass('agent_knowledge_bases') IS NOT NULL THEN
		UPDATE knowledge_bases kb
		SET agent_id = first_attachment.agent_id,
			updated_at = now()
		FROM (
			SELECT DISTINCT ON (knowledge_base_id) knowledge_base_id, agent_id
			FROM agent_knowledge_bases
			ORDER BY knowledge_base_id, created_at, agent_id
		) AS first_attachment
		WHERE kb.id = first_attachment.knowledge_base_id
			AND kb.agent_id IS NULL;
	END IF;
END $$;

-- Backs both directions the server reads: an agent's bases (the resource's
-- knowledge_base_ids, and the namespaces a live call retrieves from) and the
-- cascade's lookup when an agent is deleted.
CREATE INDEX IF NOT EXISTS idx_knowledge_bases_agent_id
	ON knowledge_bases (agent_id)
	WHERE agent_id IS NOT NULL;

DROP TABLE IF EXISTS agent_knowledge_bases;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS agent_knowledge_bases (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	knowledge_base_id TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (agent_id, knowledge_base_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_knowledge_bases_knowledge_base_id
	ON agent_knowledge_bases (knowledge_base_id);

-- One row per owned base. The attachments this migration collapsed on the way up
-- are gone for good; the rollback restores the ownership that survived, not the
-- history.
INSERT INTO agent_knowledge_bases (agent_id, knowledge_base_id)
SELECT agent_id, id FROM knowledge_bases WHERE agent_id IS NOT NULL
ON CONFLICT (agent_id, knowledge_base_id) DO NOTHING;

DROP INDEX IF EXISTS idx_knowledge_bases_agent_id;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS agent_id;
-- +goose StatementEnd
