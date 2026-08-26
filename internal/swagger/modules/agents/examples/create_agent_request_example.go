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
		CallDirection: "outbound",
		Status:        "active",
	},
	Model: &types.ModelSection{
		Provider:    "openai",
		Name:        "gpt-4.1-mini",
		Temperature: 0.3,
	},
	Prompt: &types.PromptSection{
		BeginMessageMode:    "agent_speaks_first",
		BeginMessage:        "Hello! How can I help you today?",
		BeginMessageDelayMs: 1000,
		SystemPrompt:        "You are a helpful, friendly voice assistant on a phone call. Keep responses clear and concise, speak naturally, and stay polite and professional at all times.",
		DynamicVariables: map[string]string{},
	},
	Voice: &types.VoiceSection{
		Provider: "openai_realtime",
		ElevenLabs: &types.ElevenLabsVoice{
			VoiceID:    "DODLEQrClDo8wCz460ld",
			VoiceName:  "Lauren",
			VoiceModel: "eleven_flash_v2_5",
		},
		OpenAI: &types.OpenAIVoice{
			VoiceID:      "alloy",
			VoiceName:    "Alloy",
			VoiceModel:   "tts-1",
			Instructions: nil,
		},
	},
	Transcriber: &types.TranscriberSection{
		Provider:   "openai",
		Language:   "en",
		OpenAI:     &types.TranscriberOpenAI{Model: "gpt-4o-transcribe"},
		ElevenLabs: &types.TranscriberElevenLabs{Model: "scribe_v2"},
	},
	KnowledgeBase: &types.KnowledgeBaseSection{
		KnowledgeBaseIDs: []string{"knowledge_base_a456426614174000"},
	},
	PostCall: &types.PostCallSection{
		AnalysisProvider: "openai",
		AnalysisModel:    nil,
		PostCallAnalysisData: []types.PostCallField{
			{
				Type:              "string",
				Name:              "customer_name",
				Description:       "The caller's full name as stated during the call.",
				Examples:          []string{},
				Required:          false,
				EnumValues:        []string{},
				ConditionalPrompt: nil,
			},
		},
	},
}
