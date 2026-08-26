package types

// AgentResource is a persisted agent: the server-managed fields plus an echo of
// the submitted configuration (the embedded CreateAgentRequest is promoted, so
// its sections appear alongside id/timestamps). Lifecycle status lives on the
// agent section (agent.status), mirroring the database layout.
type AgentResource struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	CreateAgentRequest
}

// SuccessEnvelope is the standard success response wrapper. Meta and Links are
// optional so the same type serves single-resource and list responses.
type SuccessEnvelope struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    any    `json:"data"`
	Meta    *Meta  `json:"meta,omitempty"`
	Links   any    `json:"links,omitempty"`
}

// ResourceLinks is the HATEOAS link block for a single resource.
type ResourceLinks struct {
	Self string `json:"self"`
}

// ListLinks is the HATEOAS link block for a paginated collection. Previous and
// Next are nullable and intentionally rendered (no omitempty) so `null` shows.
type ListLinks struct {
	Self     string  `json:"self"`
	First    string  `json:"first"`
	Previous *string `json:"previous"`
	Next     *string `json:"next"`
	Last     string  `json:"last"`
}

// Meta carries collection metadata.
type Meta struct {
	Pagination Pagination `json:"pagination"`
}

// Pagination describes the current page of a collection.
type Pagination struct {
	Page            int  `json:"page"`
	Limit           int  `json:"limit"`
	TotalItems      int  `json:"total_items"`
	TotalPages      int  `json:"total_pages"`
	HasNextPage     bool `json:"has_next_page"`
	HasPreviousPage bool `json:"has_previous_page"`
}

// DeleteData is the data block returned by a successful delete.
type DeleteData struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// FieldError is a single field-level validation error.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ErrorEnvelope is the standard error response wrapper.
type ErrorEnvelope struct {
	Success bool         `json:"success"`
	Message string       `json:"message"`
	Errors  []FieldError `json:"errors"`
}
