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
)

// recordingTool answers every call with a fixed result and records what it was
// asked, so a test can prove the arguments survived the stream intact.
type recordingTool struct {
	names     []string
	arguments []string
	result    string
}

func (r *recordingTool) Definitions() []toolDefinition {
	return []toolDefinition{{
		Name:        knowledgeToolName,
		Description: "Search the knowledge base.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
	}}
}

func (r *recordingTool) Run(_ context.Context, name, arguments string) string {
	r.names = append(r.names, name)
	r.arguments = append(r.arguments, arguments)
	return r.result
}

// decodeRequests collects each request body a fake provider received, so a test
// can inspect what the second round was actually sent.
type decodeRequests struct {
	payloads []map[string]any
}

func (d *decodeRequests) record(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Errorf("decode request: %v", err)
	}
	d.payloads = append(d.payloads, payload)
	return payload
}

func TestReplyStreamOpenAIRunsToolsAndAnswers(t *testing.T) {
	var requests decodeRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := requests.record(t, r)
		w.Header().Set("Content-Type", "text/event-stream")

		if len(requests.payloads) == 1 {
			// The model looks the answer up instead of replying.
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"output":[`+
				`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"`+knowledgeToolName+`","arguments":"{\"query\":\"refund window\"}"}`+
				`]}}`+"\n\n")
			return
		}

		// The second round must carry both the model's call and its result.
		input, _ := payload["input"].([]any)
		if len(input) != 3 {
			t.Errorf("second-round input = %#v, want the conversation plus the call and its output", input)
		}
		if output, ok := input[len(input)-1].(map[string]any); !ok || output["type"] != "function_call_output" || output["call_id"] != "call_1" {
			t.Errorf("last input item = %#v, want the tool result", input[len(input)-1])
		} else if !strings.Contains(fmt.Sprint(output["output"]), "five days") {
			t.Errorf("tool result was not passed back: %#v", output["output"])
		}
		for _, delta := range []string{"Refunds take ", "five days."} {
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", delta)
		}
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"output":[]}}`+"\n\n")
	}))
	defer server.Close()

	tool := &recordingTool{result: "[1] Refund policy\nRefunds take five days."}
	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}.withTools(tool)

	var deltas []string
	text, err := llm.ReplyStream(context.Background(), "openai", "gpt-4.1-mini", 0.5, "be brief",
		[]conversationMessage{{Role: "user", Content: "how long do refunds take?"}}, func(delta string) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "Refunds take five days." {
		t.Fatalf("text = %q", text)
	}
	// The reply is still spoken as it is written, so the tool round must not have
	// collapsed the stream into one delta.
	if len(deltas) != 2 {
		t.Fatalf("deltas = %#v, want the answer streamed", deltas)
	}
	if len(tool.arguments) != 1 || !strings.Contains(tool.arguments[0], "refund window") {
		t.Fatalf("tool calls = %#v", tool.arguments)
	}
	if tool.names[0] != knowledgeToolName {
		t.Fatalf("tool name = %q", tool.names[0])
	}
	if tools, ok := requests.payloads[0]["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("first request declared no tools: %#v", requests.payloads[0]["tools"])
	}
}

