-- +goose Up
-- +goose StatementBegin
-- Remove storage that is not present in the current direct-owner schema. The
-- safe fragments used to display an API key are folded into key_prefix before
-- last4 is removed, so existing keys remain identifiable without storing their
-- bearer value.
DO $$
BEGIN
	IF EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND table_name = 'api_keys'
			AND column_name = 'last4'
	) THEN
		UPDATE api_keys
		SET key_prefix = key_prefix || '...' || last4
		WHERE key_prefix NOT LIKE '%...%';
		ALTER TABLE api_keys DROP COLUMN last4;
	END IF;
END $$;

DROP TABLE IF EXISTS agent_post_call_fields;
DROP TABLE IF EXISTS agent_dynamic_variables;

ALTER TABLE agents
	ALTER COLUMN transcriber_elevenlabs_model SET DEFAULT 'scribe_v1';

-- The latest phone-number lifecycle has three persisted states. Pairing and
-- connection failures are represented as disconnected by the server.
UPDATE phone_numbers
SET status = 'disconnected', updated_at = now()
WHERE status NOT IN ('pending_qr', 'connected', 'disconnected');

ALTER TABLE phone_numbers DROP CONSTRAINT IF EXISTS phone_numbers_status_check;
ALTER TABLE phone_numbers
	ADD CONSTRAINT phone_numbers_status_check
	CHECK (status IN ('pending_qr', 'connected', 'disconnected'));

-- Campaign leads now finish as called or failed. Preserve the closest meaning
-- for rows written by the previous status vocabulary.
ALTER TABLE campaign_leads DROP CONSTRAINT IF EXISTS campaign_leads_status_check;
UPDATE campaign_leads SET status = 'called', updated_at = now() WHERE status = 'contacted';
UPDATE campaign_leads SET status = 'failed', updated_at = now() WHERE status = 'opted_out';

ALTER TABLE campaign_leads
	ADD CONSTRAINT campaign_leads_status_check
	CHECK (status IN ('pending', 'calling', 'called', 'failed'));

-- Rebuild the progress trigger before dropping today_calls_date. Counting
-- today's calls from the calls table keeps today_calls accurate without a
-- companion date column.
CREATE OR REPLACE FUNCTION sync_campaign_call_progress()
RETURNS TRIGGER AS $$
BEGIN
	IF TG_OP = 'INSERT' THEN
		IF NEW.campaign_id IS NOT NULL THEN
			UPDATE outbound_campaigns
			SET calls_placed = calls_placed + 1,
				today_calls = (
					SELECT count(*)::integer
					FROM calls
					WHERE campaign_id = NEW.campaign_id
						AND created_at >= current_date
						AND created_at < current_date + interval '1 day'
				),
				updated_at = now()
			WHERE id = NEW.campaign_id;
		END IF;

		IF NEW.lead_id IS NOT NULL THEN
			UPDATE campaign_leads
			SET attempts = attempts + 1,
				last_attempted_at = now(),
				status = 'calling',
				updated_at = now()
			WHERE id = NEW.lead_id;
		END IF;
		RETURN NULL;
	END IF;

	IF NEW.campaign_id IS NOT NULL THEN
		UPDATE outbound_campaigns
		SET answered_calls = answered_calls
				+ CASE WHEN NEW.status = 'answered' THEN 1 ELSE 0 END,
			successful_calls = successful_calls
				+ CASE WHEN NEW.status = 'ended' AND NEW.answered_at IS NOT NULL THEN 1 ELSE 0 END,
			total_usage_seconds = total_usage_seconds
				+ CASE WHEN NEW.status = 'ended' THEN COALESCE(NEW.duration_seconds, 0) ELSE 0 END,
			updated_at = now()
		WHERE id = NEW.campaign_id;
	END IF;

	IF NEW.lead_id IS NOT NULL THEN
		UPDATE campaign_leads
		SET status = CASE
				WHEN NEW.status IN ('answered', 'ended') AND NEW.answered_at IS NOT NULL THEN 'called'
				WHEN NEW.status IN ('ended', 'declined', 'failed') THEN 'failed'
				ELSE status
			END,
			updated_at = now()
		WHERE id = NEW.lead_id;
	END IF;

	RETURN NULL;
