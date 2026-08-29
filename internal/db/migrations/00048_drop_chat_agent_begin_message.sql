-- +goose Up
-- +goose StatementBegin
-- A chat agent never opens a WhatsApp thread on its own: the conversation
-- starts when a person writes in. The begin-message mode and its canned text
-- were carried over from the voice contract and had no reader, so they are
-- dropped rather than left as dead configuration in the dashboard.
ALTER TABLE chat_agents DROP COLUMN IF EXISTS prompt_begin_message_mode;
ALTER TABLE chat_agents DROP COLUMN IF EXISTS prompt_begin_message;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE chat_agents
	ADD COLUMN IF NOT EXISTS prompt_begin_message_mode TEXT NOT NULL DEFAULT 'agent_waits_for_user'
		CHECK (prompt_begin_message_mode IN (
			'agent_sends_first',
			'agent_waits_for_user',
			'agent_sends_model_generated_message'
		));
ALTER TABLE chat_agents ADD COLUMN IF NOT EXISTS prompt_begin_message TEXT DEFAULT '';
-- +goose StatementEnd
