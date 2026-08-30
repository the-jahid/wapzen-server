-- +goose Up
-- +goose StatementBegin
-- The saved history of a WhatsApp text thread. Until now a chat agent's replies
-- existed only in the process that wrote them: the manager kept the last turns
-- in memory to give the model context, and a restart erased them. Nothing could
-- be read back, so the dashboard had no way to show what an agent had said.
--
-- A thread is keyed on (phone_number_id, peer_jid) rather than on the agent:
-- swapping which agent answers a number is a change of staff, not the start of a
-- new conversation with that contact. chat_agent_id records who answered last so
-- the thread can still be listed per agent.
--
-- phone_number_id and chat_agent_id are ON DELETE SET NULL, the way calls treats
-- its number and agent: unpairing a device or deleting an agent must not delete
-- the record of what was said. user_id is the owner and cascades, so a thread is
-- always reachable from the account that owns it.
CREATE TABLE IF NOT EXISTS chat_conversations (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	phone_number_id TEXT REFERENCES phone_numbers(id) ON DELETE SET NULL,
	chat_agent_id TEXT REFERENCES chat_agents(id) ON DELETE SET NULL,
	-- The contact's WhatsApp JID as the message arrived on the wire.
	peer_jid TEXT NOT NULL CHECK (char_length(peer_jid) > 0),
	-- The bare number when the JID carries one. An "@lid" peer has no phone in
	-- it, so this stays NULL rather than storing the opaque id twice.
	peer_phone TEXT,
	peer_name TEXT CHECK (peer_name IS NULL OR char_length(peer_name) <= 120),
	status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
	-- Maintained by the trigger below so a listing can order and summarize
	-- threads without counting their messages.
	message_count INTEGER NOT NULL DEFAULT 0 CHECK (message_count >= 0),
	last_message_role TEXT CHECK (last_message_role IS NULL OR last_message_role IN ('user', 'assistant')),
	last_message_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One thread per number and contact. Partial, because phone_number_id is
-- nullable: a thread whose number was deleted keeps its history but can never be
-- written to again, so it no longer competes for the key.
CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_conversations_number_peer_unique
	ON chat_conversations (phone_number_id, peer_jid)
	WHERE phone_number_id IS NOT NULL;
-- The inbox reads one account's threads, most recently active first.
CREATE INDEX IF NOT EXISTS idx_chat_conversations_user_recent
	ON chat_conversations (user_id, last_message_at DESC);
CREATE INDEX IF NOT EXISTS idx_chat_conversations_chat_agent_id
	ON chat_conversations (chat_agent_id)
	WHERE chat_agent_id IS NOT NULL;

-- The turn-by-turn transcript, one row per message: role 'user' is the contact,
-- role 'assistant' is the agent's reply — the same Role/Content model
-- call_messages uses for a voice call. Rows are append-only, ordered within a
-- thread by seq (two rapid turns can share a created_at), and deleted with the
-- conversation.
CREATE TABLE IF NOT EXISTS chat_messages (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	conversation_id TEXT NOT NULL REFERENCES chat_conversations(id) ON DELETE CASCADE,
	seq INTEGER NOT NULL CHECK (seq >= 0),
	role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
	content TEXT NOT NULL,
	-- The WhatsApp message id of an inbound message, when known. Redelivery of
	-- an already-answered message is common, so this is what keeps the second
	-- copy out of the transcript.
	wa_message_id TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (conversation_id, seq)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_messages_wa_id_unique
	ON chat_messages (conversation_id, wa_message_id)
	WHERE wa_message_id IS NOT NULL;

-- The conversation's summary follows its messages rather than being maintained
-- beside them, so the two cannot drift whichever code path appended the turn.
CREATE OR REPLACE FUNCTION sync_chat_conversation_summary()
RETURNS TRIGGER AS $$
BEGIN
	UPDATE chat_conversations
	SET message_count = message_count + 1,
		last_message_role = NEW.role,
		last_message_at = NEW.created_at,
		updated_at = now()
	WHERE id = NEW.conversation_id;
	-- AFTER trigger: the return value is ignored.
	RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS chat_messages_sync_conversation ON chat_messages;
CREATE TRIGGER chat_messages_sync_conversation
AFTER INSERT ON chat_messages
FOR EACH ROW EXECUTE FUNCTION sync_chat_conversation_summary();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS chat_messages_sync_conversation ON chat_messages;
DROP FUNCTION IF EXISTS sync_chat_conversation_summary();
DROP TABLE IF EXISTS chat_messages;
DROP TABLE IF EXISTS chat_conversations;
-- +goose StatementEnd
