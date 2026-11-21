package constants

// OpenAIRealtimeModelOptions are the speech-to-speech models selectable per
// agent. OpenAI Realtime is the only live-call engine.
var OpenAIRealtimeModelOptions = []string{
	"gpt-realtime",
	"gpt-realtime-1.5",
	"gpt-realtime-mini",
	"gpt-realtime-2",
	"gpt-realtime-2.1",
	"gpt-realtime-2.1-mini",
}

// OpenAIRealtimeModel is a model identifier accepted by the Realtime API.
type OpenAIRealtimeModel = string
