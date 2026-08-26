package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// ValidationErrorExample is a 400 returned when the request body fails schema
// validation.
var ValidationErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Validation failed",
	Errors: []types.FieldError{
		{Field: "agent.name", Message: "name is required"},
		{Field: "model.temperature", Message: "temperature must be between 0.1 and 1"},
	},
}

// ConflictErrorExample is a 409 returned when a unique name or phone-number
// direction assignment is already in use.
var ConflictErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Conflict",
	Errors: []types.FieldError{
		{Field: "agent.phone_number_id", Message: "phone number is already assigned to another agent for this call direction"},
	},
}

// UnauthorizedErrorExample is a 401 returned when the bearer token is missing or
// invalid.
var UnauthorizedErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Authentication required",
	Errors: []types.FieldError{
		{Field: "authorization", Message: "Missing or invalid bearer token"},
	},
}

// UnprocessableErrorExample is a 422 returned when the request is well-formed but
// semantically invalid.
var UnprocessableErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Unprocessable entity",
	Errors: []types.FieldError{
		{Field: "voice.elevenlabs.voice_id", Message: "voice_id not available for the selected provider"},
	},
}

// NotFoundErrorExample is a 404 returned when no agent matches the id.
var NotFoundErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Agent not found",
	Errors: []types.FieldError{
		{Field: "agent_id", Message: "No agent exists with id agent_99999"},
	},
}

// TooManyRequestsErrorExample is a 429 returned when the client is rate limited.
var TooManyRequestsErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Rate limit exceeded. Retry after 30 seconds.",
	Errors:  []types.FieldError{},
}

// InternalErrorExample is a 500 returned for unexpected server errors.
var InternalErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Internal server error",
	Errors:  []types.FieldError{},
}

// ListQueryValidationErrorExample is a 400 returned when list query parameters
// (page / limit / fields) are invalid.
var ListQueryValidationErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Invalid query parameters",
	Errors: []types.FieldError{
		{Field: "limit", Message: "limit must be between 1 and 100"},
		{Field: "fields", Message: "unknown field 'agent.unknown' in fields selection"},
	},
}

// AgentIDValidationErrorExample is a 400 returned when the agent_id path
// parameter is malformed.
var AgentIDValidationErrorExample = types.ErrorEnvelope{
	Success: false,
	Message: "Invalid path parameter",
	Errors: []types.FieldError{
		{Field: "agent_id", Message: "agent_id must be a non-empty string"},
	},
}
