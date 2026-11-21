package constants

// Inline / nested enums referenced by individual fields inside the grouped
// agent configuration. Kept together because each is small and only used by a
// single nested object.

// OpenAIVoiceOptions are the voices the OpenAI Realtime models speak.
var OpenAIVoiceOptions = []string{
	"alloy", "ash", "ballad", "coral", "echo",
	"sage", "shimmer", "verse", "marin", "cedar",
}

// OpenAIVoiceID is the union of OpenAIVoiceOptions.
type OpenAIVoiceID = string

// ResourceStatusOptions are the lifecycle states of an agent resource.
var ResourceStatusOptions = []string{"active", "inactive"}

// ResourceStatus is the union of ResourceStatusOptions.
type ResourceStatus = string
