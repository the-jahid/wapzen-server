-- +goose Up
-- +goose StatementBegin
UPDATE agents
SET transcriber_elevenlabs_model = 'scribe_v2'
WHERE transcriber_elevenlabs_model = 'scribe_v1';

UPDATE agents
SET voice_elevenlabs_voice_model = 'eleven_flash_v2_5'
WHERE voice_elevenlabs_voice_model = 'eleven_monolingual_v1';

ALTER TABLE agents
	ALTER COLUMN transcriber_elevenlabs_model SET DEFAULT 'scribe_v2';

ALTER TABLE agents
	ALTER COLUMN voice_elevenlabs_voice_model SET DEFAULT 'eleven_flash_v2_5';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	ALTER COLUMN transcriber_elevenlabs_model SET DEFAULT 'scribe_v1';

ALTER TABLE agents
	ALTER COLUMN voice_elevenlabs_voice_model SET DEFAULT 'eleven_turbo_v2_5';
-- +goose StatementEnd
