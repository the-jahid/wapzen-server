package models

import "time"

// Outbound campaign lifecycle statuses. These mirror the
// outbound_campaigns.status CHECK constraint; any value written back to the
// table must be one of these.
const (
	CampaignStatusDraft     = "draft"
	CampaignStatusRunning   = "running"
	CampaignStatusPaused    = "paused"
	CampaignStatusCompleted = "completed"
	CampaignStatusFailed    = "failed"
)

// CampaignMaxNameLength mirrors the campaign_name CHECK constraint, so a name
// that is too long reads as a field error instead of a 500 from the insert.
const CampaignMaxNameLength = 80

// CampaignStatuses returns the allowed statuses in schema order, for validation
// and API documentation.
func CampaignStatuses() []string {
	return []string{
		CampaignStatusDraft,
		CampaignStatusRunning,
		CampaignStatusPaused,
		CampaignStatusCompleted,
		CampaignStatusFailed,
	}
}

// IsValidCampaignStatus reports whether status is one of the allowed statuses.
func IsValidCampaignStatus(status string) bool {
	switch status {
	case CampaignStatusDraft, CampaignStatusRunning, CampaignStatusPaused,
		CampaignStatusCompleted, CampaignStatusFailed:
		return true
	default:
		return false
	}
}

// IsTerminalCampaignStatus reports whether a campaign in this status is
// finished. A terminal campaign never moves again: re-running one would restart
// its counters against a record that already reported a final result.
func IsTerminalCampaignStatus(status string) bool {
	return status == CampaignStatusCompleted || status == CampaignStatusFailed
}

// CanTransitionCampaignStatus reports whether a campaign may move from one
// status to another. Staying put is allowed so an update that resends the
// current status is not refused for changing nothing.
//
// draft and paused may go on the air; a running campaign may be paused or
// finished, and may fail; the terminal statuses may not move at all.
func CanTransitionCampaignStatus(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case CampaignStatusDraft:
		return to == CampaignStatusRunning || to == CampaignStatusFailed
	case CampaignStatusPaused:
		return to == CampaignStatusRunning || to == CampaignStatusCompleted || to == CampaignStatusFailed
	case CampaignStatusRunning:
		return to == CampaignStatusPaused || to == CampaignStatusCompleted || to == CampaignStatusFailed
	default:
		return false
	}
}

// OutboundCampaign is one outbound calling campaign owned by an application
// user, persisted in the outbound_campaigns table. The JSON shape is the
// documented OutboundCampaign resource: the id is exposed as campaign_id, and
// the two rates are derived from the counters rather than stored.
//
// BudgetUSD is nullable because "no budget" is a real setting, distinct from a
// budget of zero, and is what the dashboard renders as "No Budget".
type OutboundCampaign struct {
	ID        string   `json:"campaign_id" example:"campaign_a456426614174000"`
	UserID    string   `json:"-"`
	Name      string   `json:"campaign_name" example:"July reactivation"`
	Status    string   `json:"status" example:"running"`
	BudgetUSD *float64 `json:"budget_usd" example:"250"`

	// AgentID is the agent the campaign dials with, and the only half of the
	// pairing a caller sets: the agent already carries the number it speaks on,
	// so AgentName, PhoneNumberID and PhoneNumber are read off the agent on
	// every read rather than stored here, and cannot disagree with it.
	AgentID       *string `json:"agent_id" example:"agent_9f1c2d3e4b5a6789"`
	AgentName     *string `json:"agent_name" example:"Pearl"`
	PhoneNumberID *string `json:"phone_number_id" example:"pn_2c1d4e5f6a7b8c90"`
	PhoneNumber   *string `json:"phone_number" example:"+390232164148"`

	// Counters, maintained by the server as the campaign places calls.
	LeadsCount        int `json:"leads_count" example:"1240"`
	CallsPlaced       int `json:"calls_placed" example:"656"`
	AnsweredCalls     int `json:"answered_calls" example:"412"`
	SuccessfulCalls   int `json:"successful_calls" example:"188"`
	TodayCalls        int `json:"today_calls" example:"37"`
	TotalUsageSeconds int `json:"total_usage_seconds" example:"74520"`

	// TodayCallsDate is the day TodayCalls counts, as a plain YYYY-MM-DD date.
	TodayCallsDate *string `json:"today_calls_date" example:"2026-08-10"`

	// PickupRate and SuccessRate are derived from the counters by
	// ApplyDerivedFields rather than stored, so they can never disagree with the
	// numbers they come from.
	PickupRate  float64 `json:"pickup_rate" example:"0.628"`
	SuccessRate float64 `json:"success_rate" example:"0.287"`

	StartedAt   *time.Time `json:"started_at" example:"2026-08-01T09:00:00Z"`
	CompletedAt *time.Time `json:"completed_at" example:"2026-08-10T18:00:00Z"`
	CreatedAt   time.Time  `json:"created_at" example:"2026-08-01T08:42:00Z"`
	UpdatedAt   time.Time  `json:"updated_at" example:"2026-08-10T11:15:00Z"`
}

// ApplyDerivedFields computes the two rates and clears a stale today's counter.
//
// It runs on every row leaving the repository, so the API is consistent no
// matter which query loaded the row. The rates are 0 — not NaN — for a campaign
// that has placed no calls, and today_calls reports 0 once its date is no longer
// today, since a counter left over from yesterday is not today's activity.
func (c *OutboundCampaign) ApplyDerivedFields(today time.Time) {
	if c.TodayCallsDate == nil || *c.TodayCallsDate != today.Format("2006-01-02") {
		c.TodayCalls = 0
	}

	if c.CallsPlaced <= 0 {
		c.PickupRate, c.SuccessRate = 0, 0
		return
	}
	c.PickupRate = roundRate(float64(c.AnsweredCalls) / float64(c.CallsPlaced))
	c.SuccessRate = roundRate(float64(c.SuccessfulCalls) / float64(c.CallsPlaced))
}

// roundRate trims a rate to three decimals, which is the precision the
// documented examples and the dashboard's one-decimal percentages need. Without
// it a ratio like 412/656 serializes as 0.6280487804878049.
func roundRate(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// NewOutboundCampaign is the data captured when a campaign is created. Only the
// owner and the name are required; a nil budget leaves the campaign uncapped,
// and a nil agent leaves the draft without one until it is given an agent to
// dial with. Every counter starts at zero and the status starts as draft, both
// left to the column defaults.
type NewOutboundCampaign struct {
	UserID    string
	Name      string
	BudgetUSD *float64
	AgentID   *string
}

// OutboundCampaignUpdate is a partial update. A nil field is left untouched;
// BudgetUSD and AgentID are the two fields that can be set back to NULL, so each
// carries its own flag rather than relying on the pointer alone — otherwise
// "uncap this campaign" and "leave the budget alone" would be the same request,
// as would "detach the agent" and "keep the agent".
// The lifecycle timestamps are not settable here: started_at and completed_at
// follow from the status the update writes, so the repository stamps them.
type OutboundCampaignUpdate struct {
	Name        *string
	Status      *string
	BudgetUSD   *float64
	ClearBudget bool
	AgentID     *string
	ClearAgent  bool
}

// IsEmpty reports whether the update would change nothing.
func (u OutboundCampaignUpdate) IsEmpty() bool {
	return u.Name == nil && u.Status == nil &&
		u.BudgetUSD == nil && !u.ClearBudget &&
		u.AgentID == nil && !u.ClearAgent
}
