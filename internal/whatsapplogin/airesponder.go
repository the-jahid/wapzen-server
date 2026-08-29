package whatsapplogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/chatagents"
)

const (
	openAIResponsesURL   = "https://api.openai.com/v1/responses"
	anthropicMessagesURL = "https://api.anthropic.com/v1/messages"
	chatResponseMaxTok   = 500
	aiMaxAttempts        = 3
	aiRetryDelay         = 800 * time.Millisecond

	// chatMaxToolRounds bounds how many times one reply may call tools before the
	// model is made to answer with what it has. Each round is another round trip
	// the person waits through with the typing indicator on, and a model that
	// keeps reaching for tools has to produce an answer at some point.
	chatMaxToolRounds = 3
)

type aiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// aiResponder is provider-neutral: the active database row selects OpenAI or
// Anthropic for each message, so two phone numbers can run different agents in
// the same server process.
type aiResponder struct {
	openAIKey    string
	anthropicKey string
	// The endpoints are fields rather than constants so a deployment can point
	// them at a proxy — the same OPENAI_RESPONSES_URL and ANTHROPIC_MESSAGES_URL
	// the call path reads — and so the reply loop can be exercised in tests
	// against a server that answers like one.
	openAIEndpoint    string
	anthropicEndpoint string
	httpClient        *http.Client
}

func newAIResponder() *aiResponder {
	openAIKey := usableAPIKey(os.Getenv("OPENAI_API_KEY"), "openai_replace_me")
	anthropicKey := usableAPIKey(os.Getenv("ANTHROPIC_API_KEY"), "anthropic_replace_me")
	if openAIKey == "" && anthropicKey == "" {
		return nil
	}
	return &aiResponder{
		openAIKey:         openAIKey,
		anthropicKey:      anthropicKey,
		openAIEndpoint:    endpointOrDefault("OPENAI_RESPONSES_URL", openAIResponsesURL),
		anthropicEndpoint: endpointOrDefault("ANTHROPIC_MESSAGES_URL", anthropicMessagesURL),
		httpClient:        &http.Client{Timeout: 30 * time.Second},
	}
}

func endpointOrDefault(env, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(env)); value != "" {
		return value
	}
	return fallback
}

func usableAPIKey(value, placeholder string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == placeholder {
		return ""
	}
	return value
}

func (r *aiResponder) Available(provider string) bool {
	switch provider {
	case "anthropic":
		return r != nil && r.anthropicKey != ""
	default:
		return r != nil && r.openAIKey != ""
	}
}

// Reply answers one incoming message. When the agent has tools attached they are
// declared to the model and run here, in as many rounds as the model asks for up
// to chatMaxToolRounds; the text of every round is kept, so anything the model
// said before it looked something up is part of the reply rather than lost.
func (r *aiResponder) Reply(ctx context.Context, agent chatagents.LiveAgent, messages []aiMessage, tools *chatToolbox) (string, error) {
	if !r.Available(agent.ModelProvider) {
		return "", fmt.Errorf("%s API key is not configured", agent.ModelProvider)
	}
	instructions := strings.TrimSpace(agent.SystemPrompt)
	if directive := tools.Instructions(); directive != "" {
		instructions = strings.TrimSpace(instructions + "\n\n" + directive)
	}

	if agent.ModelProvider == "anthropic" {
		return r.replyAnthropic(ctx, agent, instructions, messages, tools)
	}
	return r.replyOpenAI(ctx, agent, instructions, messages, tools)
}

