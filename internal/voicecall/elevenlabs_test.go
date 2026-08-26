package voicecall

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/purpshell/meowcaller"
)

func TestElevenLabsSTTURLUsesRealtimeScribeVAD(t *testing.T) {
	got := elevenLabsSTTURL(defaultElevenLabsSTTURL, "scribe_v2_realtime", "bn-BD", defaultOpenAISilenceMS, commitStrategyVAD)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("model_id") != "scribe_v2_realtime" || q.Get("audio_format") != "pcm_16000" {
		t.Errorf("Scribe query = %v", q)
	}
	if q.Get("commit_strategy") != "vad" || q.Get("language_code") != "bn" {
		t.Errorf("Scribe VAD/language query = %v", q)
	}
}

func TestElevenLabsTTSURLUsesSavedVoiceAndModel(t *testing.T) {
	got := elevenLabsTTSURL(defaultElevenLabsTTSBase, "voice_123", "eleven_flash_v2_5", "en-US")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/v1/text-to-speech/voice_123/multi-stream-input" {
		t.Errorf("TTS path = %q", u.Path)
	}
	q := u.Query()
	if q.Get("model_id") != "eleven_flash_v2_5" || q.Get("output_format") != "pcm_16000" || q.Get("language_code") != "en" || q.Get("auto_mode") != "true" {
		t.Errorf("TTS query = %v", q)
	}
}

func TestElevenLabsDynamicPipelineNeedsNoAgentID(t *testing.T) {
	cfg := elevenLabsConfig{APIKey: "eleven-key", EnabledFlag: true}
	if !cfg.Enabled() {
		t.Error("ElevenLabs should be enabled by its API key alone")
	}
	if got := elevenLabsSTTURL(defaultElevenLabsSTTURL, defaultElevenLabsSTTModel, "en", defaultOpenAISilenceMS, commitStrategyManual); containsAgentID(got) {
		t.Errorf("Scribe URL unexpectedly contains agent_id: %s", got)
	}
	if got := elevenLabsTTSURL(defaultElevenLabsTTSBase, "voice", "eleven_flash_v2_5", "en"); containsAgentID(got) {
		t.Errorf("TTS URL unexpectedly contains agent_id: %s", got)
	}
}

func TestNormalizeScribeModelPreservesSupportedSelections(t *testing.T) {
	for _, model := range []string{"scribe_v1", "scribe_v2", "scribe_v2_realtime"} {
		if got := normalizeScribeModel(model); got != model {
			t.Errorf("normalizeScribeModel(%q) = %q", model, got)
		}
	}
	if got := normalizeScribeModel("unknown"); got != defaultElevenLabsSTTModel {
		t.Fatalf("unknown model fallback = %q", got)
	}
}

func TestElevenLabsBatchTranscriptionUsesSavedModel(t *testing.T) {
	var gotModel, gotLanguage, gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("xi-api-key")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart form: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model_id")
		gotLanguage = r.FormValue("language_code")
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("missing audio file: %v", err)
		} else {
			data, _ := io.ReadAll(file)
			_ = file.Close()
			if len(data) < 44 || string(data[:4]) != "RIFF" {
				t.Errorf("uploaded file is not WAV: %d bytes", len(data))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello caller"}`))
	}))
	defer server.Close()

	b := &elevenLabsVoiceBridge{cfg: elevenLabsConfig{
		APIKey:           "eleven-key",
		BatchSTTEndpoint: server.URL,
		STTModel:         "scribe_v2",
		Language:         "en-US",
		HTTPClient:       server.Client(),
	}}
	text, err := b.transcribeBatch(context.Background(), make([]float32, audioSampleRate/2))
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello caller" || gotModel != "scribe_v2" || gotLanguage != "en" || gotAPIKey != "eleven-key" {
		t.Fatalf("text=%q model=%q language=%q api_key=%q", text, gotModel, gotLanguage, gotAPIKey)
	}
}

func containsAgentID(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Query().Get("agent_id") != ""
}

