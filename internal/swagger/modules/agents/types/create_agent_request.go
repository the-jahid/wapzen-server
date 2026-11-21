// Package types holds the Go shape of the static Agents payloads. These mirror
// the documented JSON contract and are used to type the example payloads, so
// the examples cannot drift from the field names/structure they illustrate.
//
// Every field here is backed by a real column in the agents schema (including
// later agent migrations): scalar fields are columns on `agents`,
// knowledge_base_ids is the set of `knowledge_bases` rows pointing back at the
// agent and tool_ids the set of `tools` rows doing the same. The docs
// intentionally advertise nothing the database does not persist.
package types

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"

// CreateAgentRequest is the grouped configuration object accepted by
// POST /v1/agents. Only the `agent` section is required.
type CreateAgentRequest struct {
	Agent         *AgentSection         `json:"agent,omitempty"`
	Prompt        *PromptSection        `json:"prompt,omitempty"`
	Voice         *VoiceSection         `json:"voice,omitempty"`
	PostCall      *PostCallSection      `json:"post_call,omitempty"`
	KnowledgeBase *KnowledgeBaseSection `json:"knowledge_base,omitempty"`
	Tools         *ToolsSection         `json:"tools,omitempty"`
}

// AgentSection is the only required section; name is its only required field.
type AgentSection struct {
	Name          string                   `json:"name"`
	Language      string                   `json:"language,omitempty"`
	Timezone      *string                  `json:"timezone"`
	PhoneNumberID *string                  `json:"phone_number_id"`
	Status        constants.ResourceStatus `json:"status,omitempty"`
}

// PromptSection configures the agent's prompting and conversational behavior.
type PromptSection struct {
	BeginMessageMode    constants.BeginMessageMode `json:"begin_message_mode,omitempty"`
	BeginMessage        string                     `json:"begin_message,omitempty"`
	BeginMessageDelayMs int                        `json:"begin_message_delay_ms,omitempty"`
	SystemPrompt        string                     `json:"system_prompt,omitempty"`
}

// VoiceSection holds the agent's OpenAI Realtime voice. Realtime is the only
// live-call engine, so there is no provider to choose.
type VoiceSection struct {
	OpenAI *OpenAIVoice `json:"openai,omitempty"`
}

// OpenAIVoice holds the OpenAI Realtime settings used for calls.
type OpenAIVoice struct {
	VoiceID       constants.OpenAIVoiceID       `json:"voice_id,omitempty"`
	VoiceName     string                        `json:"voice_name,omitempty"`
	RealtimeModel constants.OpenAIRealtimeModel `json:"realtime_model,omitempty"`
	Speed         float64                       `json:"speed,omitempty"`
	Volume        float64                       `json:"volume,omitempty"`
}

// PostCallSection configures the scalar post-call analysis settings persisted
// directly on agents. The former list of extraction fields is intentionally not
// exposed because it is not part of the current schema.
type PostCallSection struct {
	AnalysisProvider constants.LLMProvider `json:"analysis_provider,omitempty"`
	AnalysisModel    *string               `json:"analysis_model"`
}

// KnowledgeBaseSection lists the knowledge bases the agent may answer from. The
// ids reference knowledge bases owned by the same user; the agent quotes their
// indexed sources during a call when the prompt does not cover what was asked.
//
// The list is the whole attachment set: sending it replaces the agent's current
// attachments, and an empty list detaches every knowledge base.
type KnowledgeBaseSection struct {
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

// ToolsSection lists the tools the agent may call during a call. The ids
// reference tools owned by the same user; the model is offered them as callable
// functions for the length of every call this agent answers or places.
//
// The list is the whole attachment set: sending it replaces the agent's current
// attachments, and an empty list detaches every tool.
type ToolsSection struct {
	ToolIDs []string `json:"tool_ids"`
}
