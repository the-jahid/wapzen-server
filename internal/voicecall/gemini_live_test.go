package voicecall

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
)

func TestIsGeminiModel(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-3.1-flash-live-preview":         true,
		" Gemini-2.5-flash-native-audio-latest": true,
		"gpt-realtime-2.1-mini":                 false,
		"":                                      false,
	} {
		if got := isGeminiModel(model); got != want {
			t.Errorf("isGeminiModel(%q) = %t, want %t", model, got, want)
		}
	}
}

func TestNormalizeGeminiVoice(t *testing.T) {
	if got := normalizeGeminiVoice("kore"); got != "Kore" {
		t.Errorf("kore -> %q", got)
	}
	// An OpenAI voice left on an agent switched to Gemini falls back.
	if got := normalizeGeminiVoice("echo"); got != defaultGeminiVoice {
		t.Errorf("echo -> %q, want default", got)
	}
}

type fakeTools struct{}

func (fakeTools) Definitions() []toolDefinition {
	return []toolDefinition{{Name: "lookup", Description: "Look it up.", Parameters: map[string]any{"type": "object"}}}
}
func (fakeTools) Run(context.Context, string, string) string { return "ok" }

func TestGeminiLiveSetup(t *testing.T) {
	cfg := geminiLiveConfig{Model: "gemini-3.1-flash-live-preview", Voice: "charon", Instructions: "Be nice.", Tools: fakeTools{}, SilenceMS: 400, PrefixMS: 120, EndSensitivity: "low"}
	data, _ := json.Marshal(geminiLiveSetup(cfg, "h1"))
	s := string(data)
	for _, want := range []string{
		`"model":"models/gemini-3.1-flash-live-preview"`,
		`"voiceName":"Charon"`,
		`"systemInstruction":{"parts":[{"text":"Be nice."}]}`,
		`"parametersJsonSchema":{"type":"object"}`,
		`"sessionResumption":{"handle":"h1"}`,
		`"endOfSpeechSensitivity":"END_SENSITIVITY_LOW"`,
		`"silenceDurationMs":400`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("setup missing %s\n%s", want, s)
		}
	}
	if strings.Contains(s, "startOfSpeechSensitivity") {
		t.Errorf("unset start sensitivity should be omitted: %s", s)
	}
}

func TestUseGemini(t *testing.T) {
	both := &Client{openAI: openAIRealtimeConfig{APIKey: "sk"}, gemini: geminiLiveConfig{APIKey: "g"}}
	openAIOnly := &Client{openAI: openAIRealtimeConfig{APIKey: "sk"}}
	geminiOnly := &Client{gemini: geminiLiveConfig{APIKey: "g"}}
	cases := []struct {
		c     *Client
		model string
		want  bool
	}{
		{both, "gemini-3.1-flash-live-preview", true},
		{both, "gpt-realtime-2.1-mini", false},
		{openAIOnly, "gemini-3.1-flash-live-preview", false},
		{geminiOnly, "gpt-realtime-2.1-mini", true},
	}
	for _, tc := range cases {
		if got := tc.c.useGemini(tc.model); got != tc.want {
			t.Errorf("useGemini(%q) = %t, want %t", tc.model, got, tc.want)
		}
	}
}

func TestGeminiTranscriptOrder(t *testing.T) {
	b := &geminiLiveBridge{callerTurn: -1, assistantTurn: -1, cancelledTools: map[string]bool{}}
	b.callerText("What are ")
	b.assistantText("We open ")
	b.callerText("your hours?") // trails in after the reply began
	b.assistantText("at nine.")
	b.endTurn()
	b.callerText("Thanks.")
	got := b.Transcript()
	want := []conversationMessage{{Role: "user", Content: "What are your hours?"}, {Role: "assistant", Content: "We open at nine."}, {Role: "user", Content: "Thanks."}}
	if len(got) != len(want) {
		t.Fatalf("transcript = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("turn %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestGeminiLiveSmoke opens a real Gemini Live session through the bridge and
// checks the agent greets with audio. It runs only with GEMINI_LIVE_SMOKE=1 and
// GEMINI_API_KEY set, since it spends API quota.
func TestGeminiLiveSmoke(t *testing.T) {
	if os.Getenv("GEMINI_LIVE_SMOKE") != "1" {
		t.Skip("set GEMINI_LIVE_SMOKE=1 to run against the live API")
	}
	cfg := loadGeminiLiveConfig()
	if !cfg.Enabled() {
		t.Skip("GEMINI_API_KEY not set")
	}
	if model := os.Getenv("GEMINI_SMOKE_MODEL"); model != "" {
		cfg.Model = model
	}
	cfg.Instructions = composeCallInstructions("You are the receptionist at Acme Dental. Speak English.", "", "")
	cfg.BeginMessageMode = "agent_speaks_first"
	cfg.WelcomeMessage = "Hello, thank you for calling Acme Dental. How can I help you today?"

	var mu sync.Mutex
	var samples int
	played := make(chan struct{}, 8)
	b := newGeminiLiveBridge(cfg, func(source meowcaller.AudioSource) {
		go func() {
			for {
				frame, err := source.ReadFrame()
				if err != nil {
					return
				}
				if !hasSound(frame) {
					continue // the source pads with silence while it waits
				}
				mu.Lock()
				samples += len(frame)
				mu.Unlock()
				select {
				case played <- struct{}{}:
				default:
				}
			}
		}()
	}, func() {}, bridgeHooks{})
	defer b.Close()

	start := time.Now()
	select {
	case <-b.Ready():
		if err := b.Err(); err != nil {
			t.Fatalf("session failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("session not ready")
	}
	t.Logf("ready after %s", time.Since(start))
	// Feed silence like a quiet caller line while the greeting plays.
	silence := make([]float32, meowcaller.FrameSamples)
	deadline := time.After(20 * time.Second)
	for {
		_ = b.WriteFrame(silence)
		select {
		case <-deadline:
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("no greeting audio; samples=%d transcript=%+v", samples, b.Transcript())
		case <-time.After(20 * time.Millisecond):
		}
		mu.Lock()
		n := samples
		mu.Unlock()
		if n > audioSampleRate*2 && !b.Busy() {
			t.Logf("greeting: %.1f s of audio, transcript=%+v", float64(n)/audioSampleRate, b.Transcript())
			return
		}
	}
}

func hasSound(frame []float32) bool {
	for _, v := range frame {
		if v > 0.001 || v < -0.001 {
			return true
		}
	}
	return false
}
