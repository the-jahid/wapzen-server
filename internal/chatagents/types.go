package chatagents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrPhoneNumberRequired = errors.New("assign a phone number before activating the chat agent")

// DefaultProvider and DefaultModel are not just defaults: they are the only
// model a chat agent may use. Requests naming anything else are rejected, and
// the incoming-message path answers with this model whatever a row says.
const (
	DefaultStatus       = "inactive"
	DefaultProvider     = "anthropic"
	DefaultModel        = "claude-sonnet-4-6"
	DefaultTemperature  = 0.3
	DefaultSystemPrompt = "You are a helpful, friendly WhatsApp chat assistant."
)

// OptionalString distinguishes an omitted PATCH property from an explicit
// JSON null, which clears nullable settings such as phone_number_id.
type OptionalString struct {
	Set   bool
	Value *string
}

func (o *OptionalString) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type AgentSection struct {
	Name          string  `json:"name"`
	PhoneNumberID *string `json:"phone_number_id"`
	Status        string  `json:"status"`
}

type ModelSection struct {
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Temperature float64 `json:"temperature"`
}

type PromptSection struct {
	SystemPrompt string `json:"system_prompt"`
}

type AttachmentSection struct {
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

type ToolSection struct {
	ToolIDs []string `json:"tool_ids"`
}

type Resource struct {
	ID            string            `json:"id"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Agent         AgentSection      `json:"agent"`
	Model         ModelSection      `json:"model"`
	Prompt        PromptSection     `json:"prompt"`
	KnowledgeBase AttachmentSection `json:"knowledge_base"`
	Tools         ToolSection       `json:"tools"`
	// ReplyLimitReached is true once the agent, or the number it answers on, has
	// sent all the replies it is allowed (see MaxRepliesPerAgent). Read-only.
	ReplyLimitReached bool `json:"reply_limit_reached"`
	// Runtime is filled by the handler from the running server, never stored.
	Runtime *RuntimeSection `json:"runtime,omitempty"`
}

// RuntimeSection reports whether the running server can answer as the agent:
// if its model provider has an API key here, and why its last WhatsApp reply
// failed (cleared once a reply goes through).
type RuntimeSection struct {
	ProviderConfigured bool       `json:"provider_configured"`
	LastError          string     `json:"last_error,omitempty"`
	LastErrorAt        *time.Time `json:"last_error_at,omitempty"`
}

type CreateRequest struct {
	Agent         *CreateAgentSection  `json:"agent"`
	Model         *CreateModelSection  `json:"model"`
	Prompt        *CreatePromptSection `json:"prompt"`
	KnowledgeBase *AttachmentSection   `json:"knowledge_base"`
	Tools         *ToolSection         `json:"tools"`
}

type CreateAgentSection struct {
	Name          string  `json:"name"`
	PhoneNumberID *string `json:"phone_number_id"`
	Status        string  `json:"status"`
}

type CreateModelSection struct {
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	Temperature *float64 `json:"temperature"`
}

type CreatePromptSection struct {
	SystemPrompt string `json:"system_prompt"`
}

type UpdateRequest struct {
	Agent         *UpdateAgentSection      `json:"agent"`
	Model         *UpdateModelSection      `json:"model"`
	Prompt        *UpdatePromptSection     `json:"prompt"`
	KnowledgeBase *UpdateAttachmentSection `json:"knowledge_base"`
	Tools         *UpdateToolSection       `json:"tools"`
}

type UpdateAgentSection struct {
	Name          *string        `json:"name"`
	PhoneNumberID OptionalString `json:"phone_number_id"`
	Status        *string        `json:"status"`
}

type UpdateModelSection struct {
	Provider    *string  `json:"provider"`
	Name        *string  `json:"name"`
	Temperature *float64 `json:"temperature"`
}

type UpdatePromptSection struct {
	SystemPrompt *string `json:"system_prompt"`
}

type UpdateAttachmentSection struct {
	KnowledgeBaseIDs *[]string `json:"knowledge_base_ids"`
}

type UpdateToolSection struct {
	ToolIDs *[]string `json:"tool_ids"`
}

// LiveAgent is the configuration needed by the incoming-message path.
type LiveAgent struct {
	ID               string
	ModelProvider    string
	ModelName        string
	ModelTemperature float64
	SystemPrompt     string
}

func ValidateCreate(req CreateRequest) error {
	if req.Agent == nil || strings.TrimSpace(req.Agent.Name) == "" {
		return fmt.Errorf("agent.name is required")
	}
	if req.Agent != nil {
		if err := validateAgent(req.Agent.Status); err != nil {
			return err
		}
		if err := validateActiveHasPhone(defaultString(req.Agent.Status, DefaultStatus), req.Agent.PhoneNumberID); err != nil {
			return err
		}
	}
	if req.Model != nil {
		if err := validateModel(req.Model.Provider, req.Model.Name, req.Model.Temperature); err != nil {
			return err
		}
	}
	if req.Prompt != nil {
		if err := validatePrompt(req.Prompt.SystemPrompt); err != nil {
			return err
		}
	}
	return nil
}

func validateActiveHasPhone(status string, phoneNumberID *string) error {
	if status == "active" && (phoneNumberID == nil || strings.TrimSpace(*phoneNumberID) == "") {
		return ErrPhoneNumberRequired
	}
	return nil
}

func ValidateUpdate(req UpdateRequest) error {
	hasChanges := false
	if a := req.Agent; a != nil {
		hasChanges = a.Name != nil || a.PhoneNumberID.Set || a.Status != nil
	}
	if m := req.Model; m != nil {
		hasChanges = hasChanges || m.Provider != nil || m.Name != nil || m.Temperature != nil
	}
	if p := req.Prompt; p != nil {
		hasChanges = hasChanges || p.SystemPrompt != nil
	}
	hasChanges = hasChanges || (req.KnowledgeBase != nil && req.KnowledgeBase.KnowledgeBaseIDs != nil)
	hasChanges = hasChanges || (req.Tools != nil && req.Tools.ToolIDs != nil)
	if !hasChanges {
		return fmt.Errorf("at least one setting is required")
	}
	if a := req.Agent; a != nil {
		if a.Name != nil && strings.TrimSpace(*a.Name) == "" {
			return fmt.Errorf("agent.name cannot be empty")
		}
		if a.Status != nil && !oneOf(*a.Status, "active", "inactive") {
			return fmt.Errorf("agent.status must be active or inactive")
		}
	}
	if m := req.Model; m != nil {
		if err := validateModel(deref(m.Provider), deref(m.Name), m.Temperature); err != nil {
			return err
		}
	}
	if p := req.Prompt; p != nil {
		if p.SystemPrompt != nil && strings.TrimSpace(*p.SystemPrompt) == "" {
			return fmt.Errorf("prompt.system_prompt cannot be empty")
		}
	}
	return nil
}

func validateAgent(status string) error {
	if status != "" && !oneOf(status, "active", "inactive") {
		return fmt.Errorf("agent.status must be active or inactive")
	}
	return nil
}

// validateModel accepts the fixed model only; provider and name may be omitted
// but cannot name anything else.
func validateModel(provider, name string, temperature *float64) error {
	if provider != "" && provider != DefaultProvider {
		return fmt.Errorf("model.provider must be %s", DefaultProvider)
	}
	if name != "" && strings.TrimSpace(name) != DefaultModel {
		return fmt.Errorf("model.name must be %s", DefaultModel)
	}
	if temperature != nil && (*temperature < 0.1 || *temperature > 1) {
		return fmt.Errorf("model.temperature must be between 0.1 and 1")
	}
	return nil
}

func validatePrompt(system string) error {
	if system != "" && strings.TrimSpace(system) == "" {
		return fmt.Errorf("prompt.system_prompt cannot be empty")
	}
	return nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
