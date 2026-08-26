package constants

// VoiceModelElevenLabsOptions are the voice_model values valid for the 11labs
// provider.
var VoiceModelElevenLabsOptions = []string{
	"eleven_multilingual_v2",
	"eleven_turbo_v2",
	"eleven_turbo_v2_5",
	"eleven_flash_v2",
	"eleven_flash_v2_5",
	"eleven_v3",
}

// VoiceModelOpenAIOptions are the voice_model values valid for the openai
// provider.
var VoiceModelOpenAIOptions = []string{"tts-1", "tts-1-hd", "gpt-4o-mini-tts"}

// OpenAIRealtimeModelOptions are the native speech-to-speech models selectable
// per agent when voice.provider is openai_realtime.
var OpenAIRealtimeModelOptions = []string{
	"gpt-realtime",
	"gpt-realtime-1.5",
	"gpt-realtime-mini",
	"gpt-realtime-2",
	"gpt-realtime-2.1",
	"gpt-realtime-2.1-mini",
}

// VoiceModelOptions is the union of every provider's voice_model values.
var VoiceModelOptions = concat(VoiceModelElevenLabsOptions, VoiceModelOpenAIOptions)

// VoiceModel is the union of VoiceModelOptions.
type VoiceModel = string

// OpenAIRealtimeModel is a model identifier accepted by the Realtime API.
type OpenAIRealtimeModel = string
