package constants

// Inline / nested enums referenced by individual fields inside the grouped
// agent configuration. Kept together because each is small and only used by a
// single nested object.

// OpenAIVoiceOptions are the voices an agent can speak in: the OpenAI Realtime
// voices, then the Gemini Live ones (used when the realtime model is Gemini).
var OpenAIVoiceOptions = []string{
	"alloy", "ash", "ballad", "coral", "echo",
	"sage", "shimmer", "verse", "marin", "cedar",
	"puck", "charon", "kore", "fenrir", "aoede", "leda", "orus", "zephyr",
}

// OpenAIVoiceID is the union of OpenAIVoiceOptions.
type OpenAIVoiceID = string

// ResourceStatusOptions are the lifecycle states of an agent resource.
var ResourceStatusOptions = []string{"active", "inactive"}

// ResourceStatus is the union of ResourceStatusOptions.
type ResourceStatus = string
