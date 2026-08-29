-- +goose Up
-- +goose StatementBegin
-- A chat agent is the text-message counterpart of a voice agent. It owns one
-- optional WhatsApp number and is consulted for every inbound message received
-- by that number. Keeping the switch and behaviour in Postgres means a running
-- WhatsApp session sees dashboard changes on the very next message.
CREATE TABLE IF NOT EXISTS chat_agents (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	agent_name TEXT NOT NULL,
	phone_number_id TEXT REFERENCES phone_numbers(id) ON DELETE SET NULL,
	language TEXT NOT NULL DEFAULT 'en',
	timezone TEXT,
	message_direction TEXT NOT NULL DEFAULT 'inbound'
		CHECK (message_direction IN ('inbound', 'outbound', 'both')),
	status TEXT NOT NULL DEFAULT 'inactive'
		CHECK (status IN ('active', 'inactive')),
	model_provider TEXT NOT NULL DEFAULT 'openai'
		CHECK (model_provider IN ('openai', 'anthropic')),
	model_name TEXT NOT NULL DEFAULT 'gpt-4.1-mini',
	model_temperature DOUBLE PRECISION NOT NULL DEFAULT 0.3
		CHECK (model_temperature BETWEEN 0.1 AND 1.0),
	prompt_begin_message_mode TEXT NOT NULL DEFAULT 'agent_waits_for_user'
		CHECK (prompt_begin_message_mode IN (
			'agent_sends_first',
			'agent_waits_for_user',
			'agent_sends_model_generated_message'
		)),
	prompt_begin_message TEXT DEFAULT '',
	prompt_system_prompt TEXT NOT NULL DEFAULT 'You are a helpful, friendly WhatsApp chat assistant.',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (user_id, agent_name),
	UNIQUE (phone_number_id)
);

CREATE INDEX IF NOT EXISTS idx_chat_agents_user_id ON chat_agents (user_id);

-- Knowledge bases and tools have one conversational owner. The two nullable
-- columns support either kind while the check prevents a resource from being
-- silently shared between a voice and chat agent.
ALTER TABLE knowledge_bases
	ADD COLUMN IF NOT EXISTS chat_agent_id TEXT REFERENCES chat_agents(id) ON DELETE CASCADE;
ALTER TABLE tools
	ADD COLUMN IF NOT EXISTS chat_agent_id TEXT REFERENCES chat_agents(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_knowledge_bases_chat_agent_id
	ON knowledge_bases (chat_agent_id) WHERE chat_agent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tools_chat_agent_id
	ON tools (chat_agent_id) WHERE chat_agent_id IS NOT NULL;

ALTER TABLE knowledge_bases
	ADD CONSTRAINT knowledge_bases_one_agent_owner
	CHECK (num_nonnulls(agent_id, chat_agent_id) <= 1);
ALTER TABLE tools
	ADD CONSTRAINT tools_one_agent_owner
	CHECK (num_nonnulls(agent_id, chat_agent_id) <= 1);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tools DROP CONSTRAINT IF EXISTS tools_one_agent_owner;
ALTER TABLE knowledge_bases DROP CONSTRAINT IF EXISTS knowledge_bases_one_agent_owner;
DROP INDEX IF EXISTS idx_tools_chat_agent_id;
DROP INDEX IF EXISTS idx_knowledge_bases_chat_agent_id;
ALTER TABLE tools DROP COLUMN IF EXISTS chat_agent_id;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS chat_agent_id;
DROP TABLE IF EXISTS chat_agents;
-- +goose StatementEnd
