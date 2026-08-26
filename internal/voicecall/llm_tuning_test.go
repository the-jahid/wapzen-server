package voicecall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIPayloadSkipsTemperatureForReasoningModels(t *testing.T) {
	reasoning := openAIResponsesPayload("gpt-5.4-mini", 0.3, "", nil)
	if _, sent := reasoning["temperature"]; sent {
		t.Error("a reasoning model was sent a temperature it will reject")
	}
	effort, ok := reasoning["reasoning"].(map[string]any)
	if !ok || effort["effort"] != "none" {
		t.Errorf("reasoning = %#v, want effort none", reasoning["reasoning"])
	}
	verbosity, ok := reasoning["text"].(map[string]any)
	if !ok || verbosity["verbosity"] != "low" {
		t.Errorf("text = %#v, want verbosity low", reasoning["text"])
	}

	plain := openAIResponsesPayload("gpt-4.1-mini", 0.3, "", nil)
	if plain["temperature"] != 0.3 {
		t.Errorf("temperature = %#v, want it kept for a non-reasoning model", plain["temperature"])
	}
	if _, sent := plain["reasoning"]; sent {
		t.Error("a non-reasoning model was sent a reasoning effort it will reject")
	}
	if _, sent := plain["text"]; sent {
		t.Error("a non-reasoning model was sent a verbosity it will reject")
	}

	// gpt-5-chat-latest carries the family name without the reasoning.
	if _, sent := openAIResponsesPayload("gpt-5-chat-latest", 0.3, "", nil)["reasoning"]; sent {
		t.Error("the chat model was sent a reasoning effort")
	}
}

func TestReplyStreamLearnsTheEffortAModelAccepts(t *testing.T) {
	const model = "gpt-5.9-probe"
	var efforts []string
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := payload["reasoning"].(map[string]any)
		effort, _ := reasoning["effort"].(string)
		efforts = append(efforts, effort)
		if effort == "none" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported value: 'none' is not supported with the 'gpt-5.9-probe' model. Supported values are: 'minimal', 'low', 'medium' and 'high'.","param":"reasoning.effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
	}))
	defer server.Close()

	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}
	got, err := llm.ReplyStream(context.Background(), "openai", model, 0.3, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("reply = %q", got)
	}
	// The rejection named the efforts it takes, so the retry picks the quickest.
	if len(efforts) != 2 || efforts[0] != "none" || efforts[1] != "minimal" {
		t.Fatalf("efforts = %#v, want none then minimal", efforts)
	}

	// The next turn must not pay for that discovery again.
	before := attempts
	if _, err := llm.ReplyStream(context.Background(), "openai", model, 0.3, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if attempts-before != 1 {
		t.Fatalf("second turn made %d requests, want 1", attempts-before)
	}
	if tuning := tuningFor("openai", model); tuning.Effort != "minimal" {
		t.Fatalf("learned tuning = %+v", tuning)
	}
}

func TestReplyStreamDropsReasoningAModelRefusesOutright(t *testing.T) {
	const model = "gpt-5.9-plain"
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, sent := payload["reasoning"]; sent {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'reasoning.effort' is not supported with this model.","param":"reasoning.effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
	}))
	defer server.Close()

	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}
	if _, err := llm.ReplyStream(context.Background(), "openai", model, 0.3, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want the rejection then one retry", attempts)
	}
	if tuning := tuningFor("openai", model); tuning.Effort != "" {
		t.Fatalf("learned tuning = %+v, want the effort dropped", tuning)
	}
}

func TestBestSupportedEffortPrefersTheQuickest(t *testing.T) {
	cases := map[string]string{
		"supported values are: 'none', 'low', 'medium', 'high', 'xhigh', and 'max'.": "none",
		"supported values are: 'minimal', 'low', 'medium' and 'high'.":               "minimal",
		"supported values are: 'low', 'medium' and 'high'.":                          "low",
		"supported values are: 'medium' and 'high'.":                                 "",
		"this message names nothing":                                                 "",
	}
	for message, want := range cases {
		if got := bestSupportedEffort(message); got != want {
			t.Errorf("bestSupportedEffort(%q) = %q, want %q", message, got, want)
		}
	}
}
