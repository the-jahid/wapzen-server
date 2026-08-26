package constants

// Inline / nested enums referenced by individual fields inside the grouped
// agent configuration. Kept together because each is small and only used by a
// single nested object.

// OpenAIResponseFormatOptions are the audio container formats for OpenAI TTS.
var OpenAIResponseFormatOptions = []string{"mp3", "opus", "aac", "flac", "wav", "pcm"}

// OpenAIResponseFormat is the union of OpenAIResponseFormatOptions.
type OpenAIResponseFormat = string

// OpenAIVoiceOptions are the built-in Audio Speech voices.
var OpenAIVoiceOptions = []string{
	"alloy", "ash", "ballad", "coral", "echo",
	"fable", "nova", "onyx", "sage", "shimmer", "verse", "marin", "cedar",
}

// OpenAIVoice is the union of OpenAIVoiceOptions.
type OpenAIVoiceID = string

// STTModeOptions trades transcription speed against accuracy.
var STTModeOptions = []string{"fast", "accurate"}

// STTMode is the union of STTModeOptions.
type STTMode = string

// PostCallFieldTypeOptions are the data types of post-call analysis fields.
var PostCallFieldTypeOptions = []string{"string", "number", "boolean", "enum"}

// PostCallFieldType is the union of PostCallFieldTypeOptions.
type PostCallFieldType = string

// ResourceStatusOptions are the lifecycle states of an agent resource.
var ResourceStatusOptions = []string{"active", "inactive"}

// ResourceStatus is the union of ResourceStatusOptions.
type ResourceStatus = string
