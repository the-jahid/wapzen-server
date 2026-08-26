package voicecall

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/purpshell/meowcaller"
)

func TestOpenAIRealtimeWebSocketURLAddsModel(t *testing.T) {
	got := openAIRealtimeWebSocketURL(defaultOpenAIRealtimeURL, "gpt-realtime-2.1-mini")
	want := defaultOpenAIRealtimeURL + "?model=gpt-realtime-2.1-mini"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got := openAIRealtimeWebSocketURL(defaultOpenAIRealtimeURL+"?model=pinned", "ignored"); got != defaultOpenAIRealtimeURL+"?model=pinned" {
		t.Fatalf("explicit model was replaced: %q", got)
	}
}

func TestOpenAIRealtimeSessionUsesLowLatencyAudioSettings(t *testing.T) {
	payload := openAIRealtimeSessionUpdate(openAIStandardConfig{
		RealtimeModel:     "gpt-realtime-2.1-mini",
		Instructions:      "Be concise.",
		Voice:             "nova", // TTS-only voice must be remapped.
		Speed:             3,
		RealtimeSilenceMS: 250,
		RealtimePrefixMS:  120,
		RealtimeThreshold: 0.5,
	})
	session := payload["session"].(map[string]any)
	if session["model"] != "gpt-realtime-2.1-mini" || session["max_output_tokens"] != 256 {
		t.Fatalf("session = %#v", session)
	}
	audio := session["audio"].(map[string]any)
	input := audio["input"].(map[string]any)
	format := input["format"].(map[string]any)
	if format["rate"] != openAIRealtimeSampleRate {
		t.Fatalf("input rate = %#v", format["rate"])
	}
	turn := input["turn_detection"].(map[string]any)
	if turn["silence_duration_ms"] != 250 || turn["interrupt_response"] != true {
		t.Fatalf("turn detection = %#v", turn)
	}
	output := audio["output"].(map[string]any)
	if output["voice"] != "shimmer" || output["speed"] != 1.5 {
		t.Fatalf("output = %#v", output)
	}
	// An agent with no knowledge bases declares no tools at all, so the session
	// is exactly what it was before retrieval existed.
	if _, present := session["tools"]; present {
		t.Fatalf("session declared tools without any: %#v", session["tools"])
	}
}

func TestOpenAIRealtimeSessionDeclaresKnowledgeTool(t *testing.T) {
	payload := openAIRealtimeSessionUpdate(openAIStandardConfig{
		RealtimeModel: "gpt-realtime-2.1-mini",
		Instructions:  "Be concise.",
		Tools:         newKnowledgeToolbox(&fakeRetriever{enabled: true}, testKnowledgeBases(), "call-1"),
	})
	session := payload["session"].(map[string]any)

	tools, ok := session["tools"].([]map[string]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", session["tools"])
	}
	if tools[0]["type"] != "function" || tools[0]["name"] != knowledgeToolName {
		t.Fatalf("tool = %#v", tools[0])
	}
	if tools[0]["parameters"] == nil {
		t.Fatal("the tool was declared without an argument schema")
	}
	if session["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v, want the model free to call it", session["tool_choice"])
	}
}

