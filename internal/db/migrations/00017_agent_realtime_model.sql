-- +goose Up
-- +goose StatementBegin
-- The model that answers live calls was server-owned (OPENAI_REALTIME_MODEL),
-- so every agent on a deployment had to share one. Give each agent its own,
-- defaulting to the same model the server default used.
--
-- This is the speech-to-speech model for live calls, which is a different thing
-- from model_name (the text LLM) and from voice_openai_voice_model (the TTS
-- model used for generated audio files). All three coexist.
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS voice_openai_realtime_model TEXT NOT NULL DEFAULT 'gpt-realtime-2'
		CHECK (voice_openai_realtime_model IN (
			'gpt-realtime-2',
			'gpt-realtime',
			'gpt-realtime-mini',
			'gpt-4o-realtime-preview',
			'gpt-4o-mini-realtime-preview'
		));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	DROP COLUMN IF EXISTS voice_openai_realtime_model;
-- +goose StatementEnd
