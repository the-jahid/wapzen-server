package agents

import (
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// ---------------------------------------------------------------------------
// Request body — the grouped CreateAgentRequest object.
//
// createAgentRequestProperties builds a *fresh* properties map on every call so
// the derived schemas (update / resource) can mutate their own copy without
// aliasing the create schema.
// ---------------------------------------------------------------------------

func createAgentRequestProperties() oas.Object {
	root := oas.Obj().
		P("agent", agentSectionSchema()).
		P("model", modelSectionSchema()).
		P("prompt", promptSectionSchema()).
		P("voice", voiceSectionSchema()).
		P("transcriber", transcriberSectionSchema()).
		P("post_call", postCallSectionSchema()).
		P("knowledge_base", knowledgeBaseSectionSchema()).
		P("tools", toolsSectionSchema())
	return root.Properties()
}

func agentSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Core agent identity and routing. The only required section.").
		Req("name").
		P("name", oas.Str().Desc("Human-friendly display name for the agent.").Example("My Agent")).
		P("language", oas.Str().Desc("Primary language as a BCP-47 tag.").Default("en-US").Example("en-US")).
		P("timezone", oas.Str().Nullable().Desc("IANA timezone used for scheduling and timestamps.").Example(nil)).
		P("phone_number_id", oas.Str().Nullable().Desc("User-owned phone number assigned to this outbound agent. A phone number can be assigned to only one agent.").Example(nil)).
		P("call_direction", oas.Str().Desc("Allowed call direction for this agent.").
			Enum(constants.CallDirectionOptions).Default("outbound").Example("outbound")).
		P("status", oas.Str().Desc("Lifecycle status. Defaults to active when omitted.").
			Enum(constants.ResourceStatusOptions).Default("active").Example("active"))
}

func modelSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Conversational LLM configuration.").
		P("provider", oas.Str().Desc("LLM provider.").Enum(constants.LLMProviderOptions).Default("openai").Example("openai")).
		P("name", oas.Str().Desc("Provider-specific model identifier.").Default("gpt-4.1-mini").Example("gpt-4.1-mini")).
		P("temperature", oas.Num().Desc("Sampling temperature. Higher is more creative.").
			Min(0.1).Max(1).Default(0.3).Example(0.3))
}

func promptSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Prompting and conversational behavior.").
		P("begin_message_mode", oas.Str().Desc("Who speaks first and how the opening line is produced.").
			Enum(constants.BeginMessageModeOptions).Default("agent_speaks_first").Example("agent_speaks_first")).
		P("begin_message", oas.Str().Desc("The agent's opening line (when applicable).").
			Default("Hello! How can I help you today?").
			Example("Hello! How can I help you today?")).
		P("begin_message_delay_ms", oas.Int().Desc("Delay before the agent speaks first, in milliseconds.").
			Min(0).Max(5000).Default(1000).Example(1000)).
		P("system_prompt", oas.Str().Desc("System prompt that defines the agent's persona and instructions.").
			Default("You are a helpful, friendly voice assistant on a phone call. Keep responses clear and concise, speak naturally, and stay polite and professional at all times.").
			Example("You are a helpful, friendly voice assistant on a phone call. Keep responses clear and concise, speak naturally, and stay polite and professional at all times."))
}

func voiceSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Text-to-speech configuration.").
		P("provider", oas.Str().Desc("Live-call pipeline: native OpenAI Realtime, request-based OpenAI, or ElevenLabs.").Enum(constants.VoiceProviderOptions).Default("openai_realtime").Example("openai_realtime")).
		P("elevenlabs", oas.Obj().Desc("ElevenLabs-specific voice selection.").
			P("voice_id", oas.Str().Desc("ElevenLabs voice identifier.").Default("DODLEQrClDo8wCz460ld").Example("DODLEQrClDo8wCz460ld")).
			P("voice_name", oas.Str().Desc("Human-friendly display name of the selected voice.").Default("Lauren").Example("Lauren")).
			P("voice_model", oas.Str().Desc("ElevenLabs voice model.").Enum(constants.VoiceModelElevenLabsOptions).Default("eleven_flash_v2_5").Example("eleven_flash_v2_5"))).
		P("openai", oas.Obj().Desc("OpenAI-specific voice selection.").
			P("voice_id", oas.Str().Desc("OpenAI Audio Speech voice used for calls.").Enum(constants.OpenAIVoiceOptions).Default("alloy").Example("alloy")).
			P("voice_name", oas.Str().Desc("Human-friendly display name of the selected voice.").Default("Alloy").Example("Alloy")).
			P("voice_model", oas.Str().Desc("OpenAI Audio Speech model used for live calls.").Enum(constants.VoiceModelOpenAIOptions).Default("tts-1").Example("tts-1")).
			P("realtime_model", oas.Str().Desc("Native speech-to-speech model used when voice.provider is openai_realtime.").Enum(constants.OpenAIRealtimeModelOptions).Default("gpt-realtime-2.1-mini").Example("gpt-realtime-2.1-mini")).
			P("instructions", oas.Str().Nullable().Desc("Style/delivery instructions for the OpenAI voice.").Example(nil)).
			P("speed", oas.Num().Desc("Spoken rate; 1 is the model's normal pace.").Min(0.25).Max(1.5).Default(1).Example(1)).
			P("volume", oas.Num().Desc("Playback gain applied to the agent's audio; 1 leaves it unchanged.").Min(0).Max(2).Default(1).Example(1)))
}

func transcriberSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Speech-to-text configuration. The concrete model lives in the provider-specific sub-object.").
		P("provider", oas.Str().Desc("Transcriber provider.").Enum(constants.TranscriberProviderOptions).Default("openai").Example("openai")).
		P("language", oas.Str().Desc("Expected spoken language.").Default("en").Example("en")).
		P("openai", oas.Obj().Desc("OpenAI-specific transcription settings.").
			P("model", oas.Str().Desc("OpenAI transcription model.").Enum(constants.TranscriberModelOpenAIOptions).Default("gpt-4o-transcribe").Example("gpt-4o-transcribe"))).
		P("elevenlabs", oas.Obj().Desc("ElevenLabs-specific transcription settings.").
			P("model", oas.Str().Desc("ElevenLabs transcription model.").Enum(constants.TranscriberModelElevenLabsOptions).Default("scribe_v1").Example("scribe_v1")))
}

func postCallSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Post-call analysis settings stored on the agent.").
		P("analysis_provider", oas.Str().Desc("LLM provider used for post-call analysis.").Enum(constants.LLMProviderOptions).Default("openai").Example("openai")).
		P("analysis_model", oas.Str().Nullable().Desc("Provider-specific model used for post-call analysis.").Example(nil))
}

func knowledgeBaseSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Knowledge bases this agent can answer from. Each id must reference a knowledge base owned by the same user; an unknown id is rejected with 404.").
		P("knowledge_base_ids", oas.Arr(oas.Str().Example("knowledge_base_a456426614174000")).
			Desc("Ids of the attached knowledge bases. The list is the complete attachment set: sending it replaces the current attachments, and [] detaches all of them.").
			Default([]string{}).
			Example([]string{"knowledge_base_a456426614174000"}))
}

// toolsSectionSchema documents the tools the agent may call mid-call. Like the
// knowledge-base ids, the list is the whole attachment set.
func toolsSectionSchema() *oas.Schema {
	return oas.Obj().Desc("Tools this agent may call during a call. Each id must reference a tool owned by the same user; an unknown id is rejected with 404.").
		P("tool_ids", oas.Arr(oas.Str().Example("tool_12345")).
			Desc("Ids of the attached tools. The list is the complete attachment set: sending it replaces the current attachments, and [] detaches all of them.").
			Default([]string{}).
			Example([]string{"tool_12345"}))
}

// ---------------------------------------------------------------------------
// Top-level request schemas.
// ---------------------------------------------------------------------------

func createAgentRequestSchema() oas.Object {
	s := oas.Obj().Desc("Full agent configuration. The `agent` section is required.").Req("agent")
	props := s.Properties()
	for k, v := range createAgentRequestProperties() {
		props[k] = v
	}
	return s.Build()
}

func updateAgentRequestSchema() oas.Object {
	// Same properties as create, but nothing is required. The `agent` section's
	// own `required: [name]` is dropped so partial updates validate.
	s := oas.Obj().Desc("Partial agent configuration. Send only the fields you want to change.")
	props := s.Properties()
	for k, v := range createAgentRequestProperties() {
		props[k] = v
	}
	if agent, ok := props["agent"].(oas.Object); ok {
		delete(agent, "required")
	}
	return s.Build()
}

