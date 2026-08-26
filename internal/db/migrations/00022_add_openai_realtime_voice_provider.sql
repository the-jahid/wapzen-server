-- +goose Up
-- +goose StatementBegin
ALTER TABLE agents
    DROP CONSTRAINT agents_voice_provider_check;

ALTER TABLE agents
    ADD CONSTRAINT agents_voice_provider_check
    CHECK (voice_provider IN ('openai_realtime', 'openai', '11labs'));

ALTER TABLE agents
    ALTER COLUMN voice_provider SET DEFAULT 'openai_realtime';

-- The two OpenAI providers have overlapping but not identical voice sets.
-- Persist their union; runtime validation narrows the selected voice for the
-- active pipeline.
ALTER TABLE agents
    DROP CONSTRAINT agents_voice_openai_voice_id_check;

ALTER TABLE agents
    ADD CONSTRAINT agents_voice_openai_voice_id_check
    CHECK (voice_openai_voice_id IN (
        'alloy', 'ash', 'ballad', 'coral', 'echo', 'sage', 'shimmer',
        'verse', 'marin', 'cedar', 'fable', 'nova', 'onyx'
    ));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE agents
SET voice_provider = 'openai'
WHERE voice_provider = 'openai_realtime';

UPDATE agents SET voice_openai_voice_id = 'ash', voice_openai_voice_name = 'Ash'
WHERE voice_openai_voice_id = 'onyx';
UPDATE agents SET voice_openai_voice_id = 'shimmer', voice_openai_voice_name = 'Shimmer'
WHERE voice_openai_voice_id = 'nova';
UPDATE agents SET voice_openai_voice_id = 'ballad', voice_openai_voice_name = 'Ballad'
WHERE voice_openai_voice_id = 'fable';

ALTER TABLE agents
    DROP CONSTRAINT agents_voice_openai_voice_id_check;

ALTER TABLE agents
    ADD CONSTRAINT agents_voice_openai_voice_id_check
    CHECK (voice_openai_voice_id IN (
        'alloy', 'ash', 'ballad', 'coral', 'echo',
        'sage', 'shimmer', 'verse', 'marin', 'cedar'
    ));

ALTER TABLE agents
    DROP CONSTRAINT agents_voice_provider_check;

ALTER TABLE agents
    ADD CONSTRAINT agents_voice_provider_check
    CHECK (voice_provider IN ('openai', '11labs'));

ALTER TABLE agents
    ALTER COLUMN voice_provider SET DEFAULT 'openai';
-- +goose StatementEnd
