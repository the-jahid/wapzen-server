package whatsapplogin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"whatsapp-ai-caller-server/internal/agenttools"
	"whatsapp-ai-caller-server/internal/chatagents"
	"whatsapp-ai-caller-server/internal/models"
)

func TestExtractProviderResponseText(t *testing.T) {
	openAI, calls, err := extractOpenAIText([]byte(`{"output":[{"content":[{"text":"Hello from OpenAI"}]}]}`))
	if err != nil || openAI != "Hello from OpenAI" {
		t.Fatalf("OpenAI text=%q err=%v", openAI, err)
	}
	if len(calls) != 0 {
		t.Errorf("OpenAI calls = %v, want none", calls)
	}
	anthropic, uses, _, err := extractAnthropicText([]byte(`{"content":[{"type":"text","text":"Hello from Claude"}]}`))
	if err != nil || anthropic != "Hello from Claude" {
		t.Fatalf("Anthropic text=%q err=%v", anthropic, err)
	}
	if len(uses) != 0 {
		t.Errorf("Anthropic uses = %v, want none", uses)
	}
}

// TestExtractProviderToolCalls pins that a reply asking for a tool is read as
// one, in both providers' shapes.
func TestExtractProviderToolCalls(t *testing.T) {
	_, calls, err := extractOpenAIText([]byte(`{"output":[
		{"type":"function_call","call_id":"call_1","name":"get_post","arguments":"{\"id\":\"7\"}"}
	]}`))
	if err != nil {
		t.Fatalf("extractOpenAIText: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "get_post" || calls[0].CallID != "call_1" {
		t.Fatalf("calls = %+v, want one get_post", calls)
	}
	// The raw item is echoed back in the next request, so it has to be kept.
	if !strings.Contains(string(calls[0].item), "function_call") {
		t.Errorf("item = %s, want the output item kept verbatim", calls[0].item)
	}

	_, uses, content, err := extractAnthropicText([]byte(`{"content":[
		{"type":"tool_use","id":"toolu_1","name":"get_post","input":{"id":"7"}}
	]}`))
	if err != nil {
		t.Fatalf("extractAnthropicText: %v", err)
	}
	if len(uses) != 1 || uses[0].Name != "get_post" || uses[0].ID != "toolu_1" {
		t.Fatalf("uses = %+v, want one get_post", uses)
	}
	if len(content) != 1 {
		t.Errorf("content = %v, want the assistant turn kept for the echo", content)
	}
	// A tool called with no arguments must still produce valid JSON.
	empty := anthropicToolUse{ID: "toolu_2", Name: "ping"}
	if got := string(empty.arguments()); got != "{}" {
		t.Errorf("arguments = %q, want {}", got)
	}
}

// TestReplyRunsToolThenAnswers is the whole chat tool loop against servers that
// answer the way the provider and the tool's endpoint do: the model asks for the
// tool, the tool's request goes out, and the result comes back for the model to
// answer from.
func TestReplyRunsToolThenAnswers(t *testing.T) {
	// The tool guard refuses loopback by default, which is exactly what a test
	// server is; the opt-in is the one a self-hosted install uses for internal APIs.
	t.Setenv(agenttools.AllowPrivateTargetsEnv, "true")

	var toolCalls int
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toolCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"title":"Hello world"}`)
	}))
	defer toolServer.Close()

	var rounds int
	var sawToolResult bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(payload.Tools) != 1 || payload.Tools[0].Name != "get_post" {
			t.Errorf("tools = %+v, want get_post declared", payload.Tools)
		}
		rounds++
		w.Header().Set("Content-Type", "application/json")
		if rounds == 1 {
			_, _ = io.WriteString(w, `{"output":[
				{"type":"function_call","call_id":"call_1","name":"get_post","arguments":"{\"id\":\"7\"}"}
			]}`)
			return
		}
		for _, item := range payload.Input {
			if item["type"] == "function_call_output" {
				sawToolResult = strings.Contains(item["output"].(string), "Hello world")
			}
		}
		_, _ = io.WriteString(w, `{"output_text":"The post is called Hello world."}`)
	}))
	defer modelServer.Close()

	responder := &aiResponder{
		openAIKey:      "test-key",
		openAIEndpoint: modelServer.URL,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
	}
	box := newChatToolbox([]models.AgentTool{{
		ID: "tool_1", Type: models.ToolTypeAPIRequest, Name: "get_post", Description: "Reads a post.",
		APIRequest: &models.ToolAPIRequestConfig{
			Method: "GET", URL: toolServer.URL, TimeoutSeconds: 5,
			Parameters: []models.ToolParameter{{Name: "id", Type: "string", Required: true}},
		},
	}}, "chat_agent_1", nil)
	if box == nil {
		t.Fatal("newChatToolbox returned nil for one api_request tool")
	}

	reply, err := responder.Reply(context.Background(), chatagents.LiveAgent{
		ID: "chat_agent_1", ModelProvider: "openai", ModelName: "gpt-4.1-mini",
		ModelTemperature: 0.3, SystemPrompt: "You are helpful.",
	}, []aiMessage{{Role: "user", Content: "what is post 7 called?"}}, box)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if toolCalls != 1 {
		t.Errorf("tool was called %d times, want 1", toolCalls)
	}
	if !sawToolResult {
		t.Error("the tool's response was not handed back to the model")
	}
	if !strings.Contains(reply, "Hello world") {
		t.Errorf("reply = %q, want the answer built from the tool result", reply)
	}
}

// TestReplyWithoutToolsDeclaresNone pins that an agent with nothing attached
// takes exactly the path it did before tools existed.
func TestReplyWithoutToolsDeclaresNone(t *testing.T) {
	var declaredTools bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, declaredTools = payload["tools"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output_text":"Hi there."}`)
	}))
	defer modelServer.Close()

	responder := &aiResponder{
		openAIKey:      "test-key",
		openAIEndpoint: modelServer.URL,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
	}
	reply, err := responder.Reply(context.Background(), chatagents.LiveAgent{
		ID: "chat_agent_1", ModelProvider: "openai", ModelName: "gpt-4.1-mini", SystemPrompt: "You are helpful.",
	}, []aiMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if declaredTools {
		t.Error("tools were declared for an agent that has none")
	}
	if reply != "Hi there." {
		t.Errorf("reply = %q, want Hi there.", reply)
	}
}

