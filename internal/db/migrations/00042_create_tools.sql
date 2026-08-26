-- +goose Up
-- +goose StatementBegin
-- A tool is one action an agent can take while a call is running: hit an HTTP
-- endpoint and speak the answer back, hand the caller to another number, send
-- the caller a WhatsApp message, or hang up. The row only defines the action —
-- it is inert until an agent is attached to it (see agent_tools).
--
-- name and description are prompt text, not labels: they are what the model
-- reads when deciding whether to call the tool, which is why name is
-- constrained to a valid function identifier (the providers reject anything
-- else) and both are required.
--
-- The type-specific settings live in one JSONB column rather than in a wide
-- table of mostly-NULL columns. The three variants share almost no fields, the
-- api_request block nests two arrays (headers and parameters) that have no
-- identity of their own, and the documented contract replaces a configuration
-- block wholesale rather than merging into it — so there is nothing a column
-- per field would buy. The server validates the block's shape per type before
-- it is stored; Postgres only guarantees it is JSON.
CREATE TABLE IF NOT EXISTS tools (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	type TEXT NOT NULL
		CHECK (type IN ('api_request', 'transfer_call', 'end_call', 'send_text')),
	-- Lowercase letters, digits and underscores, starting with a letter: the
	-- intersection of what OpenAI and Anthropic accept as a function name.
	tool_name TEXT NOT NULL
		CHECK (tool_name ~ '^[a-z][a-z0-9_]{0,63}$'),
	description TEXT NOT NULL
		CHECK (char_length(description) BETWEEN 1 AND 1000),
	-- The configuration block matching type, or {} for a type that needs none.
	config JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tools_user_id ON tools (user_id);
-- Function names are unique per owner. Two tools sharing a name on one call
-- would be ambiguous to the model, and the owner is the only scope in which
-- that can be prevented up front — this backs the 409 returned by Create.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tools_user_name_unique
	ON tools (user_id, tool_name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tools;
-- +goose StatementEnd
