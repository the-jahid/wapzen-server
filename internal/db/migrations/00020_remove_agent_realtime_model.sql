-- +goose Up
-- +goose StatementBegin
-- OpenAI inbound calls now use Transcriptions -> selected text model -> Speech,
-- so the old speech-to-speech model selection is no longer part of an agent.
ALTER TABLE agents
	DROP COLUMN IF EXISTS voice_openai_realtime_model;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS voice_openai_realtime_model TEXT NOT NULL DEFAULT 'gpt-realtime-2'
		CHECK (voice_openai_realtime_model IN (
			'gpt-realtime',
			'gpt-realtime-1.5',
			'gpt-realtime-mini',
			'gpt-realtime-2',
			'gpt-realtime-2.1',
			'gpt-realtime-2.1-mini'
		));
-- +goose StatementEnd
