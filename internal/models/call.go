package models

import "time"

// Call lifecycle statuses. These mirror the calls.status CHECK constraint; any
// value written back to the table must be one of these.
const (
	CallStatusReceived = "received"
	CallStatusAnswered = "answered"
	CallStatusEnded    = "ended"
	// CallStatusDeclined is an outbound call the callee cut while it was still
	// ringing: it never connected, so it is not "ended" — nothing was said.
	CallStatusDeclined = "declined"
	CallStatusFailed   = "failed"
)

// Call direction/type values, mirroring the calls.call_type column.
const (
	CallTypeInbound  = "inbound"
	CallTypeOutbound = "outbound"
)

// CallStatuses returns the allowed call statuses in schema order, for
// validation and API documentation.
func CallStatuses() []string {
	return []string{CallStatusReceived, CallStatusAnswered, CallStatusEnded, CallStatusDeclined, CallStatusFailed}
}

// IsValidCallStatus reports whether status is one of the allowed call statuses.
func IsValidCallStatus(status string) bool {
	switch status {
	case CallStatusReceived, CallStatusAnswered, CallStatusEnded, CallStatusDeclined, CallStatusFailed:
		return true
	default:
		return false
	}
}

// Call is one voice call the server handled, persisted in the calls table.
// PhoneNumberID and AgentID are nullable (the number or agent may have been
// deleted since); AnsweredAt, EndedAt, EndReason, and DurationSeconds stay unset
// until the relevant point in the call's lifecycle. Messages holds the saved
// conversation transcript; it is populated by the Get endpoint and omitted from
// list responses.
//
// CampaignID and LeadID are set only on a call an outbound campaign placed, and
// are what makes a campaign's own call history readable; both are nullable
// because most calls belong to no campaign.
type Call struct {
	ID              string        `json:"id" example:"9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"`
	CallID          string        `json:"call_id" example:"C7A1B2C3D4E5F6"`
	UserID          string        `json:"-"`
	PhoneNumberID   *string       `json:"phone_number_id" example:"phone_number_12345"`
	AgentID         *string       `json:"agent_id" example:"agent_12345"`
	CampaignID      *string       `json:"campaign_id" example:"campaign_a456426614174000"`
	LeadID          *string       `json:"lead_id" example:"lead_b7218f2265ca4000"`
	Peer            string        `json:"peer" example:"15557654321@s.whatsapp.net"`
	CallType        string        `json:"call_type" example:"inbound"`
	Status          string        `json:"status" example:"ended"`
	EndReason       *string       `json:"end_reason" example:"caller hung up"`
	DurationSeconds *int          `json:"duration_seconds" example:"42"`
	AnsweredAt      *time.Time    `json:"answered_at" example:"2026-07-24T10:00:05Z"`
	EndedAt         *time.Time    `json:"ended_at" example:"2026-07-24T10:00:47Z"`
	CreatedAt       time.Time     `json:"created_at" example:"2026-07-24T10:00:00Z"`
	UpdatedAt       time.Time     `json:"updated_at" example:"2026-07-24T10:00:47Z"`
	Messages        []CallMessage `json:"messages,omitempty"`
}

// NewCall is the data captured when a call is first recorded — an inbound
// WhatsApp call as it is answered by an agent, or an outbound call once its
// offer is on the wire — used to insert its initial row in the calls table.
// PhoneNumberID and AgentID are optional (stored as NULL when empty); UserID,
// CallID, and CallType are always set. CallType is CallTypeInbound or
// CallTypeOutbound. CampaignID and LeadID are set only when an outbound campaign
// placed the call, and are likewise stored as NULL when empty.
type NewCall struct {
	CallID        string
	UserID        string
	PhoneNumberID string
	AgentID       string
	CampaignID    string
	LeadID        string
	Peer          string
	CallType      string
}

// CallMessage is one turn in a call's conversation transcript, persisted to the
// call_messages table after the call ends. Role is "assistant" (the AI agent)
// or "user" (the caller); Content is the spoken text of that turn. The turn's
// order within the call is the message's position in the slice passed to
// SaveTranscript.
type CallMessage struct {
	Role    string `json:"role" example:"assistant"`
	Content string `json:"content" example:"Hello! How can I help you today?"`
}