// replyOpenAI answers over the Responses API. Each tool round appends the
// model's function_call and the result to the same input list and asks for a
// fresh response over it, which is the shape that API expects a tool result in.
func (r *aiResponder) replyOpenAI(ctx context.Context, agent chatagents.LiveAgent, instructions string, messages []aiMessage, tools *chatToolbox) (string, error) {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		input = append(input, map[string]string{"role": message.Role, "content": message.Content})
	}

	var parts []string
	for round := 0; ; round++ {
		payload := map[string]any{
			"model": agent.ModelName, "instructions": instructions, "input": input,
			"temperature": agent.ModelTemperature, "max_output_tokens": chatResponseMaxTok,
		}
		// The tool definitions stay on the request even in the final round: the
		// input already carries function calls by then, and the API rejects those
		// unless the tools they name are declared. tool_choice is what ends the
		// loop.
		attachOpenAITools(payload, tools, round >= chatMaxToolRounds)

		body, err := r.post(ctx, "openai", r.openAIEndpoint, r.openAIKey, payload)
		if err != nil {
			return "", err
		}
		text, calls, err := extractOpenAIText(body)
		if err != nil {
			return "", err
		}
		if text != "" {
			parts = append(parts, text)
		}
		if len(calls) == 0 || tools == nil || round >= chatMaxToolRounds {
			break
		}
		for _, call := range calls {
			log.Printf("%s calling tool %s", tools.label, call.Name)
			input = append(input, call.item, map[string]any{
				"type":    "function_call_output",
				"call_id": call.CallID,
				"output":  tools.Run(ctx, call.Name, call.Arguments),
			})
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// replyAnthropic is replyOpenAI's twin for the Messages API: the model's own
// turn is appended verbatim and the tool results come back as a user turn of
// tool_result blocks, which is what that API expects.
func (r *aiResponder) replyAnthropic(ctx context.Context, agent chatagents.LiveAgent, instructions string, messages []aiMessage, tools *chatToolbox) (string, error) {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		input = append(input, message)
	}

	var parts []string
	for round := 0; ; round++ {
		payload := map[string]any{
			"model": agent.ModelName, "system": instructions, "messages": input,
			"temperature": agent.ModelTemperature, "max_tokens": chatResponseMaxTok,
		}
		attachAnthropicTools(payload, tools, round >= chatMaxToolRounds)

		body, err := r.post(ctx, "anthropic", r.anthropicEndpoint, r.anthropicKey, payload)
		if err != nil {
			return "", err
		}
		text, uses, content, err := extractAnthropicText(body)
		if err != nil {
			return "", err
		}
		if text != "" {
			parts = append(parts, text)
		}
		if len(uses) == 0 || tools == nil || round >= chatMaxToolRounds {
			break
		}
		results := make([]any, 0, len(uses))
		for _, use := range uses {
			log.Printf("%s calling tool %s", tools.label, use.Name)
			results = append(results, map[string]any{
				"type":        "tool_result",
				"tool_use_id": use.ID,
				"content":     tools.Run(ctx, use.Name, string(use.arguments())),
			})
		}
		input = append(input,
			map[string]any{"role": "assistant", "content": content},
			map[string]any{"role": "user", "content": results},
		)
	}
	return strings.Join(parts, "\n\n"), nil
}

// attachOpenAITools declares the available tools on a Responses request. When
// final is set the model is barred from calling them again, which is how a reply
// that keeps reaching for tools is forced to answer instead.
func attachOpenAITools(payload map[string]any, box *chatToolbox, final bool) {
	definitions := box.Definitions()
	if len(definitions) == 0 {
		return
	}
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        definition.Name,
			"description": definition.Description,
			"parameters":  definition.Parameters,
		})
	}
	payload["tools"] = tools
	if final {
		payload["tool_choice"] = "none"
	} else {
		payload["tool_choice"] = "auto"
	}
}

// attachAnthropicTools declares the available tools on a Messages request, with
// the same final-round rule as attachOpenAITools.
func attachAnthropicTools(payload map[string]any, box *chatToolbox, final bool) {
	definitions := box.Definitions()
	if len(definitions) == 0 {
		return
	}
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"name":         definition.Name,
			"description":  definition.Description,
			"input_schema": definition.Parameters,
		})
	}
	payload["tools"] = tools
	if final {
		payload["tool_choice"] = map[string]any{"type": "none"}
	} else {
		payload["tool_choice"] = map[string]any{"type": "auto"}
	}
}

