package models

import "time"

// User is the application-owned record linked to a Clerk user by OAuthID.
type User struct {
	ID        string    `json:"id" example:"9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"`
	Email     string    `json:"email" example:"user@example.com"`
	OAuthID   string    `json:"oauthId" example:"user_2abc123"`
	Username  *string   `json:"username,omitempty" example:"janedoe"`
	CreatedAt time.Time `json:"createdAt" example:"2026-06-30T10:00:00Z"`
	UpdatedAt time.Time `json:"updatedAt" example:"2026-06-30T10:00:00Z"`
}