// TestChatToolboxDropsCallOnlyTools pins that the two tools that exist to
// release a phone call are not offered in a chat, which has no call to release.
func TestChatToolboxDropsCallOnlyTools(t *testing.T) {
	box := newChatToolbox([]models.AgentTool{
		{ID: "t1", Type: models.ToolTypeEndCall, Name: "end_call", Description: "Hang up."},
		{ID: "t2", Type: models.ToolTypeTransferCall, Name: "transfer_call", Description: "Hand over."},
		{ID: "t3", Type: models.ToolTypeSendText, Name: "send_link", Description: "Send the booking link.",
			SendText: &models.ToolSendTextConfig{Body: "https://example.com/book"}},
	}, "chat_agent_1", nil)

	if box == nil {
		t.Fatal("newChatToolbox returned nil with one usable tool")
	}
	if len(box.order) != 1 || box.order[0] != "send_link" {
		t.Errorf("tools = %v, want only send_link", box.order)
	}

	if onlyCallTools := newChatToolbox([]models.AgentTool{
		{ID: "t1", Type: models.ToolTypeEndCall, Name: "end_call", Description: "Hang up."},
	}, "chat_agent_1", nil); onlyCallTools != nil {
		t.Error("an agent with only call tools must offer none in a chat")
	}
}

