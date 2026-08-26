-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_transcriber_openai_model_check;

ALTER TABLE agents
	ADD CONSTRAINT agents_transcriber_openai_model_check
	CHECK (transcriber_openai_model IN (
		'gpt-4o-transcribe',
		'gpt-4o-mini-transcribe',
		'gpt-4o-transcribe-diarize',
		'whisper-1'
	));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE agents
SET transcriber_openai_model = 'gpt-4o-transcribe'
WHERE transcriber_openai_model IN ('gpt-4o-transcribe-diarize', 'whisper-1');

ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_transcriber_openai_model_check;

ALTER TABLE agents
	ADD CONSTRAINT agents_transcriber_openai_model_check
	CHECK (transcriber_openai_model IN (
		'gpt-4o-transcribe',
		'gpt-4o-mini-transcribe'
	));
-- +goose StatementEnd
