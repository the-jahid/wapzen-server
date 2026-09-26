package agents

import (
	"errors"
	"fmt"
)

// Domain errors returned by the service and repository. The controller maps
// each to an HTTP status (see writeError) and surfaces err.Error() as the
// message, so the text here is what an API client reads.
var (
	// ErrAgentNotFound is returned when no agent matches the requested id —
	// including when the agent exists but belongs to another user, so ownership
	// never leaks.
	ErrAgentNotFound = errors.New("agent not found")

	// ErrAgentNameTaken is returned when the user already has an agent with the
	// requested name (names are unique per owner).
	ErrAgentNameTaken = errors.New("an agent with this name already exists")

	// ErrUserNotFound is returned when the owning user row disappeared mid-request.
	ErrUserNotFound = errors.New("user not found")

	// ErrPhoneNumberNotFound is returned when an agent is assigned to a phone
	// number that does not exist or is not owned by the authenticated user.
	ErrPhoneNumberNotFound = errors.New("phone number not found")

	// ErrPhoneNumberAssignmentConflict is returned when another agent already
	// uses the requested phone number.
	ErrPhoneNumberAssignmentConflict = errors.New("phone number is already assigned to another agent for this call direction")

	// ErrKnowledgeBaseNotFound is returned when an agent is attached to a
	// knowledge base that does not exist or is not owned by the authenticated
	// user. The offending id is appended, since a request may carry several.
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")

	// ErrKnowledgeBaseAttachmentConflict is returned when an agent is attached to
	// a knowledge base another agent already owns. A knowledge base belongs to one
	// agent, so honouring the request would take it away from that agent
	// mid-conversation; it has to be detached there first.
	ErrKnowledgeBaseAttachmentConflict = errors.New("knowledge base is already attached to another agent")

	// ErrToolNotFound is returned when an agent is attached to a tool that does
	// not exist or is not owned by the authenticated user. The offending id is
	// appended, since a request may carry several.
	ErrToolNotFound = errors.New("tool not found")
)

// ValidationError is a request the caller has to fix: a missing required field,
// a malformed PATCH body, a value the schema rejects, or a state the business
// rules forbid (such as going live without a phone number).
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}
