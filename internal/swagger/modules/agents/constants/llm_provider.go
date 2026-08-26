package constants

// LLMProviderOptions is the set of allowed LLM provider values (used by the
// model and post_call_analysis providers).
var LLMProviderOptions = []string{"openai", "anthropic", "google", "azure_openai", "custom"}

// LLMProvider is the union of LLMProviderOptions.
type LLMProvider = string
