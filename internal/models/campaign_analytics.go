package models

import "time"

// CampaignAnalyticsDefaultDays is how many days of daily activity the analytics
// endpoint reports when the caller does not say. Two weeks is enough to see a
// campaign's shape without the series turning into a stripe of ones.
const CampaignAnalyticsDefaultDays = 14

// CampaignAnalyticsMaxDays bounds the daily series so one request cannot ask the
// database to build an unbounded number of rows.
const CampaignAnalyticsMaxDays = 90

// CampaignAnalytics is one campaign's performance, aggregated from the leads it
// holds and the calls it placed.
//
// Everything here is computed from those rows rather than read off the
// campaign's stored counters. The two agree in normal operation — the same
// trigger maintains the counters from the same calls — but the counters are the
// campaign's lifetime totals and survive a deleted call, whereas analytics
// describes the calls that are actually still there to describe.
type CampaignAnalytics struct {
	CampaignID string `json:"campaign_id" example:"campaign_a456426614174000"`

	Leads CampaignLeadTotals `json:"leads"`
	Calls CampaignCallTotals `json:"calls"`

	// PickupRate is answered calls over calls placed; SuccessRate is calls that
	// were answered and then ended normally over calls placed; ReachRate is the
	// share of leads that have been called. All three are 0 when their
	// denominator is, rather than NaN.
	PickupRate  float64 `json:"pickup_rate" example:"0.62"`
	SuccessRate float64 `json:"success_rate" example:"0.28"`
	ReachRate   float64 `json:"reach_rate" example:"0.41"`

	TalkTime CampaignTalkTime `json:"talk_time"`

	// Daily is one row per day, oldest first, zero-filled so a day with no calls
	// is a gap in the chart rather than a missing bar.
	Daily []CampaignDailyActivity `json:"daily"`

	// EndReasons are the most common reasons this campaign's calls ended, most
	// frequent first. Calls that never recorded one are left out.
	EndReasons []CampaignEndReason `json:"end_reasons"`

	FirstCallAt *time.Time `json:"first_call_at" example:"2026-08-10T10:00:00Z"`
	LastCallAt  *time.Time `json:"last_call_at" example:"2026-08-14T16:20:00Z"`
}

// CampaignLeadTotals counts the campaign's leads by lifecycle status.
type CampaignLeadTotals struct {
	Total   int `json:"total" example:"120"`
	Pending int `json:"pending" example:"64"`
	Calling int `json:"calling" example:"2"`
	Called  int `json:"called" example:"43"`
	Failed  int `json:"failed" example:"11"`
}

// CampaignCallTotals counts the campaign's calls by lifecycle status, plus the
// two counts that cut across those statuses. Received/Answered/Ended/Declined/
// Failed are where the calls stand right now and sum to Total; Connected is
// every call somebody picked up (whether or not it has ended since), and
// Successful is those that then ended normally.
type CampaignCallTotals struct {
	Total      int `json:"total" example:"96"`
	Received   int `json:"received" example:"1"`
	Answered   int `json:"answered" example:"2"`
	Ended      int `json:"ended" example:"70"`
	Declined   int `json:"declined" example:"18"`
	Failed     int `json:"failed" example:"5"`
	Connected  int `json:"connected" example:"60"`
	Successful int `json:"successful" example:"58"`
}

// CampaignTalkTime is how long this campaign has spent connected. Only answered
// calls have a duration, so these describe the conversations, not the attempts.
type CampaignTalkTime struct {
	TotalSeconds   int `json:"total_seconds" example:"7420"`
	AverageSeconds int `json:"average_seconds" example:"124"`
	LongestSeconds int `json:"longest_seconds" example:"431"`
}

// CampaignDailyActivity is one day of the campaign's calling: how many calls it
// placed and how many of them were answered.
type CampaignDailyActivity struct {
	Date     string `json:"date" example:"2026-08-14"`
	Calls    int    `json:"calls" example:"12"`
	Answered int    `json:"answered" example:"7"`
}

// CampaignEndReason is one recorded hangup reason and how often it happened.
type CampaignEndReason struct {
	Reason string `json:"reason" example:"callee hung up"`
	Count  int    `json:"count" example:"23"`
}

// ApplyDerivedRates fills in the three rates from the counts already loaded, so
// they can never disagree with the numbers they come from.
func (a *CampaignAnalytics) ApplyDerivedRates() {
	if a.Calls.Total > 0 {
		a.PickupRate = roundRate(float64(a.Calls.Connected) / float64(a.Calls.Total))
		a.SuccessRate = roundRate(float64(a.Calls.Successful) / float64(a.Calls.Total))
	}
	if a.Leads.Total > 0 {
		a.ReachRate = roundRate(float64(a.Leads.Called) / float64(a.Leads.Total))
	}
}
