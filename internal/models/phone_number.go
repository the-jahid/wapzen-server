package models

import "time"

// Phone number login lifecycle statuses.
const (
	PhoneNumberStatusPendingQR    = "pending_qr"
	PhoneNumberStatusConnected    = "connected"
	PhoneNumberStatusDisconnected = "disconnected"
)

// PhoneNumber is a WhatsApp device login owned by one application user.
type PhoneNumber struct {
	ID              string     `json:"id" example:"phone_number_12345"`
	UserID          string     `json:"-"`
	PhoneNumber     *string    `json:"phone_number" example:"+15551234567"`
	Label           *string    `json:"label" example:"Main support line"`
	WAJID           *string    `json:"wa_jid" example:"15551234567:1@s.whatsapp.net"`
	Status          string     `json:"status" example:"pending_qr"`
	QRCode          *string    `json:"qr_code"`
	LastConnectedAt *time.Time `json:"last_connected_at" example:"2026-07-03T10:00:00Z"`
	CreatedAt       time.Time  `json:"created_at" example:"2026-07-03T10:00:00Z"`
	UpdatedAt       time.Time  `json:"updated_at" example:"2026-07-03T10:00:00Z"`
}