END;
$$ LANGUAGE plpgsql;

UPDATE outbound_campaigns c
SET today_calls = (
	SELECT count(*)::integer
	FROM calls
	WHERE campaign_id = c.id
		AND created_at >= current_date
		AND created_at < current_date + interval '1 day'
);

ALTER TABLE outbound_campaigns DROP COLUMN IF EXISTS today_calls_date;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE outbound_campaigns ADD COLUMN IF NOT EXISTS today_calls_date DATE;
UPDATE outbound_campaigns
SET today_calls_date = current_date
WHERE today_calls > 0 AND today_calls_date IS NULL;

ALTER TABLE campaign_leads DROP CONSTRAINT IF EXISTS campaign_leads_status_check;
UPDATE campaign_leads SET status = 'contacted', updated_at = now() WHERE status = 'called';
ALTER TABLE campaign_leads
	ADD CONSTRAINT campaign_leads_status_check
	CHECK (status IN ('pending', 'calling', 'contacted', 'failed', 'opted_out'));

ALTER TABLE phone_numbers DROP CONSTRAINT IF EXISTS phone_numbers_status_check;
ALTER TABLE phone_numbers
	ADD CONSTRAINT phone_numbers_status_check
	CHECK (status IN ('pending_qr', 'connected', 'disconnected', 'failed', 'expired'));

CREATE TABLE IF NOT EXISTS agent_dynamic_variables (
	agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	value TEXT NOT NULL,
	PRIMARY KEY (agent_id, name)
);

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
CREATE INDEX IF NOT EXISTS idx_agent_post_call_fields_agent_id
	ON agent_post_call_fields (agent_id);

ALTER TABLE agents
	ALTER COLUMN transcriber_elevenlabs_model SET DEFAULT 'scribe_v2';

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS last4 TEXT;
UPDATE api_keys
SET last4 = CASE
		WHEN key_prefix LIKE '%...%' THEN split_part(key_prefix, '...', 2)
		ELSE ''
	END,
	key_prefix = split_part(key_prefix, '...', 1);
ALTER TABLE api_keys ALTER COLUMN last4 SET NOT NULL;

CREATE OR REPLACE FUNCTION sync_campaign_call_progress()
RETURNS TRIGGER AS $$
BEGIN
	IF TG_OP = 'INSERT' THEN
		IF NEW.campaign_id IS NOT NULL THEN
			UPDATE outbound_campaigns
			SET calls_placed = calls_placed + 1,
				today_calls = CASE WHEN today_calls_date = current_date THEN today_calls + 1 ELSE 1 END,
				today_calls_date = current_date,
				updated_at = now()
			WHERE id = NEW.campaign_id;
		END IF;
		IF NEW.lead_id IS NOT NULL THEN
			UPDATE campaign_leads
			SET attempts = attempts + 1,
				last_attempted_at = now(),
				status = CASE WHEN status = 'opted_out' THEN status ELSE 'calling' END,
				updated_at = now()
			WHERE id = NEW.lead_id;
		END IF;
		RETURN NULL;
	END IF;

	IF NEW.campaign_id IS NOT NULL THEN
		UPDATE outbound_campaigns
		SET answered_calls = answered_calls + CASE WHEN NEW.status = 'answered' THEN 1 ELSE 0 END,
			successful_calls = successful_calls
				+ CASE WHEN NEW.status = 'ended' AND NEW.answered_at IS NOT NULL THEN 1 ELSE 0 END,
			total_usage_seconds = total_usage_seconds
				+ CASE WHEN NEW.status = 'ended' THEN COALESCE(NEW.duration_seconds, 0) ELSE 0 END,
			updated_at = now()
		WHERE id = NEW.campaign_id;
	END IF;

	IF NEW.lead_id IS NOT NULL THEN
		UPDATE campaign_leads
		SET status = CASE
				WHEN status = 'opted_out' THEN status
				WHEN NEW.status IN ('answered', 'ended') AND NEW.answered_at IS NOT NULL THEN 'contacted'
				WHEN NEW.status IN ('ended', 'declined', 'failed') THEN 'failed'
				ELSE status
			END,
			updated_at = now()
		WHERE id = NEW.lead_id;
	END IF;
	RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
