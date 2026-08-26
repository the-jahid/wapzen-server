package constants

// LLMProviderOptions is the set accepted by both model_provider and
// post_call_analysis_provider in the agents schema.
var LLMProviderOptions = []string{"openai", "anthropic"}

// LLMProvider is the union of LLMProviderOptions.
type LLMProvider = string
