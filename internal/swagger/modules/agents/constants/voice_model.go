package constants

// OpenAIRealtimeModelOptions are the speech-to-speech models selectable per
// agent. A gemini-* model runs the call on Google Gemini Live, the rest on
// OpenAI Realtime.
var OpenAIRealtimeModelOptions = []string{
	"gpt-realtime",
	"gpt-realtime-1.5",
	"gpt-realtime-mini",
	"gpt-realtime-2",
	"gpt-realtime-2.1",
	"gpt-realtime-2.1-mini",
	"gemini-3.1-flash-live-preview",
	"gemini-3.8-live",
	"gemini-2.5-flash-native-audio-latest",
}

// OpenAIRealtimeModel is a model identifier accepted by the Realtime API.
type OpenAIRealtimeModel = string

// Every new voice agent starts on Google Gemini Live: Gemini 3.8 Live speaking
// in Zephyr. The agents table carries the same column defaults (whiskey
// migration 20261122000001) and the dashboard sends them on create.
const (
	DefaultRealtimeModel = "gemini-3.8-live"
	DefaultVoiceID       = "zephyr"
	DefaultVoiceName     = "Zephyr"

	// Used when a create request names an OpenAI voice without a model.
	DefaultOpenAIRealtimeModel = "gpt-realtime-2.1-mini"
	// Used when a create request names an OpenAI model without a voice.
	DefaultOpenAIVoiceID   = "echo"
	DefaultOpenAIVoiceName = "Echo"
)