// post sends one request and returns the raw response body, retrying the
// failures that are worth retrying: a 5xx, a rate limit, and a connection that
// did not complete.
func (r *aiResponder) post(ctx context.Context, provider, endpoint, key string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= aiMaxAttempts; attempt++ {
		responseBody, retryable, err := r.attempt(ctx, provider, endpoint, key, body)
		if err == nil {
			return responseBody, nil
		}
		lastErr = err
		if !retryable || attempt == aiMaxAttempts {
			break
		}
		log.Printf("whatsapp chat agent: %s attempt %d/%d failed, retrying: %v", provider, attempt, aiMaxAttempts, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(aiRetryDelay * time.Duration(attempt)):
		}
	}
	return nil, lastErr
}

func (r *aiResponder) attempt(ctx context.Context, provider, endpoint, key string, body []byte) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if provider == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode >= 500 || resp.StatusCode == 429,
			fmt.Errorf("%s status %s: %s", provider, resp.Status, strings.TrimSpace(string(responseBody)))
	}
	return responseBody, false, nil
}

// openAIToolCall is one function call the model emitted, kept alongside the raw
// output item it came from: the item has to be echoed back verbatim in the next
// request for the API to accept its result.
type openAIToolCall struct {
	item      json.RawMessage
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Type      string `json:"type"`
}

// extractOpenAIText reads one Responses reply: the text the model wrote and the
// tool calls it ended with, either of which may be absent.
func extractOpenAIText(body []byte) (string, []openAIToolCall, error) {
	var parsed struct {
		OutputText string            `json:"output_text"`
		Output     []json.RawMessage `json:"output"`
		Error      *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", nil, err
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", nil, errors.New(parsed.Error.Message)
	}

	var calls []openAIToolCall
	var parts []string
	for _, item := range parsed.Output {
		var decoded struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(item, &decoded); err != nil {
			continue
		}
		if decoded.Type == "function_call" {
			var call openAIToolCall
			if json.Unmarshal(item, &call) == nil && call.CallID != "" {
				call.item = item
				calls = append(calls, call)
			}
			continue
		}
		for _, content := range decoded.Content {
			if text := strings.TrimSpace(content.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}

	// output_text is the whole reply already assembled, so it is preferred; the
	// per-item text above covers the responses that do not carry it.
	if text := strings.TrimSpace(parsed.OutputText); text != "" {
		return text, calls, nil
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), calls, nil
}

// anthropicToolUse is one tool the model asked for, in the Messages API's shape.
type anthropicToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// arguments is the tool's input as JSON. A tool called with no arguments has no
// input at all, which is an empty object rather than invalid JSON.
func (u anthropicToolUse) arguments() json.RawMessage {
	if raw := strings.TrimSpace(string(u.Input)); raw != "" && raw != "null" {
		return json.RawMessage(raw)
	}
	return json.RawMessage("{}")
}

// extractAnthropicText reads one Messages reply: the text, the tool calls, and
// the content blocks themselves, which are echoed back verbatim as the model's
// turn when a tool result is attached to it.
func extractAnthropicText(body []byte) (string, []anthropicToolUse, []json.RawMessage, error) {
	var parsed struct {
		Content []json.RawMessage `json:"content"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", nil, nil, err
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", nil, nil, errors.New(parsed.Error.Message)
	}

	var uses []anthropicToolUse
	var parts []string
	for _, block := range parsed.Content {
		var decoded struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &decoded); err != nil {
			continue
		}
		switch decoded.Type {
		case "text":
			if text := strings.TrimSpace(decoded.Text); text != "" {
				parts = append(parts, text)
			}
		case "tool_use":
			var use anthropicToolUse
			if json.Unmarshal(block, &use) == nil && use.ID != "" {
				uses = append(uses, use)
			}
		}
	}
	return strings.Join(parts, "\n"), uses, parsed.Content, nil
}