func TestOpenAIRealtimeBridgeStreamsCallAudioBothWays(t *testing.T) {
	sessionUpdate := make(chan map[string]any, 1)
	inputAudio := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		readJSON := func(dst chan<- map[string]any) bool {
			_, data, err := conn.Read(ctx)
			if err != nil {
				t.Errorf("read websocket: %v", err)
				return false
			}
			var event map[string]any
			if err := json.Unmarshal(data, &event); err != nil {
				t.Errorf("decode websocket event: %v", err)
				return false
			}
			dst <- event
			return true
		}
		if !readJSON(sessionUpdate) {
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.updated"}`))
		if !readJSON(inputAudio) {
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"input_audio_buffer.speech_stopped"}`))
		audio := base64.StdEncoding.EncodeToString(float32ToPCM16LE(make([]float32, 2400)))
		delta, _ := json.Marshal(map[string]any{
			"type":          "response.output_audio.delta",
			"delta":         audio,
			"item_id":       "assistant-1",
			"content_index": 0,
		})
		_ = conn.Write(ctx, websocket.MessageText, delta)
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.output_audio.done"}`))
		_, _, _ = conn.Read(ctx) // wait for the bridge to close the call
	}))
	defer server.Close()

	played := make(chan struct{}, 1)
	cfg := openAIStandardConfig{
		APIKey:            "test-key",
		RealtimeURL:       "ws" + strings.TrimPrefix(server.URL, "http"),
		RealtimeModel:     "gpt-realtime-2.1-mini",
		RealtimeVADMode:   "server_vad",
		RealtimeThreshold: 0.5,
		RealtimeSilenceMS: 250,
		RealtimePrefixMS:  120,
		Voice:             "coral",
		Speed:             1,
		Volume:            1,
	}
	bridge := newOpenAIRealtimeVoiceBridge(cfg, func(source meowcaller.AudioSource) {
		if _, err := source.ReadFrame(); err == nil {
			played <- struct{}{}
		}
	}, nil)
	defer bridge.Close()
	if err := bridge.WriteFrame(make([]float32, 960)); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-sessionUpdate:
		if event["type"] != "session.update" {
			t.Fatalf("first event = %#v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Realtime session was not configured")
	}
	select {
	case event := <-inputAudio:
		if event["type"] != "input_audio_buffer.append" || event["audio"] == "" {
			t.Fatalf("audio event = %#v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller audio was not streamed")
	}
	select {
	case <-played:
	case <-time.After(3 * time.Second):
		t.Fatal("Realtime response audio was not played")
	}
}

func TestOpenAIRealtimeIsTheDefaultOpenAIMode(t *testing.T) {
	t.Setenv("OPENAI_REALTIME_ENABLED", "")
	t.Setenv("OPENAI_REALTIME_MODEL", "")
	cfg := loadOpenAIStandardConfig()
	if !cfg.RealtimeEnabled || cfg.RealtimeModel != defaultOpenAIRealtimeModel {
		t.Fatalf("Realtime defaults: enabled=%t model=%q", cfg.RealtimeEnabled, cfg.RealtimeModel)
	}
}

func TestOpenAIRealtimeSessionPinsTranscriptionLanguage(t *testing.T) {
	payload := openAIRealtimeSessionUpdate(openAIStandardConfig{
		RealtimeModel:      "gpt-realtime-2.1-mini",
		TranscriptionModel: "gpt-4o-mini-transcribe",
		Language:           "bn-BD",
	})
	session := payload["session"].(map[string]any)
	transcription := session["audio"].(map[string]any)["input"].(map[string]any)["transcription"].(map[string]any)
	if transcription["model"] != "gpt-4o-mini-transcribe" || transcription["language"] != "bn" {
		t.Fatalf("transcription = %#v", transcription)
	}

	// An agent with no language selected must not pin one.
	payload = openAIRealtimeSessionUpdate(openAIStandardConfig{RealtimeModel: "gpt-realtime-2.1-mini"})
	session = payload["session"].(map[string]any)
	transcription = session["audio"].(map[string]any)["input"].(map[string]any)["transcription"].(map[string]any)
	if _, present := transcription["language"]; present {
		t.Fatalf("transcription pinned a language without one selected: %#v", transcription)
	}
}

// A response's instructions replace the session's, so the greeting has to carry
// the agent's prompt — and with it the language lock — or the call opens in
// English whatever language the agent is set to.
func TestOpenAIRealtimeGreetingKeepsAgentInstructions(t *testing.T) {
	const prompt = "Language: You must speak and understand only Bengali (bn-BD)."

	generated := openAIRealtimeGreetingInstruction(openAIStandardConfig{
		Instructions:     prompt,
		BeginMessageMode: "agent_speaks_first_with_model_generated_message",
	})
	if !strings.Contains(generated, prompt) || !strings.Contains(generated, "Greet the caller now") {
		t.Errorf("model-generated greeting = %q", generated)
	}

	verbatim := openAIRealtimeGreetingInstruction(openAIStandardConfig{
		Instructions:     prompt,
		BeginMessageMode: "agent_speaks_first",
		WelcomeMessage:   "Assalamu alaikum",
	})
	if !strings.Contains(verbatim, prompt) || !strings.Contains(verbatim, "Assalamu alaikum") {
		t.Errorf("verbatim greeting = %q", verbatim)
	}

	// An agent that waits for the caller still opens with nothing.
	if got := openAIRealtimeGreetingInstruction(openAIStandardConfig{
		Instructions:     prompt,
		BeginMessageMode: "agent_waits_for_user",
	}); got != "" {
		t.Errorf("silent mode produced a greeting: %q", got)
	}
}
