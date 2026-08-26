package voicecall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
)

func TestSpeechChunkerReleasesOpeningClauseEarly(t *testing.T) {
	var c speechChunker
	got := c.Push("Sure thing, I can help with that. ")
	if len(got) != 1 || got[0] != "Sure thing," {
		t.Fatalf("first release = %#v, want just the opening clause", got)
	}
	// A later clause is held longer so it still sounds like a phrase.
	if chunks := c.Push("Yes. "); len(chunks) != 0 {
		t.Fatalf("short later clause released too early: %#v", chunks)
	}
	if chunks := c.Push("Your appointment is confirmed for Tuesday at ten. "); len(chunks) != 1 {
		t.Fatalf("later sentence releases = %#v, want 1", chunks)
	}
	if rest := c.Flush(); rest != "" {
		t.Fatalf("flush = %q, want empty", rest)
	}
}

// TestSpeechChunkerReleasesShortOpenerInstantly pins the first-chunk tuning:
// a short interjection sets the response time the caller feels, so it must be
// handed to text-to-speech the moment its punctuation arrives.
func TestSpeechChunkerReleasesShortOpenerInstantly(t *testing.T) {
	var c speechChunker
	if chunks := c.Push("Awesome! I"); len(chunks) != 1 || chunks[0] != "Awesome!" {
		t.Fatalf("opener release = %#v, want [\"Awesome!\"]", chunks)
	}
}

func TestSpeechChunkerKeepsDecimalsAndFlushesTail(t *testing.T) {
	var c speechChunker
	if chunks := c.Push("The total is 12.50 for now"); len(chunks) != 0 {
		t.Fatalf("split inside a decimal: %#v", chunks)
	}
	if rest := c.Flush(); rest != "The total is 12.50 for now" {
		t.Fatalf("flush = %q", rest)
	}
}

func TestSpeechChunkerCapsUnpunctuatedOpeningPhrase(t *testing.T) {
	var c speechChunker
	input := "I can certainly help you find the right appointment for your schedule today and"
	chunks := c.Push(input + " ")
	if len(chunks) != 1 || len([]rune(chunks[0])) < firstChunkMaxChars-10 {
		t.Fatalf("opening release = %#v, want one useful phrase", chunks)
	}
	if rest := c.Flush(); rest != "for your schedule today and" {
		t.Fatalf("remaining text = %q, want the unreleased tail", rest)
	}
}

func TestReplyStreamOpenAIEmitsDeltasInOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["stream"] != true {
			t.Errorf("stream = %#v, want true", payload["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range []string{"Hello", " there"} {
			fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", delta)
		}
		// An event whose "delta" is not a string must not break the stream.
		_, _ = io.WriteString(w, "data: {\"type\":\"response.something.delta\",\"delta\":{\"nested\":1}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}
	var deltas []string
	text, err := llm.ReplyStream(context.Background(), "openai", "gpt-4.1-mini", 0.5, "be brief",
		[]conversationMessage{{Role: "user", Content: "hi"}}, func(delta string) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello there" {
		t.Fatalf("text = %q", text)
	}
	if strings.Join(deltas, "|") != "Hello| there" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestReplyStreamAnthropicEmitsTextDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Good \"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"morning\"}}\n\n")
		// message_delta reuses the "delta" field for a different shape.
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := conversationLLM{anthropicKey: "test-key", claudeEndpoint: server.URL, httpClient: server.Client()}
	var deltas []string
	text, err := llm.ReplyStream(context.Background(), "anthropic", "claude-sonnet-5", 0.5, "be brief",
		[]conversationMessage{{Role: "user", Content: "hi"}}, func(delta string) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "Good morning" || len(deltas) != 2 {
		t.Fatalf("text = %q deltas = %#v", text, deltas)
	}
}

func TestReplyStreamRetriesWithoutRejectedTemperature(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		success  string
	}{
		{name: "OpenAI unsupported", provider: "openai", success: "OpenAI works"},
		{name: "Anthropic deprecated", provider: "anthropic", success: "Claude works"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if attempts == 1 {
					if _, sent := payload["temperature"]; !sent {
						t.Error("first request did not include temperature")
					}
					w.WriteHeader(http.StatusBadRequest)
					if tt.provider == "anthropic" {
						_, _ = io.WriteString(w, `{"error":{"message":"temperature is deprecated for this model"}}`)
					} else {
						_, _ = io.WriteString(w, `{"error":{"message":"temperature is not supported with this model"}}`)
					}
					return
				}
				if _, sent := payload["temperature"]; sent {
					t.Error("retry still included temperature")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if tt.provider == "anthropic" {
					fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", tt.success)
				} else {
					fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", tt.success)
				}
			}))
			defer server.Close()

			llm := conversationLLM{
				openAIKey:      "test-key",
				anthropicKey:   "test-key",
				openAIEndpoint: server.URL,
				claudeEndpoint: server.URL,
				httpClient:     server.Client(),
			}
			got, err := llm.ReplyStream(context.Background(), tt.provider, "test-model", 0.3, "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.success || attempts != 2 {
				t.Fatalf("reply = %q attempts = %d", got, attempts)
			}
		})
	}
}

func TestReplyStreamSurfacesStreamedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"message\":\"model overloaded\"}}\n\n")
	}))
	defer server.Close()

	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}
	_, err := llm.ReplyStream(context.Background(), "openai", "gpt-4.1-mini", 0.5, "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("error = %v, want the streamed message", err)
	}
}

// TestOpenAIStandardSpeaksBeforeLLMFinishes is the whole point of streaming the
// reply: the caller must hear the opening sentence while the model is still
// writing the rest, not after it.
func TestOpenAIStandardSpeaksBeforeLLMFinishes(t *testing.T) {
	spoken := make(chan string, 4)
	speech := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode TTS request: %v", err)
		}
		input, _ := payload["input"].(string)
		spoken <- input
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(float32ToPCM16LE(make([]float32, openAITTSSampleRate/10)))
	}))
	defer speech.Close()

	releaseRest := make(chan struct{})
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server cannot flush a stream")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Sure thing, I can help with that. \"}\n\n")
		flusher.Flush()
		<-releaseRest // hold the model's remaining tokens back
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Your booking is confirmed.\"}\n\n")
		flusher.Flush()
	}))
	defer llmServer.Close()

	// Stand in for the call's player, which pulls frames until the source ends.
	played := make(chan struct{}, 4)
	drain := func(source meowcaller.AudioSource) {
		played <- struct{}{}
		go func() {
			for {
				if _, err := source.ReadFrame(); err != nil {
					return
				}
			}
		}()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &openAIStandardVoiceBridge{
		cfg: openAIStandardConfig{
			APIKey:     "test-key",
			SpeechURL:  speech.URL,
			TTSModel:   "gpt-4o-mini-tts",
			Voice:      "coral",
			Speed:      1,
			Volume:     1,
			HTTPClient: speech.Client(),
			LLM: conversationLLM{
				openAIKey:      "test-key",
				openAIEndpoint: llmServer.URL,
				httpClient:     llmServer.Client(),
			},
		},
		ctx:        ctx,
		cancel:     cancel,
		playSource: drain,
	}

	turn := b.beginTurn()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		b.completeLLMTurn(turn, []conversationMessage{{Role: "user", Content: "book me in"}})
	}()

	// The first clause must reach text-to-speech while the model is blocked.
	select {
	case input := <-spoken:
		if input != "Sure thing," {
			t.Fatalf("first synthesized text = %q", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no speech was synthesized before the model finished writing")
	}
	select {
	case <-played:
	case <-time.After(3 * time.Second):
		t.Fatal("audio was not played before the model finished writing")
	}

	close(releaseRest)
	select {
	case input := <-spoken:
		if input != "I can help with that. Your booking is confirmed." {
			t.Fatalf("second synthesized text = %q", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the rest of the reply was never synthesized")
	}

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not complete")
	}

	history := b.historySnapshot()
	if len(history) != 1 || history[0].Content != "Sure thing, I can help with that. Your booking is confirmed." {
		t.Fatalf("history = %#v", history)
	}
}

func TestVADSilenceSecondsClampsToUsableRange(t *testing.T) {
	if got := vadSilenceSeconds(250); got != "0.25" {
		t.Errorf("vadSilenceSeconds(250) = %q", got)
	}
	if got := vadSilenceSeconds(0); got != "0.25" {
		t.Errorf("unset silence = %q, want the default", got)
	}
	if got := vadSilenceSeconds(99999); got != "3.00" {
		t.Errorf("oversized silence = %q, want the clamp", got)
	}
}
