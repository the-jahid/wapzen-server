-- +goose Up
-- +goose StatementBegin
-- OpenAI Realtime is once again an explicit per-agent live-call provider, so
-- each agent needs to keep the Realtime model selected in the dashboard.
ALTER TABLE agents
	ADD COLUMN voice_openai_realtime_model TEXT NOT NULL DEFAULT 'gpt-realtime-2.1-mini'
		CHECK (voice_openai_realtime_model IN (
			'gpt-realtime',
			'gpt-realtime-1.5',
			'gpt-realtime-mini',
			'gpt-realtime-2',
			'gpt-realtime-2.1',
			'gpt-realtime-2.1-mini'
		));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	DROP COLUMN IF EXISTS voice_openai_realtime_model;
-- +goose StatementEnd
