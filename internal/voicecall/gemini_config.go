package voicecall

import (
	"strings"
)

const (
	defaultGeminiLiveURL = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	// Every voice agent defaults to Gemini 3.8 Live with the Zephyr voice
	// (2026-11-22). gemini-3.1-flash-live-preview measured ~0.55 s from a
	// finished question to its first audio; the 2.5 native-audio model ~2.6 s and
	// it sent tool arguments empty.
	defaultGeminiLiveModel = "gemini-3.8-live"
	defaultGeminiVoice     = "Zephyr"

	// Gemini Live hears 16 kHz PCM — the WhatsApp call rate, so the caller's
	// audio goes over untouched — and speaks 24 kHz, which is resampled down.
	geminiInputSampleRate  = 16000
	geminiOutputSampleRate = 24000

	// Same turn-end silence as the OpenAI session, for the same reason: long
	// enough to ride over a mid-sentence pause, short enough to answer quickly.
	defaultGeminiSilenceMS = 400

	// maxGeminiReconnects bounds how many times one call resumes its session on
	// a fresh connection. Google ends each Live connection after about ten
	// minutes (it warns with goAway first); a 20-minute call needs one or two.
	maxGeminiReconnects = 4
)

// geminiLiveConfig describes a Google Gemini Live session: like OpenAI
// Realtime, one speech-to-speech model that hears, thinks and speaks.
type geminiLiveConfig struct {
	APIKey string
	URL    string
	Model  string
	Voice  string
	// SilenceMS and PrefixMS tune Gemini's own turn detector.
	SilenceMS int
	PrefixMS  int
	// StartSensitivity / EndSensitivity are "high", "low" or "" for Google's
	// default.
	StartSensitivity string
	EndSensitivity   string

	// Per-call settings, filled from the agent in runCall.
	Instructions     string
	Tools            toolRunner
	BeginMessageMode string
	WelcomeMessage   string
	WelcomeDelayMs   int
	Volume           float64
}

func loadGeminiLiveConfig() geminiLiveConfig {
	return geminiLiveConfig{
		APIKey:           strings.TrimSpace(firstNonEmptyEnv("GEMINI_API_KEY", "GOOGLE_API_KEY")),
		URL:              envString("GEMINI_LIVE_URL", defaultGeminiLiveURL),
		Model:            envString("GEMINI_LIVE_MODEL", defaultGeminiLiveModel),
		Voice:            normalizeGeminiVoice(envString("GEMINI_LIVE_VOICE", defaultGeminiVoice)),
		SilenceMS:        envInt("GEMINI_LIVE_SILENCE_MS", defaultGeminiSilenceMS),
		PrefixMS:         envInt("GEMINI_LIVE_PREFIX_PADDING_MS", 120),
		StartSensitivity: envString("GEMINI_LIVE_START_SENSITIVITY", ""),
		EndSensitivity:   envString("GEMINI_LIVE_END_SENSITIVITY", ""),
		Volume:           clampVolume(envFloat("GEMINI_LIVE_VOLUME", defaultVolume)),
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := envString(key, ""); value != "" {
			return value
		}
	}
	return ""
}

// Enabled reports whether Gemini Live can take calls: a key is set and
// GEMINI_LIVE_ENABLED does not turn it off.
func (c geminiLiveConfig) Enabled() bool {
	return strings.TrimSpace(c.APIKey) != "" && envBoolDefault("GEMINI_LIVE_ENABLED", true)
}

// isGeminiModel reports whether an agent's realtime model runs on Gemini Live
// rather than OpenAI Realtime. The model alone decides the engine, so an agent
// needs no separate provider setting.
func isGeminiModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini")
}

// geminiVoices maps the stored (lowercase) voice id to the name Gemini takes.
var geminiVoices = map[string]string{
	"puck": "Puck", "charon": "Charon", "kore": "Kore", "fenrir": "Fenrir",
	"aoede": "Aoede", "leda": "Leda", "orus": "Orus", "zephyr": "Zephyr",
}

// isGeminiVoice reports whether voice is one of the Gemini Live voices.
func isGeminiVoice(voice string) bool {
	_, ok := geminiVoices[strings.ToLower(strings.TrimSpace(voice))]
	return ok
}

// normalizeGeminiVoice returns the Gemini voice name for voice, or the default
// voice for anything else (an OpenAI voice left on an agent moved to Gemini).
func normalizeGeminiVoice(voice string) string {
	if name, ok := geminiVoices[strings.ToLower(strings.TrimSpace(voice))]; ok {
		return name
	}
	return defaultGeminiVoice
}

// geminiSensitivity renders a "high"/"low" setting as the enum the setup
// message takes for start- or end-of-speech, or "" to leave Google's default.
func geminiSensitivity(kind, value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return kind + "_SENSITIVITY_HIGH"
	case "low":
		return kind + "_SENSITIVITY_LOW"
	default:
		return ""
	}
}

// useGemini decides a call's engine: Gemini Live for a Gemini model, OpenAI
// Realtime otherwise — each only when this server has its key, falling back to
// whichever engine it does have.
func (c *Client) useGemini(model string) bool {
	if isGeminiModel(model) {
		return c.gemini.Enabled() || !c.openAI.Enabled()
	}
	return !c.openAI.Enabled() && c.gemini.Enabled()
}

// geminiCallConfig carries one call's resolved settings (prompt, tools,
// greeting, volume) onto the Gemini session. The agent's model and voice are
// used when they are Gemini ones, the server defaults otherwise.
func (c *Client) geminiCallConfig(call openAIRealtimeConfig, agentVoice string) geminiLiveConfig {
	cfg := c.gemini
	if isGeminiModel(call.RealtimeModel) {
		cfg.Model = strings.TrimSpace(call.RealtimeModel)
	}
	if isGeminiVoice(agentVoice) {
		cfg.Voice = normalizeGeminiVoice(agentVoice)
	}
	cfg.Instructions = call.Instructions
	cfg.Tools = call.Tools
	cfg.BeginMessageMode = call.BeginMessageMode
	cfg.WelcomeMessage = call.WelcomeMessage
	cfg.WelcomeDelayMs = call.WelcomeDelayMs
	cfg.Volume = call.Volume
	return cfg
}