// ---------------------------------------------------------------------------
// Resource + response envelopes.
// ---------------------------------------------------------------------------

func agentResourceSchema() oas.Object {
	s := oas.Obj().Desc("A persisted agent resource: server-managed fields plus the submitted configuration.").
		P("id", oas.Str().Desc("Unique agent identifier.").Example("agent_12345")).
		P("created_at", oas.Str().Format("date-time").Desc("Creation timestamp.").Example("2026-01-15T09:30:00Z")).
		P("updated_at", oas.Str().Format("date-time").Desc("Last-update timestamp.").Example("2026-01-15T09:30:00Z"))
	props := s.Properties()
	for k, v := range createAgentRequestProperties() {
		props[k] = v
	}
	return s.Build()
}

func resourceLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Resource links.").
		P("self", oas.Str().Desc("Canonical URL of this resource.").Example("/v1/agents/agent_12345"))
}

func createAgentResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful create response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Agent created successfully")).
		P("data", oas.Ref("AgentResource")).
		P("links", resourceLinksSchema()).
		Build()
}

func getAgentResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful single-agent response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Agent retrieved successfully")).
		P("data", oas.Ref("AgentResource")).
		P("links", resourceLinksSchema()).
		Build()
}

func paginationMetaSchema() *oas.Schema {
	return oas.Obj().Desc("Collection metadata.").
		P("pagination", oas.Obj().
			P("page", oas.Int().Desc("Current page (1-based).").Example(1)).
			P("limit", oas.Int().Desc("Page size.").Example(20)).
			P("total_items", oas.Int().Desc("Total matching items.").Example(42)).
			P("total_pages", oas.Int().Desc("Total number of pages.").Example(3)).
			P("has_next_page", oas.Bool().Example(true)).
			P("has_previous_page", oas.Bool().Example(false)))
}

func listLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Pagination links as query URLs.").
		P("self", oas.Str().Example("/v1/agents?page=1&limit=20")).
		P("first", oas.Str().Example("/v1/agents?page=1&limit=20")).
		P("previous", oas.Str().Nullable().Desc("Previous page URL, or null on the first page.").Example(nil)).
		P("next", oas.Str().Nullable().Desc("Next page URL, or null on the last page.").Example("/v1/agents?page=2&limit=20")).
		P("last", oas.Str().Example("/v1/agents?page=3&limit=20"))
}

func listAgentsResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful paginated list response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Agents retrieved successfully")).
		P("data", oas.Arr(oas.Ref("AgentResource")).Desc("Page of agent resources.")).
		P("meta", paginationMetaSchema()).
		P("links", listLinksSchema()).
		Build()
}

func deleteAgentResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful delete response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Agent deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").
			P("id", oas.Str().Example("agent_12345")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func errorResponseSchema() oas.Object {
	return oas.Obj().Desc("Standard error envelope.").Req("success", "message", "errors").
		P("success", oas.Bool().Example(false)).
		P("message", oas.Str().Desc("Human-readable error summary.").Example("Validation failed")).
		P("errors", oas.Arr(
			oas.Obj().Req("field", "message").
				P("field", oas.Str().Desc("Field the error applies to.").Example("agent.name")).
				P("message", oas.Str().Desc("What went wrong.").Example("name is required")),
		).Desc("Field-level error details.")).
		Build()
}

// componentSchemas returns the full set of named schemas registered under
// components.schemas and referenced by the Agents operations.
func componentSchemas() oas.Object {
	return oas.Object{
		"CreateAgentRequest":  createAgentRequestSchema(),
		"UpdateAgentRequest":  updateAgentRequestSchema(),
		"AgentResource":       agentResourceSchema(),
		"CreateAgentResponse": createAgentResponseSchema(),
		"GetAgentResponse":    getAgentResponseSchema(),
		"ListAgentsResponse":  listAgentsResponseSchema(),
		"DeleteAgentResponse": deleteAgentResponseSchema(),
		"ErrorResponse":       errorResponseSchema(),
	}
}
