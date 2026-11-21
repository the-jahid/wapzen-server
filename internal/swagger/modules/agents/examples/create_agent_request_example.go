package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// CreateAgentRequestExample is a complete create payload whose shape mirrors the
// agents database schema (migration 00002): every documented field, with the
// server-generated fields (id, user_id, created_at, updated_at) excluded.
var CreateAgentRequestExample = types.CreateAgentRequest{
	Agent: &types.AgentSection{
		Name:          "My Agent",
		Language:      "en-US",
		Timezone:      nil,
		PhoneNumberID: nil,
		Status:        "active",
	},
	Prompt: &types.PromptSection{
		BeginMessageMode:    "agent_speaks_first",
		BeginMessage:        "Hello! How can I help you today?",
		BeginMessageDelayMs: 1000,
		SystemPrompt:        "You are a helpful, friendly voice assistant on a phone call. Keep responses clear and concise, speak naturally, and stay polite and professional at all times.",
	},
	Voice: &types.VoiceSection{
		OpenAI: &types.OpenAIVoice{
			VoiceID:       "alloy",
			VoiceName:     "Alloy",
			RealtimeModel: "gpt-realtime-2.1-mini",
			Speed:         1,
			Volume:        1,
		},
	},
	PostCall: &types.PostCallSection{
		AnalysisProvider: "openai",
		AnalysisModel:    nil,
	},
	KnowledgeBase: &types.KnowledgeBaseSection{
		KnowledgeBaseIDs: []string{"knowledge_base_a456426614174000"},
	},
	Tools: &types.ToolsSection{
		ToolIDs: []string{"tool_12345"},
	},
}
