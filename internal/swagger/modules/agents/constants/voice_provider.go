package constants

// VoiceProviderOptions is the set of allowed voice provider values.
var VoiceProviderOptions = []string{"openai_realtime", "openai", "11labs"}

// VoiceProvider is the union of VoiceProviderOptions.
type VoiceProvider = string