// TestChatSendTextUsesConfiguredBody pins that a send_text tool sends its own
// message through the chat's sender rather than folding it into the reply.
func TestChatSendTextUsesConfiguredBody(t *testing.T) {
	var sent []string
	box := newChatToolbox([]models.AgentTool{{
		ID: "t1", Type: models.ToolTypeSendText, Name: "send_link", Description: "Send the booking link.",
		SendText: &models.ToolSendTextConfig{Body: "https://example.com/book"},
	}}, "chat_agent_1", func(_ context.Context, body string) error {
		sent = append(sent, body)
		return nil
	})
	if box == nil {
		t.Fatal("newChatToolbox returned nil for one send_text tool")
	}

	result := box.Run(context.Background(), "send_link", `{"message":"ignored"}`)
	if len(sent) != 1 || sent[0] != "https://example.com/book" {
		t.Fatalf("sent = %v, want the configured body", sent)
	}
	if !strings.Contains(result, "sent") {
		t.Errorf("result = %q, want the send reported to the model", result)
	}
}

func TestTuneAnthropicPayload(t *testing.T) {
	for _, tc := range []struct {
		model        string
		wantTemp     bool
		wantThinking bool
	}{
		{"claude-opus-5", false, true},
		{"claude-sonnet-5", false, true},
		{"claude-fable-5-1", false, true},
		{"claude-opus-4-8", false, false},
		{"claude-sonnet-4-6", true, false},
		{"claude-haiku-4-5-20251001", true, false},
	} {
		payload := map[string]any{"max_tokens": chatResponseMaxTok}
		tuneAnthropicPayload(payload, tc.model, 0.3)
		if _, ok := payload["temperature"]; ok != tc.wantTemp {
			t.Errorf("%s: temperature sent = %v, want %v", tc.model, ok, tc.wantTemp)
		}
		if _, ok := payload["output_config"]; ok != tc.wantThinking {
			t.Errorf("%s: output_config sent = %v, want %v", tc.model, ok, tc.wantThinking)
		}
	}
}

// TestReplyAnthropicRecoversFromStaleModelSettings pins that a saved model the
// API no longer serves as configured still gets an answer: temperature is
// dropped when refused, and a retired model falls back to a current one.
func TestReplyAnthropicRecoversFromStaleModelSettings(t *testing.T) {
	var models []string
	var temperatures []bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		model, _ := payload["model"].(string)
		_, sentTemperature := payload["temperature"]
		models = append(models, model)
		temperatures = append(temperatures, sentTemperature)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case model == "claude-retired-1":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-retired-1"}}`)
		case model == "claude-strict-1" && sentTemperature:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"`temperature` is deprecated for this model.\"}}")
		default:
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Hello."}]}`)
		}
	}))
	defer modelServer.Close()

	responder := &aiResponder{
		anthropicKey:      "test-key",
		anthropicEndpoint: modelServer.URL,
		httpClient:        &http.Client{Timeout: 10 * time.Second},
	}
	for _, tc := range []struct {
		model      string
		wantModels []string
		wantTemps  []bool
	}{
		{"claude-strict-1", []string{"claude-strict-1", "claude-strict-1"}, []bool{true, false}},
		// Remembered: the next reply skips the refused temperature outright.
		{"claude-strict-1", []string{"claude-strict-1"}, []bool{false}},
		{"claude-retired-1", []string{"claude-retired-1", anthropicFallbackModel}, []bool{true, false}},
		{"claude-retired-1", []string{anthropicFallbackModel}, []bool{false}},
	} {
		models, temperatures = nil, nil
		reply, err := responder.Reply(context.Background(), chatagents.LiveAgent{
			ID: "chat_agent_1", ModelProvider: "anthropic", ModelName: tc.model, ModelTemperature: 0.3, SystemPrompt: "You are helpful.",
		}, []aiMessage{{Role: "user", Content: "hello"}}, nil)
		if err != nil || reply != "Hello." {
			t.Fatalf("%s: Reply = %q, %v; want Hello.", tc.model, reply, err)
		}
		if strings.Join(models, ",") != strings.Join(tc.wantModels, ",") {
			t.Errorf("%s: requested models %v, want %v", tc.model, models, tc.wantModels)
		}
		for i := range temperatures {
			if i < len(tc.wantTemps) && temperatures[i] != tc.wantTemps[i] {
				t.Errorf("%s: request %d sent temperature = %v, want %v", tc.model, i, temperatures[i], tc.wantTemps[i])
			}
		}
	}
}
