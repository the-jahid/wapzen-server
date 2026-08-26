-- +goose Up
-- +goose StatementBegin
-- An outbound campaign is a named batch of outbound calling owned by a user.
-- It carries no agent and no phone number yet: the campaign record and its
-- counters are all that is stored, so the dashboard tiles have a home before
-- the dialling side is built.
--
-- The counters are maintained by the server as calls are placed. They are kept
-- on the row rather than aggregated from calls because calls has no
-- campaign_id: nothing links a call back to the campaign that placed it, so
-- there is nothing to aggregate from yet.
CREATE TABLE IF NOT EXISTS outbound_campaigns (
	id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	campaign_name TEXT NOT NULL
		CHECK (char_length(campaign_name) BETWEEN 1 AND 80),
	status TEXT NOT NULL DEFAULT 'draft'
		CHECK (status IN ('draft', 'running', 'paused', 'completed', 'failed')),

	-- Counters. Every one starts at zero, so a campaign that has done nothing
	-- reports zeroes rather than nulls the dashboard would have to special-case.
	leads_count INTEGER NOT NULL DEFAULT 0 CHECK (leads_count >= 0),
	calls_placed INTEGER NOT NULL DEFAULT 0 CHECK (calls_placed >= 0),
	answered_calls INTEGER NOT NULL DEFAULT 0 CHECK (answered_calls >= 0),
	successful_calls INTEGER NOT NULL DEFAULT 0 CHECK (successful_calls >= 0),
	-- today_calls counts one day only; today_calls_date says which, so a counter
	-- left over from yesterday is recognisable as stale instead of being read as
	-- today's activity.
	today_calls INTEGER NOT NULL DEFAULT 0 CHECK (today_calls >= 0),
	today_calls_date DATE,
	total_usage_seconds INTEGER NOT NULL DEFAULT 0 CHECK (total_usage_seconds >= 0),

	-- NULL is uncapped ("No Budget"), which is a different setting from a budget
	-- of zero — hence nullable rather than a defaulted 0.
	budget_usd NUMERIC(12, 2) CHECK (budget_usd IS NULL OR budget_usd >= 0),

	started_at TIMESTAMPTZ,
	completed_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

	-- A campaign cannot have answered or succeeded on calls it never placed.
	CHECK (answered_calls <= calls_placed),
	CHECK (successful_calls <= calls_placed)
);

CREATE INDEX IF NOT EXISTS idx_outbound_campaigns_user_id ON outbound_campaigns (user_id);
-- Names are unique per owner, not globally: two users may both run a "Q3
-- outreach" campaign. This backs the 409 returned by Create.
CREATE UNIQUE INDEX IF NOT EXISTS idx_outbound_campaigns_user_name_unique
	ON outbound_campaigns (user_id, campaign_name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbound_campaigns;
-- +goose StatementEnd
