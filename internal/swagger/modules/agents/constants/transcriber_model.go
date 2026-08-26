package constants

// TranscriberModelOpenAIOptions are the transcriber model values valid for the
// openai provider.
var TranscriberModelOpenAIOptions = []string{"gpt-4o-transcribe", "gpt-4o-mini-transcribe", "gpt-4o-transcribe-diarize", "whisper-1"}

// TranscriberModelElevenLabsOptions are the transcriber model values valid for
// the 11labs provider.
var TranscriberModelElevenLabsOptions = []string{"scribe_v1", "scribe_v2", "scribe_v2_realtime"}

// TranscriberModelOptions is the union of every provider's transcriber model
// values.
var TranscriberModelOptions = concat(TranscriberModelOpenAIOptions, TranscriberModelElevenLabsOptions)

// TranscriberModel is the union of TranscriberModelOptions.
type TranscriberModel = string
