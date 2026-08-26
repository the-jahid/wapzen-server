-- +goose Up
-- +goose StatementBegin
-- Link a call back to the outbound campaign (and the lead) that caused it.
--
-- Until now nothing tied a call to a campaign, so the campaign counters were
-- maintained by hand and a campaign's own call history could not be listed.
-- Both columns are nullable because most calls have neither: an inbound call and
-- a one-off outbound call belong to no campaign.
--
-- ON DELETE SET NULL on both, the way calls.agent_id behaves: deleting a
-- campaign or a lead must not delete the record that the call happened. The call
-- then reads as belonging to no campaign, and its counters stay where they were.
ALTER TABLE calls
	ADD COLUMN IF NOT EXISTS campaign_id TEXT REFERENCES outbound_campaigns(id) ON DELETE SET NULL,
	ADD COLUMN IF NOT EXISTS lead_id TEXT REFERENCES campaign_leads(id) ON DELETE SET NULL;

-- The campaign Calls tab reads one campaign's calls newest first; the lead index
-- backs the per-lead lookups the trigger below and the dashboard both make.
CREATE INDEX IF NOT EXISTS idx_calls_campaign_created
	ON calls (campaign_id, created_at DESC)
	WHERE campaign_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_calls_lead_id
	ON calls (lead_id)
	WHERE lead_id IS NOT NULL;

-- Campaign counters and lead lifecycle now follow the call itself.
--
-- They are maintained here rather than in the handler because a call's later
-- states are written deep in the voice-call runtime (answered / ended / declined
-- / failed), which knows nothing about campaigns. Driving both off the calls row
-- means the numbers cannot drift from the calls they describe, whichever code
-- path moved the call.
--
-- A lead that opted out is never moved: that is a decision by the person on the
-- other end, not something a dial outcome may overwrite.
CREATE OR REPLACE FUNCTION sync_campaign_call_progress()
RETURNS TRIGGER AS $$
BEGIN
	IF TG_OP = 'INSERT' THEN
		IF NEW.campaign_id IS NOT NULL THEN
			UPDATE outbound_campaigns
			SET calls_placed = calls_placed + 1,
				-- today_calls counts one day; a counter dated before today is
				-- restarted rather than added to.
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

		-- AFTER trigger: the return value is ignored.
		RETURN NULL;
	END IF;

	IF NEW.campaign_id IS NOT NULL THEN
		UPDATE outbound_campaigns
		SET answered_calls = answered_calls
				+ CASE WHEN NEW.status = 'answered' THEN 1 ELSE 0 END,
			-- "Successful" is a call that was picked up and then ended normally.
			-- A call that ends without ever having been answered succeeded at
			-- nothing.
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
				WHEN NEW.status = 'answered' THEN 'contacted'
				WHEN NEW.status = 'ended' AND NEW.answered_at IS NOT NULL THEN 'contacted'
				WHEN NEW.status IN ('ended', 'declined', 'failed') THEN 'failed'
				ELSE status
			END,
			updated_at = now()
		WHERE id = NEW.lead_id;
	END IF;

	RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER calls_sync_campaign_progress_insert
AFTER INSERT ON calls
FOR EACH ROW
WHEN (NEW.campaign_id IS NOT NULL OR NEW.lead_id IS NOT NULL)
EXECUTE FUNCTION sync_campaign_call_progress();

CREATE TRIGGER calls_sync_campaign_progress_update
AFTER UPDATE OF status ON calls
FOR EACH ROW
WHEN ((NEW.campaign_id IS NOT NULL OR NEW.lead_id IS NOT NULL)
	AND NEW.status IS DISTINCT FROM OLD.status)
EXECUTE FUNCTION sync_campaign_call_progress();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS calls_sync_campaign_progress_update ON calls;
DROP TRIGGER IF EXISTS calls_sync_campaign_progress_insert ON calls;
DROP FUNCTION IF EXISTS sync_campaign_call_progress();

DROP INDEX IF EXISTS idx_calls_lead_id;
DROP INDEX IF EXISTS idx_calls_campaign_created;

ALTER TABLE calls
	DROP COLUMN IF EXISTS lead_id,
	DROP COLUMN IF EXISTS campaign_id;
-- +goose StatementEnd
