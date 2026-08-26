package models

import "time"

// APIKey is the public metadata for a user-owned API key. The secret is only
// returned once when a key is created and is never loaded back from storage.
type APIKey struct {
	ID         string     `json:"id" example:"8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f"`
	Name       string     `json:"name" example:"Production"`
	IsDefault  bool       `json:"isDefault" example:"true"`
	KeyPrefix  string     `json:"keyPrefix" example:"wcai_Bv6f"`
	Last4      string     `json:"last4" example:"9xQ2"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty" example:"2026-07-02T10:00:00Z"`
	CreatedAt  time.Time  `json:"createdAt" example:"2026-07-02T10:00:00Z"`
	UpdatedAt  time.Time  `json:"updatedAt" example:"2026-07-02T10:00:00Z"`
}

// CreatedAPIKey is returned by key creation endpoints. Key is the only time the
// bearer secret is exposed to clients.
type CreatedAPIKey struct {
	APIKey
	Key string `json:"key" example:"wcai_Bv6fLwL8y6bpiGvM_rJX8oBsZw8QSg6DSxQ7VhA_9xQ2"`
}
