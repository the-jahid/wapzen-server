-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS campaign_leads (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	campaign_id TEXT NOT NULL REFERENCES outbound_campaigns(id) ON DELETE CASCADE,
	phone_number TEXT NOT NULL CHECK (phone_number ~ '^\+[1-9][0-9]{7,14}$'),
	email TEXT,
	first_name TEXT CHECK (first_name IS NULL OR char_length(first_name) <= 80),
	last_name TEXT CHECK (last_name IS NULL OR char_length(last_name) <= 80),
	status TEXT NOT NULL DEFAULT 'pending'
		CHECK (status IN ('pending', 'calling', 'contacted', 'failed', 'opted_out')),
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	last_attempted_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (campaign_id, phone_number)
);

CREATE INDEX IF NOT EXISTS idx_campaign_leads_campaign_id ON campaign_leads (campaign_id);
CREATE INDEX IF NOT EXISTS idx_campaign_leads_campaign_status ON campaign_leads (campaign_id, status);

CREATE OR REPLACE FUNCTION sync_outbound_campaign_leads_count()
RETURNS TRIGGER AS $$
BEGIN
	IF TG_OP = 'INSERT' THEN
		UPDATE outbound_campaigns SET leads_count = leads_count + 1, updated_at = now() WHERE id = NEW.campaign_id;
	ELSIF TG_OP = 'DELETE' THEN
		UPDATE outbound_campaigns SET leads_count = GREATEST(leads_count - 1, 0), updated_at = now() WHERE id = OLD.campaign_id;
	ELSIF NEW.campaign_id IS DISTINCT FROM OLD.campaign_id THEN
		UPDATE outbound_campaigns SET leads_count = GREATEST(leads_count - 1, 0), updated_at = now() WHERE id = OLD.campaign_id;
		UPDATE outbound_campaigns SET leads_count = leads_count + 1, updated_at = now() WHERE id = NEW.campaign_id;
	END IF;
	-- This is an AFTER trigger, so its return value is ignored.
	RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER campaign_leads_sync_count
AFTER INSERT OR DELETE OR UPDATE OF campaign_id ON campaign_leads
FOR EACH ROW EXECUTE FUNCTION sync_outbound_campaign_leads_count();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS campaign_leads_sync_count ON campaign_leads;
DROP FUNCTION IF EXISTS sync_outbound_campaign_leads_count();
DROP TABLE IF EXISTS campaign_leads;
-- +goose StatementEnd
