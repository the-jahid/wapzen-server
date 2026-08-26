-- +goose Up
-- +goose StatementBegin
-- Calls are answered by the OpenAI Realtime API, which accepts a different set
-- of voices than the /v1/audio/speech TTS API: 'fable', 'nova' and 'onyx' are
-- TTS-only. Storing one of those was not a cosmetic mismatch — the Realtime API
-- rejects the entire session.update when a single field is invalid, so the
-- agent's instructions, turn detection and transcription settings were dropped
-- along with the voice and the call ran on API defaults.
--
-- Drop the old constraint before remapping: the replacement voices are not in
-- the pre-00016 voice set, so the UPDATEs below would violate it.
ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_voice_openai_voice_id_check;

-- Remap the three TTS-only voices onto their nearest Realtime equivalent before
-- narrowing the constraint to the voices the call can actually use.
UPDATE agents SET voice_openai_voice_id = 'ash', voice_openai_voice_name = 'Ash'
	WHERE voice_openai_voice_id = 'onyx';
UPDATE agents SET voice_openai_voice_id = 'shimmer', voice_openai_voice_name = 'Shimmer'
	WHERE voice_openai_voice_id = 'nova';
UPDATE agents SET voice_openai_voice_id = 'ballad', voice_openai_voice_name = 'Ballad'
	WHERE voice_openai_voice_id = 'fable';

ALTER TABLE agents
	ADD CONSTRAINT agents_voice_openai_voice_id_check
	CHECK (voice_openai_voice_id IN (
		'alloy', 'ash', 'ballad', 'coral', 'echo',
		'sage', 'shimmer', 'verse', 'marin', 'cedar'
	));

-- Speed is sent to the Realtime API as audio.output.speed (its accepted range
-- is 0.25–1.5). Volume has no API equivalent and is applied as a gain on the
-- audio played into the call, so it is capped to keep amplified audio from
-- clipping. 1 means "unchanged" for both.
ALTER TABLE agents
	ADD COLUMN IF NOT EXISTS voice_openai_speed DOUBLE PRECISION NOT NULL DEFAULT 1
		CHECK (voice_openai_speed >= 0.25 AND voice_openai_speed <= 1.5),
	ADD COLUMN IF NOT EXISTS voice_openai_volume DOUBLE PRECISION NOT NULL DEFAULT 1
		CHECK (voice_openai_volume >= 0 AND voice_openai_volume <= 2);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agents
	DROP COLUMN IF EXISTS voice_openai_speed,
	DROP COLUMN IF EXISTS voice_openai_volume;

ALTER TABLE agents
	DROP CONSTRAINT IF EXISTS agents_voice_openai_voice_id_check;

-- The pre-00016 voice set. Rows holding a Realtime-only voice would violate it,
-- so fold them back onto the closest of the original six first.
UPDATE agents SET voice_openai_voice_id = 'onyx', voice_openai_voice_name = 'Onyx'
	WHERE voice_openai_voice_id IN ('ash', 'cedar');
UPDATE agents SET voice_openai_voice_id = 'nova', voice_openai_voice_name = 'Nova'
	WHERE voice_openai_voice_id IN ('coral', 'marin', 'sage');
UPDATE agents SET voice_openai_voice_id = 'fable', voice_openai_voice_name = 'Fable'
	WHERE voice_openai_voice_id IN ('ballad', 'verse');

ALTER TABLE agents
	ADD CONSTRAINT agents_voice_openai_voice_id_check
	CHECK (voice_openai_voice_id IN ('alloy', 'echo', 'fable', 'nova', 'onyx', 'shimmer'));
-- +goose StatementEnd
