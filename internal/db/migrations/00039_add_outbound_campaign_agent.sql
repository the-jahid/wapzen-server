-- +goose Up
-- +goose StatementBegin
-- The agent a campaign dials with. Picking the agent is also what picks the
-- number: an agent already carries the phone_number_id it speaks on, so a
-- campaign that named a number as well could disagree with its own agent.
--
-- Nullable, because a draft is written before its agent is chosen and because a
-- campaign created before this column existed has none. The rule that a running
-- campaign must have one is enforced by the handler, which is also where the
-- agent's owner is checked -- a constraint cannot see the authenticated user,
-- and nothing here forces the agent and the campaign to share one.
--
-- ON DELETE SET NULL keeps the campaign and its counters when the agent is
-- deleted, the way calls.agent_id does; the campaign then reads as having no
-- agent and cannot be put back on the air until it is given another one.
ALTER TABLE outbound_campaigns
	ADD COLUMN IF NOT EXISTS agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_outbound_campaigns_agent_id
	ON outbound_campaigns (agent_id)
	WHERE agent_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_outbound_campaigns_agent_id;

ALTER TABLE outbound_campaigns
	DROP COLUMN IF EXISTS agent_id;
-- +goose StatementEnd
