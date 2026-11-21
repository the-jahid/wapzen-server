package voicecall

import (
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	defaultOpenAIRealtimeURL = "wss://api.openai.com/v1/realtime"
	// The mini Realtime model is the latency-oriented production default for
	// phone calls. Operators can select a larger Realtime model through env when
	// quality matters more than response time.
	defaultOpenAIRealtimeModel = "gpt-realtime-2.1-mini"
	openAIRealtimeSampleRate   = 24000
	defaultOpenAIVoice         = "echo"
	// How long the caller has to stop making sound before their turn is treated
	// as finished. Every millisecond here is silence the caller sits through on
	// every single turn, but too little cuts callers off mid-sentence: at 250 ms
	// a caller who paused to think was answered twice, once per half of the
	// sentence. 400 ms rides over a natural mid-sentence pause and still
	// answers well under a second.
	defaultOpenAISilenceMS = 400

	// defaultOpenAIMaxOutputTokens bounds one spoken reply. It is a safety cap,
	// not a brevity control — brevity comes from the call rules in the prompt.
	// Audio tokens run fast (Bengali and Hindi especially), and the old cap of
	// 256 cut 38 of 73 logged replies off mid-sentence ("status: incomplete").
	defaultOpenAIMaxOutputTokens = 4096

	// defaultOpenAINoiseReduction filters the caller's audio before the turn
	// detector and the model hear it, so background noise (traffic, a TV, a
	// second person) is less likely to be taken for the caller talking and
	// interrupt the agent mid-reply. near_field suits a phone held to the ear.
	defaultOpenAINoiseReduction = "near_field"

	// maxToolRounds bounds how many times the model may call tools since the
	// caller last spoke before it is made to answer with what it has. Each round
	// is silence the caller waits through, and one lookup answers almost every
	// question, so the budget is small on purpose.
	maxToolRounds = 2
)

// openAIRealtimeConfig describes the one live-call engine: a single OpenAI
// Realtime speech-to-speech session that hears, thinks and speaks.
type openAIRealtimeConfig struct {
	APIKey string
	// TranscriptionModel transcribes the caller's side for the saved call
	// transcript. It never gates a reply.
	TranscriptionModel string
	Voice              string
	Speed              float64
	Volume             float64
	Language           string
	Instructions       string
	// Tools are the functions the model may call during the call — the agent's
	// knowledge base lookup and its attached tools — or nil when it answers from
	// its prompt alone.
	Tools             toolRunner
	BeginMessageMode  string
	WelcomeMessage    string
	WelcomeDelayMs    int
	RealtimeURL       string
	RealtimeModel     string
	RealtimeVADMode   string
	RealtimeEagerness string
	RealtimeThreshold float64
	RealtimeSilenceMS int
	RealtimePrefixMS  int
	// MaxOutputTokens caps one response; 0 or less means no cap ("inf").
	MaxOutputTokens int
	// NoiseReduction is "near_field", "far_field", or "" for none.
	NoiseReduction string
}

func loadOpenAIRealtimeConfig() openAIRealtimeConfig {
	return openAIRealtimeConfig{
		APIKey:             strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		TranscriptionModel: envString("OPENAI_TRANSCRIBE_MODEL", defaultTranscribeModel),
		Voice:              normalizeOpenAIRealtimeVoice(envString("OPENAI_REALTIME_VOICE", defaultOpenAIVoice)),
		Speed:              clampSpeed(envFloat("OPENAI_REALTIME_SPEED", defaultSpeed)),
		Volume:             clampVolume(envFloat("OPENAI_REALTIME_VOLUME", defaultVolume)),
		Instructions:       envString("OPENAI_RESPONSE_INSTRUCTIONS", defaultInstructions),
		RealtimeURL:        envString("OPENAI_REALTIME_URL", defaultOpenAIRealtimeURL),
		RealtimeModel:      envString("OPENAI_REALTIME_MODEL", defaultOpenAIRealtimeModel),
		RealtimeVADMode:    envString("OPENAI_REALTIME_VAD_MODE", "server_vad"),
		RealtimeEagerness:  envString("OPENAI_REALTIME_EAGERNESS", "high"),
		RealtimeThreshold:  envFloat("OPENAI_REALTIME_VAD_THRESHOLD", 0.5),
		RealtimeSilenceMS:  envInt("OPENAI_REALTIME_SILENCE_MS", defaultOpenAISilenceMS),
		RealtimePrefixMS:   envInt("OPENAI_REALTIME_PREFIX_PADDING_MS", 120),
		MaxOutputTokens:    envInt("OPENAI_REALTIME_MAX_OUTPUT_TOKENS", defaultOpenAIMaxOutputTokens),
		NoiseReduction:     normalizeNoiseReduction(envString("OPENAI_REALTIME_NOISE_REDUCTION", defaultOpenAINoiseReduction)),
	}
}

// normalizeNoiseReduction accepts the two filter types the Realtime API takes
// and maps anything else ("off", "none", typos) to no filter, so a bad env value
// can never get the whole session.update rejected — a rejected update leaves
// the session running on OpenAI's defaults, without the agent's prompt.
func normalizeNoiseReduction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "near_field":
		return "near_field"
	case "far_field":
		return "far_field"
	default:
		return ""
	}
}

// Enabled reports whether calls can be answered: an OpenAI key is set and
// neither OPENAI_VOICE_ENABLED nor OPENAI_REALTIME_ENABLED turns calling off.
func (c openAIRealtimeConfig) Enabled() bool {
	key := strings.TrimSpace(c.APIKey)
	return key != "" && key != placeholderAPIKey &&
		envBoolDefault("OPENAI_VOICE_ENABLED", true) &&
		envBoolDefault("OPENAI_REALTIME_ENABLED", true)
}

// transcriptionLanguage turns an agent locale ("bn-BD") into the ISO-639-1 code
// the input transcriber takes ("bn"), or "" when there is none.
func transcriptionLanguage(locale string) string {
	language := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(language, "-_"); i >= 0 {
		language = language[:i]
	}
	if len(language) == 2 {
		return language
	}
	return ""
}

// conversationMessage is one turn of the saved call transcript.
type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// itemID is the Realtime conversation item the turn belongs to, used only
	// to put asynchronously transcribed turns in conversation order.
	itemID string
}

// websocketResponseBody renders the start of a failed WebSocket handshake's
// HTTP body for the connect-error log line, or "" when there is none.
func websocketResponseBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil || len(body) == 0 {
		return ""
	}
	return ": " + strings.TrimSpace(string(body))
}

// apiError is the error object OpenAI puts on a failed Realtime event.
type apiError struct {
	Message string `json:"message"`
}

func (e *apiError) message() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}
