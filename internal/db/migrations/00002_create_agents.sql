-- +goose Up
-- +goose StatementBegin
-- Every scalar field of the agent config is its own column. Simple string lists
-- are TEXT[] columns. The two structures that cannot be a single column -- the
-- dynamic_variables map and the post_call_analysis_data list-of-objects -- live
-- in child tables, where each of their fields is again its own column.
CREATE TABLE IF NOT EXISTS agents (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

	-- ── agent (identity) ──
	agent_name TEXT NOT NULL,
	language TEXT NOT NULL DEFAULT 'en-US',
	timezone TEXT,
	call_direction TEXT NOT NULL DEFAULT 'outbound'
		CHECK (call_direction IN ('outbound')),
	status TEXT NOT NULL DEFAULT 'active'
		CHECK (status IN ('active', 'inactive')),

	-- ── model ──
	model_provider TEXT NOT NULL DEFAULT 'openai'
		CHECK (model_provider IN ('openai', 'anthropic')),
	model_name TEXT NOT NULL DEFAULT 'gpt-4.1-mini',
	model_temperature DOUBLE PRECISION DEFAULT 0.3
		CHECK (model_temperature >= 0.1 AND model_temperature <= 1),

	-- ── prompt ──
	prompt_begin_message_mode TEXT NOT NULL DEFAULT 'agent_speaks_first'
		CHECK (prompt_begin_message_mode IN ('agent_speaks_first', 'agent_waits_for_user', 'agent_speaks_first_with_model_generated_message')),
	prompt_begin_message TEXT DEFAULT 'Hello! How can I help you today?',
	prompt_begin_message_delay_ms INTEGER DEFAULT 1000
		CHECK (prompt_begin_message_delay_ms >= 0 AND prompt_begin_message_delay_ms <= 5000),
	prompt_system_prompt TEXT DEFAULT 'You are a helpful, friendly voice assistant on a phone call. Keep responses clear and concise, speak naturally, and stay polite and professional at all times.',
	prompt_goals TEXT[] DEFAULT ARRAY[
		'Understand what the caller needs and help them with it'
	],
	prompt_rules TEXT[] DEFAULT ARRAY[
		'Be polite, patient, and professional at all times'
	],

	-- prompt.conversation_style
	prompt_style_tone TEXT DEFAULT 'warm and professional',
	prompt_style_response_length TEXT DEFAULT 'medium',
	prompt_style_empathy_level TEXT DEFAULT 'high',

	-- ── voice ──
	voice_provider TEXT DEFAULT '11labs'
		CHECK (voice_provider IN ('11labs', 'openai')),

	
	-- voice.elevenlabs (voice_id, voice_name and voice_model are required)
	voice_elevenlabs_voice_id TEXT NOT NULL DEFAULT 'DODLEQrClDo8wCz460ld', -- ElevenLabs "Lauren"
	voice_elevenlabs_voice_name TEXT NOT NULL DEFAULT 'Lauren',
	voice_elevenlabs_voice_model TEXT NOT NULL DEFAULT 'eleven_turbo_v2_5'
		CHECK (voice_elevenlabs_voice_model IN ('eleven_multilingual_v2', 'eleven_turbo_v2', 'eleven_turbo_v2_5', 'eleven_flash_v2', 'eleven_flash_v2_5', 'eleven_monolingual_v1', 'eleven_v3')),

	
	-- voice.openai (voice_id, voice_name and voice_model are required)
	voice_openai_voice_id TEXT NOT NULL DEFAULT 'alloy'
		CHECK (voice_openai_voice_id IN ('alloy', 'echo', 'fable', 'nova', 'onyx', 'shimmer')),
	voice_openai_voice_name TEXT NOT NULL DEFAULT 'Alloy',
	voice_openai_voice_model TEXT NOT NULL DEFAULT 'tts-1'
		CHECK (voice_openai_voice_model IN ('tts-1', 'tts-1-hd', 'gpt-4o-mini-tts')),
	voice_openai_instructions TEXT,


	-- ── transcriber ──
	transcriber_provider TEXT NOT NULL DEFAULT 'openai'
		CHECK (transcriber_provider IN ('openai', '11labs')),


	transcriber_language TEXT NOT NULL DEFAULT 'en',

	-- transcriber.openai
	transcriber_openai_model TEXT NOT NULL DEFAULT 'gpt-4o-transcribe'
		CHECK (transcriber_openai_model IN ('gpt-4o-transcribe', 'gpt-4o-mini-transcribe', 'gpt-4o-transcribe-diarize', 'whisper-1')),

	-- transcriber.elevenlabs
	transcriber_elevenlabs_model TEXT NOT NULL DEFAULT 'scribe_v1'
		CHECK (transcriber_elevenlabs_model IN ('scribe_v1', 'scribe_v2', 'scribe_v2_realtime')),

	-- ── post_call ── (post_call_analysis_data → child table)
	post_call_analysis_provider TEXT NOT NULL DEFAULT 'openai'
		CHECK (post_call_analysis_provider IN ('openai', 'anthropic')),
	post_call_analysis_model TEXT,

	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agents_user_id ON agents (user_id);

-- prompt.dynamic_variables — a free-form string→string map, one row per entry.
CREATE TABLE IF NOT EXISTS agent_dynamic_variables (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	value TEXT NOT NULL,
	PRIMARY KEY (agent_id, name)
);

-- post_call.post_call_analysis_data — list of fields to extract after a call.
CREATE TABLE IF NOT EXISTS agent_post_call_fields (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	type TEXT NOT NULL CHECK (type IN ('string', 'number', 'boolean', 'enum')),
	name TEXT NOT NULL,
	description TEXT,
	examples TEXT[],
	required BOOLEAN NOT NULL DEFAULT false,
	enum_values TEXT[],
	conditional_prompt TEXT
);

CREATE INDEX IF NOT EXISTS idx_agent_post_call_fields_agent_id ON agent_post_call_fields (agent_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_post_call_fields;
DROP TABLE IF EXISTS agent_dynamic_variables;
DROP TABLE IF EXISTS agents;
-- +goose StatementEnd
