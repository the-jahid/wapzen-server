package models

import (
	"regexp"
	"strings"
	"time"
)

// Tool types, mirroring the tools.type CHECK constraint. The type decides which
// configuration block on the resource is read; everything else is ignored for
// that tool.
const (
	// ToolTypeAPIRequest calls an HTTP endpoint mid-call and hands the response
	// back to the model, which speaks the useful part of it to the caller.
	ToolTypeAPIRequest = "api_request"
	// ToolTypeTransferCall announces the handover and releases the caller.
	// WhatsApp calling has no bridge primitive, so this cannot splice a second
	// leg onto the live one; see ToolTransferCallConfig.
	ToolTypeTransferCall = "transfer_call"
	// ToolTypeEndCall hangs up.
	ToolTypeEndCall = "end_call"
	// ToolTypeSendText sends the caller a WhatsApp message during the call, for
	// the things a phone call is bad at carrying: a link, an address, a code.
	ToolTypeSendText = "send_text"
)

// ToolTypes lists every tool type, in the order the dashboard offers them.
func ToolTypes() []string {
	return []string{ToolTypeAPIRequest, ToolTypeTransferCall, ToolTypeEndCall, ToolTypeSendText}
}

// IsValidToolType reports whether toolType is one of the stored tool types.
func IsValidToolType(toolType string) bool {
	switch toolType {
	case ToolTypeAPIRequest, ToolTypeTransferCall, ToolTypeEndCall, ToolTypeSendText:
		return true
	default:
		return false
	}
}

// ToolHTTPMethods are the verbs an api_request tool may use.
func ToolHTTPMethods() []string {
	return []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
}

// IsValidToolHTTPMethod reports whether method is one an api_request tool may
// use. It is compared already upper-cased; NormalizeToolMethod does that.
func IsValidToolHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// ToolParameterTypes are the JSON types a tool parameter may declare. They are
// the types a model can reliably fill in from a spoken conversation, which is
// why there is no object or array here.
func ToolParameterTypes() []string {
	return []string{"string", "number", "boolean"}
}

// IsValidToolParameterType reports whether paramType is a declarable type.
func IsValidToolParameterType(paramType string) bool {
	switch paramType {
	case "string", "number", "boolean":
		return true
	default:
		return false
	}
}

// Bounds mirroring the tools CHECK constraints and the documented contract. The
// timeout ceiling is low on purpose: the caller sits in silence while a blocking
// request runs, so a slow endpoint has to become "I cannot look that up"
// quickly rather than holding the turn open.
const (
	ToolMaxNameLength          = 64
	ToolMaxDescriptionLength   = 1000
	ToolDefaultTimeoutSeconds  = 20
	ToolMinTimeoutSeconds      = 1
	ToolMaxTimeoutSeconds      = 60
	ToolMaxHeaders             = 20
	ToolMaxParameters          = 20
	ToolMaxParameterNameLength = 64
	ToolMaxURLLength           = 2000
	ToolMaxMessageLength       = 1000
)

// toolNamePattern mirrors the tools.tool_name CHECK constraint: the
// intersection of what the OpenAI and Anthropic function-calling APIs accept as
// a function name. Validated here so a bad name reads as a field error rather
// than as a constraint violation turned 500.
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// IsValidToolName reports whether name can be used as a model-callable function
// name.
func IsValidToolName(name string) bool {
	return toolNamePattern.MatchString(name)
}

// ToolParameter is one argument the model fills in when it calls the tool. The
// description is prompt text — it is the only thing telling the model what to
// put there — so an empty one is the usual reason a tool gets called with
// nonsense arguments.
type ToolParameter struct {
	Name        string `json:"name" example:"date"`
	Type        string `json:"type" example:"string"`
	Description string `json:"description" example:"Requested day in YYYY-MM-DD form."`
	Required    bool   `json:"required" example:"true"`
}

// ToolHeader is one outgoing HTTP header on an api_request tool.
type ToolHeader struct {
	Key   string `json:"key" example:"Authorization"`
	Value string `json:"value" example:"Bearer sk-live-..."`
}

// ToolAPIRequestConfig is the configuration read when Type is api_request: the
// endpoint the call reaches out to, and the arguments the model has to produce
// before it can.
//
// Async fires the request and keeps talking. It is the right setting for a
// write the caller does not have to hear the result of (logging a callback
// request, say), and the wrong one for a lookup, since the response is
// discarded rather than spoken.
type ToolAPIRequestConfig struct {
	Method         string          `json:"method" example:"GET"`
	URL            string          `json:"url" example:"https://api.example.com/v1/availability"`
	TimeoutSeconds int             `json:"timeout_seconds" example:"20"`
	Async          bool            `json:"async" example:"false"`
	Headers        []ToolHeader    `json:"headers"`
	Parameters     []ToolParameter `json:"parameters"`
}

// ToolTransferCallConfig is the configuration read when Type is transfer_call.
//
// WhatsApp calling exposes no way to bridge a second leg onto a live call, so a
// transfer here is an announcement followed by a hangup: the agent says Message
// and releases the caller. Destination is recorded and logged so the handover is
// auditable, and so the same stored tool keeps working unchanged if a bridge
// primitive ever arrives.
type ToolTransferCallConfig struct {
	Destination string `json:"destination" example:"+8801639726992"`
	Message     string `json:"message" example:"Connecting you to a teammate now, one moment."`
}