func TestElevenLabsAudioStartsAndInterruptedContextCannotRestart(t *testing.T) {
	var played []meowcaller.AudioSource
	stopped := 0
	ctx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	_, cancelTurn := context.WithCancel(ctx)
	b := &elevenLabsVoiceBridge{
		cfg:          elevenLabsConfig{Volume: defaultVolume},
		ctx:          ctx,
		cancel:       cancelRoot,
		ignored:      make(map[string]bool),
		current:      &elevenLabsTurn{id: 1, contextID: "call_turn_1", cancel: cancelTurn},
		playSource:   func(source meowcaller.AudioSource) { played = append(played, source) },
		stopPlayback: func() { stopped++ },
	}
	pcm := float32ToPCM16LE(make([]float32, meowcaller.FrameSamples))
	encoded := base64.StdEncoding.EncodeToString(pcm)
	b.appendOutputAudio("call_turn_1", encoded)
	if len(played) != 1 {
		t.Fatalf("played %d sources, want 1", len(played))
	}

	b.interruptCurrentTurn()
	if stopped != 1 {
		t.Errorf("stopPlayback called %d times, want 1", stopped)
	}
	if b.output != nil {
		t.Error("interruption left an active output source")
	}
	b.appendOutputAudio("call_turn_1", encoded)
	if len(played) != 1 {
		t.Error("late TTS audio from the interrupted context restarted playback")
	}
}

func TestConversationHistoryIsBounded(t *testing.T) {
	b := &elevenLabsVoiceBridge{}
	for i := 0; i < maxConversationMessages+5; i++ {
		b.appendHistory(conversationMessage{Role: "user", Content: "message"})
	}
	if got := len(b.historySnapshot()); got != maxConversationMessages {
		t.Errorf("history length = %d, want %d", got, maxConversationMessages)
	}
}

func TestClientProviderAndLLMReadiness(t *testing.T) {
	c := &Client{
		openAI: openAIStandardConfig{
			APIKey:          "openai-key",
			RealtimeEnabled: true,
			LLM: conversationLLM{
				openAIKey:    "openai-key",
				anthropicKey: "anthropic-key",
			},
		},
		elevenLabs: elevenLabsConfig{
			APIKey:      "eleven-key",
			EnabledFlag: true,
			LLM: conversationLLM{
				openAIKey:    "openai-key",
				anthropicKey: "anthropic-key",
			},
		},
	}
	if !c.CanHandleProvider("openai_realtime") || !c.CanHandleProvider("openai") || !c.CanHandleProvider("ElevenLabs") {
		t.Error("configured voice providers were not reported ready")
	}
	if !c.CanHandleLLM("openai") || !c.CanHandleLLM("anthropic") {
		t.Error("configured dynamic LLM providers were not reported ready")
	}
	c.elevenLabs.APIKey = ""
	if c.CanHandleProvider("11labs") {
		t.Error("ElevenLabs was ready without ELEVENLABS_API_KEY")
	}
	c.openAI.RealtimeEnabled = false
	if c.CanHandleProvider("openai_realtime") {
		t.Error("OpenAI Realtime was ready while OPENAI_REALTIME_ENABLED was false")
	}
	if !c.CanHandleProvider("openai") {
		t.Error("standard OpenAI should remain ready when Realtime is disabled")
	}
}

// A language the TTS model cannot be pinned to must never reach the wire:
// ElevenLabs answers language_code it does not support by closing the synthesis
// socket with a policy violation, and the call then plays no audio at all.
func TestElevenLabsTTSURLOmitsUnsupportedLanguage(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		language string
		want     string
	}{
		{"multilingual v2 takes no language_code at all", "eleven_multilingual_v2", "bn-BD", ""},
		{"multilingual v2 takes none even for English", "eleven_multilingual_v2", "en-US", ""},
		{"flash v2.5 does not speak Bengali", "eleven_flash_v2_5", "bn-BD", ""},
		{"turbo v2.5 does not speak Bengali", "eleven_turbo_v2_5", "bn-BD", ""},
		{"flash v2.5 speaks French", "eleven_flash_v2_5", "fr-FR", "fr"},
		{"turbo v2.5 speaks Hindi", "eleven_turbo_v2_5", "hi-IN", "hi"},
		{"an unset model lets ElevenLabs pick, so pin nothing", "", "en-US", ""},
		{"an unknown future model pins nothing", "eleven_v9", "en-US", ""},
		{"no language selected pins nothing", "eleven_flash_v2_5", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(elevenLabsTTSURL(defaultElevenLabsTTSBase, "voice_123", tc.model, tc.language))
			if err != nil {
				t.Fatal(err)
			}
			if got := u.Query().Get("language_code"); got != tc.want {
				t.Errorf("language_code = %q, want %q", got, tc.want)
			}
		})
	}
}

