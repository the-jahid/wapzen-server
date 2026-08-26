package constants

// TranscriberProviderOptions is the set of allowed transcriber provider values.
var TranscriberProviderOptions = []string{"openai", "11labs"}

// TranscriberProvider is the union of TranscriberProviderOptions.
type TranscriberProvider = string