// ToolSendTextConfig is the configuration read when Type is send_text. An empty
// Body lets the model write the message itself, in which case it arrives as a
// tool argument at call time.
type ToolSendTextConfig struct {
	Body string `json:"body" example:"Here is the booking link we just talked about: https://example.com/book"`
}

// ToolEndCallConfig is the configuration read when Type is end_call. Message is
// what the agent says before hanging up; empty means it hangs up on whatever it
// has just finished saying.
type ToolEndCallConfig struct {
	Message string `json:"message" example:"Thanks for calling, goodbye."`
}

// Tool is one action an agent can take during a call, as stored in the tools
// table. Exactly one configuration block is populated, chosen by Type; the rest
// are nil and omitted from responses, so a resource never advertises settings
// that have no effect on it.
type Tool struct {
	ID          string `json:"id" example:"tool_12345"`
	UserID      string `json:"-"`
	Type        string `json:"type" example:"api_request"`
	Name        string `json:"name" example:"check_availability"`
	Description string `json:"description" example:"Look up open appointment slots for a given day."`
	// AgentIDs and ChatAgentIDs are the agents this tool is attached to, empty
	// while it is attached to none. A tool is shared: the same definition can be
	// attached to any number of agents of either kind. Read-only here: a tool is
	// attached by writing that agent's tools.tool_ids, so the fact has one write
	// path rather than two that can disagree. They are returned so a client can
	// see where a tool is in use before it changes or deletes one.
	AgentIDs     []string                `json:"agent_ids"`
	ChatAgentIDs []string                `json:"chat_agent_ids"`
	APIRequest   *ToolAPIRequestConfig   `json:"api_request,omitempty"`
	TransferCall *ToolTransferCallConfig `json:"transfer_call,omitempty"`
	SendText     *ToolSendTextConfig     `json:"send_text,omitempty"`
	EndCall      *ToolEndCallConfig      `json:"end_call,omitempty"`
	CreatedAt    time.Time               `json:"created_at" example:"2026-08-24T10:00:00Z"`
	UpdatedAt    time.Time               `json:"updated_at" example:"2026-08-24T10:00:00Z"`
}

// NewTool is the data captured when a tool is created. Only the block matching
// Type is read; the others are ignored even when supplied, so a request cannot
// smuggle a transfer destination onto an api_request tool.
type NewTool struct {
	UserID       string
	Type         string
	Name         string
	Description  string
	APIRequest   *ToolAPIRequestConfig
	TransferCall *ToolTransferCallConfig
	SendText     *ToolSendTextConfig
	EndCall      *ToolEndCallConfig
}

// ToolUpdate is a partial update of a tool. Every field is a pointer: a nil one
// is left untouched, so a caller renaming a tool never has to restate its
// configuration. Type is absent because it is fixed at creation — changing it
// would leave the stored configuration block describing a different action.
//
// A supplied configuration block replaces the stored one wholesale. Headers and
// parameters are arrays whose rows have no identity of their own, so there is no
// row-by-row merge a caller could predict.
type ToolUpdate struct {
	Name         *string
	Description  *string
	APIRequest   *ToolAPIRequestConfig
	TransferCall *ToolTransferCallConfig
	SendText     *ToolSendTextConfig
	EndCall      *ToolEndCallConfig
}

// IsEmpty reports whether the update would change nothing, which callers reject
// rather than issuing an UPDATE with no assignments.
func (u ToolUpdate) IsEmpty() bool {
	return u.Name == nil && u.Description == nil && u.APIRequest == nil &&
		u.TransferCall == nil && u.SendText == nil && u.EndCall == nil
}

// Config returns the tool's populated configuration block, or nil for a tool
// whose type takes none. It is what the repository serializes into the config
// column, so the stored JSON always matches the type beside it.
func (t Tool) Config() any {
	switch t.Type {
	case ToolTypeAPIRequest:
		if t.APIRequest != nil {
			return t.APIRequest
		}
	case ToolTypeTransferCall:
		if t.TransferCall != nil {
			return t.TransferCall
		}
	case ToolTypeSendText:
		if t.SendText != nil {
			return t.SendText
		}
	case ToolTypeEndCall:
		if t.EndCall != nil {
			return t.EndCall
		}
	}
	return nil
}

// AgentTool is one tool an agent may call, reduced to what a live call needs:
// the function name the model calls, what it is told the function does, and the
// configuration the runtime executes. It is deliberately not the full Tool — a
// call resolves these on every offer, and the timestamps have no bearing on
// running the action.
type AgentTool struct {
	ID           string
	Type         string
	Name         string
	Description  string
	APIRequest   *ToolAPIRequestConfig
	TransferCall *ToolTransferCallConfig
	SendText     *ToolSendTextConfig
	EndCall      *ToolEndCallConfig
}

// NormalizeToolMethod upper-cases an api_request method and falls back to POST
// when none was supplied, matching the documented default.
func NormalizeToolMethod(method string) string {
	if trimmed := strings.ToUpper(strings.TrimSpace(method)); trimmed != "" {
		return trimmed
	}
	return "POST"
}
