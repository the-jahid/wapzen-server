-- +goose Up
-- +goose StatementBegin
-- Expand the per-agent live-call model constraint to the current non-preview
-- OpenAI Realtime models. Keep gpt-realtime-2 as the default for existing
-- agents and for newly created agents that omit this optional setting.
ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_voice_openai_realtime_model_check;

ALTER TABLE agents
	ADD CONSTRAINT agents_voice_openai_realtime_model_check
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
-- Values introduced above must be folded back before restoring 00017's
-- narrower constraint.
UPDATE agents
	SET voice_openai_realtime_model = 'gpt-realtime-2'
	WHERE voice_openai_realtime_model IN (
		'gpt-realtime-1.5',
		'gpt-realtime-2.1',
		'gpt-realtime-2.1-mini'
	);

ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_voice_openai_realtime_model_check;

ALTER TABLE agents
	ADD CONSTRAINT agents_voice_openai_realtime_model_check
	CHECK (voice_openai_realtime_model IN (
		'gpt-realtime-2',
		'gpt-realtime',
		'gpt-realtime-mini',
		'gpt-4o-realtime-preview',
		'gpt-4o-mini-realtime-preview'
	));
-- +goose StatementEnd
