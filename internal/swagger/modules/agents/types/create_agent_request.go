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
	Model         *ModelSection         `json:"model,omitempty"`
	Prompt        *PromptSection        `json:"prompt,omitempty"`
	Voice         *VoiceSection         `json:"voice,omitempty"`
	Transcriber   *TranscriberSection   `json:"transcriber,omitempty"`
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
	CallDirection constants.CallDirection  `json:"call_direction,omitempty"`
	Status        constants.ResourceStatus `json:"status,omitempty"`
}

// ModelSection configures the conversational LLM.
type ModelSection struct {
	Provider    constants.LLMProvider `json:"provider,omitempty"`
	Name        string                `json:"name,omitempty"`
	Temperature float64               `json:"temperature,omitempty"`
}

// PromptSection configures the agent's prompting and conversational behavior.
type PromptSection struct {
	BeginMessageMode    constants.BeginMessageMode `json:"begin_message_mode,omitempty"`
	BeginMessage        string                     `json:"begin_message,omitempty"`
	BeginMessageDelayMs int                        `json:"begin_message_delay_ms,omitempty"`
	SystemPrompt        string                     `json:"system_prompt,omitempty"`
}

// VoiceSection selects the live-call pipeline and its provider-specific voice.
type VoiceSection struct {
	Provider   constants.VoiceProvider `json:"provider,omitempty"`
	ElevenLabs *ElevenLabsVoice        `json:"elevenlabs,omitempty"`
	OpenAI     *OpenAIVoice            `json:"openai,omitempty"`
}

// ElevenLabsVoice holds ElevenLabs-specific voice selection. voice_id,
// voice_name and voice_model are required.
type ElevenLabsVoice struct {
	VoiceID    string               `json:"voice_id,omitempty"`
	VoiceName  string               `json:"voice_name,omitempty"`
	VoiceModel constants.VoiceModel `json:"voice_model,omitempty"`
}

// OpenAIVoice holds the OpenAI Audio Speech settings used for calls.
type OpenAIVoice struct {
	VoiceID       constants.OpenAIVoiceID       `json:"voice_id,omitempty"`
	VoiceName     string                        `json:"voice_name,omitempty"`
	VoiceModel    constants.VoiceModel          `json:"voice_model,omitempty"`
	RealtimeModel constants.OpenAIRealtimeModel `json:"realtime_model,omitempty"`
	Instructions  *string                       `json:"instructions"`
	Speed         float64                       `json:"speed,omitempty"`
	Volume        float64                       `json:"volume,omitempty"`
}

// TranscriberSection configures speech-to-text. The concrete model lives in the
// provider-specific sub-object.
type TranscriberSection struct {
	Provider   constants.TranscriberProvider `json:"provider,omitempty"`
	Language   string                        `json:"language,omitempty"`
	OpenAI     *TranscriberOpenAI            `json:"openai,omitempty"`
	ElevenLabs *TranscriberElevenLabs        `json:"elevenlabs,omitempty"`
}

// TranscriberOpenAI holds the OpenAI-specific transcription model.
type TranscriberOpenAI struct {
	Model constants.TranscriberModel `json:"model,omitempty"`
}

// TranscriberElevenLabs holds the ElevenLabs-specific transcription model.
type TranscriberElevenLabs struct {
	Model constants.TranscriberModel `json:"model,omitempty"`
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
