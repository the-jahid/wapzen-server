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
	ID          string  `json:"id" example:"phone_number_12345"`
	UserID      string  `json:"-"`
	PhoneNumber *string `json:"phone_number" example:"+15551234567"`
	Label       *string `json:"label" example:"Main support line"`
	WAJID       *string `json:"wa_jid" example:"15551234567:1@s.whatsapp.net"`
	Status      string  `json:"status" example:"pending_qr"`
	QRCode      *string `json:"qr_code"`
	// QRExpiresInMs is how long the QR code in qr_code stays scannable, counted
	// from when this response was built: WhatsApp gives the first code 60s and
	// each later one 20s. A duration rather than a timestamp, so the page's
	// countdown is right even when the browser's clock is not. Only set on an
	// unscanned login.
	QRExpiresInMs   *int64     `json:"qr_expires_in_ms,omitempty" example:"17000"`
	LastConnectedAt *time.Time `json:"last_connected_at" example:"2026-07-03T10:00:00Z"`
	CreatedAt       time.Time  `json:"created_at" example:"2026-07-03T10:00:00Z"`
	UpdatedAt       time.Time  `json:"updated_at" example:"2026-07-03T10:00:00Z"`

	// PairingCode is the 8-character code a user types into WhatsApp (Linked
	// devices › Link with phone number instead) when they cannot scan the QR
	// code, such as when the dashboard is open on the phone itself. It is only
	// set on an unscanned login that asked for one, and is never stored.
	PairingCode *string `json:"pairing_code,omitempty" example:"ABCD-EFGH"`
}