func TestReplyStreamOpenAIStopsCallingToolsAfterBudget(t *testing.T) {
	var requests decodeRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round := len(requests.payloads)
		payload := requests.record(t, r)
		w.Header().Set("Content-Type", "text/event-stream")

		if payload["tool_choice"] == "none" {
			// Barred from searching again, the model finally answers.
			_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"Here is what I found."}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"output":[]}}`+"\n\n")
			return
		}
		// Every other round asks for another lookup.
		fmt.Fprintf(w, `data: {"type":"response.completed","response":{"output":[`+
			`{"type":"function_call","call_id":"call_%d","name":%q,"arguments":"{\"query\":\"again\"}"}`+
			`]}}`+"\n\n", round, knowledgeToolName)
	}))
	defer server.Close()

	tool := &recordingTool{result: "nothing useful"}
	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}.withTools(tool)

	text, err := llm.ReplyStream(context.Background(), "openai", "gpt-4.1-mini", 0.5, "be brief",
		[]conversationMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Here is what I found." {
		t.Fatalf("text = %q", text)
	}
	// A model that keeps reaching for tools costs the caller a round trip each
	// time, so the budget has to actually bind.
	if len(tool.arguments) != maxToolRounds {
		t.Fatalf("ran %d tool rounds, want at most %d", len(tool.arguments), maxToolRounds)
	}
	if len(requests.payloads) != maxToolRounds+1 {
		t.Fatalf("made %d requests, want %d", len(requests.payloads), maxToolRounds+1)
	}
}

func TestReplyStreamAnthropicRunsToolsAndAnswers(t *testing.T) {
	var requests decodeRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := requests.record(t, r)
		w.Header().Set("Content-Type", "text/event-stream")

		if len(requests.payloads) == 1 {
			// A tool call arrives as a block whose arguments are streamed in
			// fragments, like text.
			_, _ = io.WriteString(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"`+knowledgeToolName+`"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"refund window\"}"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"message_stop"}`+"\n\n")
			return
		}

		messages, _ := payload["messages"].([]any)
		if len(messages) != 3 {
			t.Fatalf("second-round messages = %#v, want the question plus the tool exchange", messages)
		}
		assistant, _ := messages[1].(map[string]any)
		blocks, _ := assistant["content"].([]any)
		if assistant["role"] != "assistant" || len(blocks) != 1 {
			t.Errorf("assistant turn = %#v, want its tool_use echoed back", assistant)
		} else if block, _ := blocks[0].(map[string]any); block["type"] != "tool_use" || block["id"] != "toolu_1" {
			t.Errorf("assistant block = %#v", blocks[0])
		}
		result, _ := messages[2].(map[string]any)
		resultBlocks, _ := result["content"].([]any)
		if result["role"] != "user" || len(resultBlocks) != 1 {
			t.Fatalf("tool result turn = %#v", result)
		}
		block, _ := resultBlocks[0].(map[string]any)
		if block["type"] != "tool_result" || block["tool_use_id"] != "toolu_1" || !strings.Contains(fmt.Sprint(block["content"]), "five days") {
			t.Errorf("tool result block = %#v", block)
		}

		_, _ = io.WriteString(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Refunds take five days."}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	tool := &recordingTool{result: "[1] Refund policy\nRefunds take five days."}
	llm := conversationLLM{anthropicKey: "test-key", claudeEndpoint: server.URL, httpClient: server.Client()}.withTools(tool)

	text, err := llm.ReplyStream(context.Background(), "anthropic", "claude-sonnet-5", 0.5, "be brief",
		[]conversationMessage{{Role: "user", Content: "how long do refunds take?"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Refunds take five days." {
		t.Fatalf("text = %q", text)
	}
	// The fragments have to reassemble into the arguments the model meant.
	if len(tool.arguments) != 1 || tool.arguments[0] != `{"query":"refund window"}` {
		t.Fatalf("tool arguments = %#v", tool.arguments)
	}
	if tools, ok := requests.payloads[0]["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("first request declared no tools: %#v", requests.payloads[0]["tools"])
	}
}

func TestReplyStreamWithoutToolsSendsNone(t *testing.T) {
	var requests decodeRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.record(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"Hello."}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"output":[]}}`+"\n\n")
	}))
	defer server.Close()

	llm := conversationLLM{openAIKey: "test-key", openAIEndpoint: server.URL, httpClient: server.Client()}
	if _, err := llm.ReplyStream(context.Background(), "openai", "gpt-4.1-mini", 0.5, "be brief",
		[]conversationMessage{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	// An agent with no knowledge bases takes exactly the request it always did.
	if _, present := requests.payloads[0]["tools"]; present {
		t.Fatalf("a call with no tools declared some anyway: %#v", requests.payloads[0]["tools"])
	}
	if _, present := requests.payloads[0]["tool_choice"]; present {
		t.Fatalf("tool_choice was sent without tools: %#v", requests.payloads[0]["tool_choice"])
	}
}
