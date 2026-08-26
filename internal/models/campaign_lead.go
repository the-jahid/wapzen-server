package models

import "time"

const (
	CampaignLeadStatusPending = "pending"
	CampaignLeadStatusCalling = "calling"
	CampaignLeadStatusCalled  = "called"
	CampaignLeadStatusFailed  = "failed"
	CampaignLeadMaxNameLength = 80
)

func CampaignLeadStatuses() []string {
	return []string{CampaignLeadStatusPending, CampaignLeadStatusCalling, CampaignLeadStatusCalled, CampaignLeadStatusFailed}
}

func IsValidCampaignLeadStatus(status string) bool {
	switch status {
	case CampaignLeadStatusPending, CampaignLeadStatusCalling, CampaignLeadStatusCalled, CampaignLeadStatusFailed:
		return true
	default:
		return false
	}
}

type CampaignLead struct {
	ID              string     `json:"lead_id"`
	CampaignID      string     `json:"campaign_id"`
	PhoneNumber     string     `json:"phone_number"`
	Email           *string    `json:"email"`
	FirstName       *string    `json:"first_name"`
	LastName        *string    `json:"last_name"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	LastAttemptedAt *time.Time `json:"last_attempted_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type NewCampaignLead struct {
	CampaignID  string
	PhoneNumber string
	Email       *string
	FirstName   *string
	LastName    *string
}

type CampaignLeadUpdate struct {
	PhoneNumber string
	SetPhone    bool
	Email       *string
	SetEmail    bool
	FirstName   *string
	SetFirst    bool
	LastName    *string
	SetLast     bool
	Status      string
	SetStatus   bool
}

func (u CampaignLeadUpdate) IsEmpty() bool {
	return !u.SetPhone && !u.SetEmail && !u.SetFirst && !u.SetLast && !u.SetStatus
}