// Scribe understands far more languages than the TTS models do, so the
// transcriber keeps its hint even when synthesis has to give one up.
func TestElevenLabsSTTURLKeepsLanguageTTSCannotUse(t *testing.T) {
	u, err := url.Parse(elevenLabsSTTURL(defaultElevenLabsSTTURL, "scribe_v2_realtime", "bn-BD", defaultOpenAISilenceMS, commitStrategyManual))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("language_code"); got != "bn" {
		t.Errorf("Scribe language_code = %q, want %q", got, "bn")
	}
}

func TestElevenLabsSpeaksLanguage(t *testing.T) {
	cases := []struct {
		model    string
		language string
		want     bool
	}{
		{"eleven_multilingual_v2", "en-US", true},
		{"eleven_multilingual_v2", "fr-FR", true},
		{"eleven_multilingual_v2", "hu-HU", false}, // v2.5 added Hungarian.
		{"eleven_multilingual_v2", "bn-BD", false},
		{"eleven_flash_v2_5", "hu-HU", true},
		{"eleven_flash_v2_5", "bn-BD", false},
		{"eleven_turbo_v2_5", "vi-VN", true},
		{"eleven_turbo_v2", "en-GB", true},
		{"eleven_flash_v2", "fr-FR", false}, // v2 is English only.
		{"", "bn-BD", false},                // ElevenLabs falls back to Multilingual v2.
		{"eleven_v3", "bn-BD", true},        // v3 covers 70+ languages.
		{"eleven_flash_v2_5", "", true},     // No language selected is never a mismatch.
	}
	for _, tc := range cases {
		if got := elevenLabsSpeaksLanguage(tc.model, tc.language); got != tc.want {
			t.Errorf("elevenLabsSpeaksLanguage(%q, %q) = %v, want %v", tc.model, tc.language, got, tc.want)
		}
	}
}

// The manual strategy is what keeps Scribe's own trailing-silence window and its
// ~600 ms of VAD finalization out of every turn, so it has to be what an
// unconfigured install gets.
func TestElevenLabsSTTURLDefaultsToManualCommit(t *testing.T) {
	u, err := url.Parse(elevenLabsSTTURL(defaultElevenLabsSTTURL, "scribe_v2_realtime", "en", defaultOpenAISilenceMS, ""))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("commit_strategy") != commitStrategyManual {
		t.Errorf("commit_strategy = %q, want %q", q.Get("commit_strategy"), commitStrategyManual)
	}
	if q.Has("vad_silence_threshold_secs") || q.Has("vad_threshold") {
		t.Errorf("manual commit still asks Scribe to run its own VAD: %v", q)
	}
}

func TestNormalizeCommitStrategy(t *testing.T) {
	for _, strategy := range []string{"", "manual", "MANUAL", "nonsense"} {
		if got := normalizeCommitStrategy(strategy); got != commitStrategyManual {
			t.Errorf("normalizeCommitStrategy(%q) = %q, want %q", strategy, got, commitStrategyManual)
		}
	}
	for _, strategy := range []string{"vad", " VAD "} {
		if got := normalizeCommitStrategy(strategy); got != commitStrategyVAD {
			t.Errorf("normalizeCommitStrategy(%q) = %q, want %q", strategy, got, commitStrategyVAD)
		}
	}
}

// An agent that never picked a voice model should not silently land on the
// slowest one ElevenLabs offers, but picking a fast model that cannot speak the
// agent's language would be a worse trade than the latency it saves.
func TestResolveElevenLabsTTSModel(t *testing.T) {
	cases := []struct {
		model    string
		language string
		want     string
	}{
		{"", "", defaultElevenLabsTTSModel},
		{"", "en-US", defaultElevenLabsTTSModel},
		{"", "bn-BD", ""},
		{"eleven_multilingual_v2", "en-US", "eleven_multilingual_v2"},
		{"  eleven_turbo_v2_5  ", "bn-BD", "eleven_turbo_v2_5"},
	}
	for _, tc := range cases {
		if got := resolveElevenLabsTTSModel(tc.model, tc.language); got != tc.want {
			t.Errorf("resolveElevenLabsTTSModel(%q, %q) = %q, want %q", tc.model, tc.language, got, tc.want)
		}
	}
}
