package voicecall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadConversationLLMUsesClaudeAPIKeyAlias(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "claude-secret")

	if got := loadConversationLLM().anthropicKey; got != "claude-secret" {
		t.Errorf("anthropic key = %q", got)
	}
}

func TestLoadConversationLLMPrioritizesAnthropicAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("CLAUDE_API_KEY", "claude-secret")

	if got := loadConversationLLM().anthropicKey; got != "anthropic-secret" {
		t.Errorf("anthropic key = %q", got)
	}
}

func TestOpenAIConversationLLMSendsHistoryAndInstructions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "gpt-4.1-mini" || payload["instructions"] != "Be concise." {
			t.Errorf("payload = %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output_text": "Hello there."})
	}))
	defer server.Close()

	llm := conversationLLM{openAIKey: "secret", openAIEndpoint: server.URL, httpClient: server.Client()}
	reply, err := llm.Reply(context.Background(), "openai", "gpt-4.1-mini", 0.3, "Be concise.", []conversationMessage{{Role: "user", Content: "Hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hello there." {
		t.Errorf("reply = %q", reply)
	}
}

func TestAnthropicConversationLLM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("Anthropic headers missing: %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "claude-opus-4-6" {
			t.Errorf("model = %q", payload["model"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "Hi from Claude."}},
		})
	}))
	defer server.Close()

	llm := conversationLLM{anthropicKey: "secret", claudeEndpoint: server.URL, httpClient: server.Client()}
	reply, err := llm.Reply(context.Background(), "anthropic", "claude-opus-4-6", 0.3, "Be concise.", []conversationMessage{{Role: "user", Content: "Hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hi from Claude." {
		t.Errorf("reply = %q", reply)
	}
}
