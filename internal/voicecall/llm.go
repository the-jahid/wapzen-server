package voicecall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultOpenAIResponsesURL = "https://api.openai.com/v1/responses"
	defaultAnthropicURL       = "https://api.anthropic.com/v1/messages"
	defaultTextModel          = "gpt-4.1-mini"
	maxConversationMessages   = 20

	// maxToolRounds bounds how many times one turn may call tools before the
	// model is made to answer with what it has. Each round is another round trip
	// the caller waits through in silence, and one lookup answers almost every
	// question, so the budget is small on purpose.
	maxToolRounds = 2
)

type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type conversationLLM struct {
	openAIKey      string
	anthropicKey   string
	openAIEndpoint string
	claudeEndpoint string
	httpClient     *http.Client

	// tools are the functions the model may call while answering, or nil when it
	// answers from the conversation alone. It is per call rather than per server:
	// the config holding this LLM is copied for each call, so setting it here
	// scopes an agent's knowledge bases to that agent's calls.
	tools toolRunner
}

// withTools returns a copy of the client that offers tools to the model. A nil
// runner returns the client unchanged, so a call with no tools takes exactly the
// path it did before.
func (l conversationLLM) withTools(tools toolRunner) conversationLLM {
	if tools == nil {
		return l
	}
	l.tools = tools
	return l
}

func loadConversationLLM() conversationLLM {
	return conversationLLM{
		openAIKey:      strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		anthropicKey:   anthropicAPIKey(),
		openAIEndpoint: envString("OPENAI_RESPONSES_URL", defaultOpenAIResponsesURL),
		claudeEndpoint: envString("ANTHROPIC_MESSAGES_URL", defaultAnthropicURL),
		httpClient:     voiceHTTPClient(45 * time.Second),
	}
}

func anthropicAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(os.Getenv("CLAUDE_API_KEY"))
}

func (l conversationLLM) Enabled(provider string) bool {
	switch normalizeLLMProvider(provider) {
	case "anthropic":
		return l.anthropicKey != "" && l.anthropicKey != "anthropic_replace_me"
	case "openai":
		return l.openAIKey != "" && l.openAIKey != placeholderAPIKey
	default:
		return false
	}
}

// endpointFor reports the URL a reply for provider will be sent to, so a bridge
// can warm that connection before the first turn.
func (l conversationLLM) endpointFor(provider string) string {
	if normalizeLLMProvider(provider) == "anthropic" {
		return l.claudeEndpoint
	}
	return l.openAIEndpoint
}

func normalizeLLMProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return "anthropic"
	case "", "openai":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func (l conversationLLM) Reply(ctx context.Context, provider, model string, temperature float64, instructions string, messages []conversationMessage) (string, error) {
	switch normalizeLLMProvider(provider) {
	case "anthropic":
		return l.replyAnthropic(ctx, model, temperature, instructions, messages)
	case "openai":
		return l.replyOpenAI(ctx, model, temperature, instructions, messages)
	default:
		return "", fmt.Errorf("unsupported LLM provider %q", provider)
	}
}

// openAIResponsesInput renders the conversation as Responses API input items.
// It returns []any because a turn that calls tools appends items of other
// shapes — the model's function_call and its output — to the same list.
func openAIResponsesInput(messages []conversationMessage) []any {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		if text := strings.TrimSpace(message.Content); text != "" {
			input = append(input, map[string]string{"role": message.Role, "content": text})
		}
	}
	return input
}

func openAIResponsesPayload(model string, temperature float64, instructions string, input []any) map[string]any {
	if model = strings.TrimSpace(model); model == "" {
		model = defaultTextModel
	}
	payload := map[string]any{
		"model":             model,
		"instructions":      instructions,
		"input":             input,
		"max_output_tokens": 256,
		"store":             false,
	}
	if temperature >= 0 && temperature <= 2 && sendsTemperature("openai", model) {
		payload["temperature"] = temperature
	}
	applyVoiceTuning(payload, tuningFor("openai", model))
	return payload
}

func (l conversationLLM) openAIHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + l.openAIKey}
}

func (l conversationLLM) replyOpenAI(ctx context.Context, model string, temperature float64, instructions string, messages []conversationMessage) (string, error) {
	payload := openAIResponsesPayload(model, temperature, instructions, openAIResponsesInput(messages))
	body, err := l.postJSON(ctx, l.openAIEndpoint, l.openAIHeaders(), payload)
	for attempt := 0; attempt < maxPayloadRetries && retryAfterRejection("openai", payload, err); attempt++ {
		body, err = l.postJSON(ctx, l.openAIEndpoint, l.openAIHeaders(), payload)
	}
	if err != nil {
		return "", fmt.Errorf("OpenAI response: %w", err)
	}
	var result struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode OpenAI response: %w", err)
	}
	if result.Error != nil && result.Error.Message != "" {
		return "", fmt.Errorf("OpenAI: %s", result.Error.Message)
	}
	if text := strings.TrimSpace(result.OutputText); text != "" {
		return text, nil
	}
	var parts []string
	for _, output := range result.Output {
		for _, content := range output.Content {
			if text := strings.TrimSpace(content.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
		return text, nil
	}
	return "", fmt.Errorf("OpenAI returned no text")
}

// anthropicInput renders the conversation as Messages API messages. Like its
// OpenAI counterpart it returns []any, because a turn that calls tools appends
// messages whose content is a block list rather than a string.
func anthropicInput(messages []conversationMessage) []any {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		if (message.Role == "user" || message.Role == "assistant") && strings.TrimSpace(message.Content) != "" {
			input = append(input, message)
		}
	}
	return input
}

func anthropicPayload(model string, temperature float64, instructions string, input []any) map[string]any {
	payload := map[string]any{
		"model":      model,
		"system":     instructions,
		"messages":   input,
		"max_tokens": 256,
	}
	if temperature >= 0 && temperature <= 1 && sendsTemperature("anthropic", model) {
		payload["temperature"] = temperature
	}
	return payload
}

func (l conversationLLM) anthropicHeaders() map[string]string {
	return map[string]string{
		"x-api-key":         l.anthropicKey,
		"anthropic-version": "2023-06-01",
	}
}

func (l conversationLLM) replyAnthropic(ctx context.Context, model string, temperature float64, instructions string, messages []conversationMessage) (string, error) {
	if model = strings.TrimSpace(model); model == "" {
		return "", fmt.Errorf("Anthropic model is required")
	}
	payload := anthropicPayload(model, temperature, instructions, anthropicInput(messages))
	body, err := l.postJSON(ctx, l.claudeEndpoint, l.anthropicHeaders(), payload)
	for attempt := 0; attempt < maxPayloadRetries && retryAfterRejection("anthropic", payload, err); attempt++ {
		body, err = l.postJSON(ctx, l.claudeEndpoint, l.anthropicHeaders(), payload)
	}
	if err != nil {
		return "", fmt.Errorf("Anthropic response: %w", err)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode Anthropic response: %w", err)
	}
	if result.Error != nil && result.Error.Message != "" {
		return "", fmt.Errorf("Anthropic: %s", result.Error.Message)
	}
	var parts []string
	for _, content := range result.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			parts = append(parts, strings.TrimSpace(content.Text))
		}
	}
	if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
		return text, nil
	}
	return "", fmt.Errorf("Anthropic returned no text")
}

// ReplyStream is Reply with the answer delivered while it is being written:
// onDelta receives each text fragment as the model emits it, and the complete
// reply is returned once the stream ends. The voice bridges speak the first
// clause instead of waiting for the last token, which takes the model's
// generation time almost entirely out of what the caller waits through.
func (l conversationLLM) ReplyStream(ctx context.Context, provider, model string, temperature float64, instructions string, messages []conversationMessage, onDelta func(string)) (string, error) {
	switch normalizeLLMProvider(provider) {
	case "anthropic":
		return l.streamAnthropic(ctx, model, temperature, instructions, messages, onDelta)
	case "openai":
		return l.streamOpenAI(ctx, model, temperature, instructions, messages, onDelta)
	default:
		return "", fmt.Errorf("unsupported LLM provider %q", provider)
	}
}

// streamOpenAI streams one reply, running any tools the model asks for along
// the way. Each tool round appends the model's function_call and the result to
// the same input list and streams a fresh response over it, so the text the
// caller hears is the whole turn's output in order — including anything the
// model said before it looked something up.
func (l conversationLLM) streamOpenAI(ctx context.Context, model string, temperature float64, instructions string, messages []conversationMessage, onDelta func(string)) (string, error) {
	input := openAIResponsesInput(messages)

	var text strings.Builder
	emit := func(delta string) {
		if delta == "" {
			return
		}
		text.WriteString(delta)
		if onDelta != nil {
			onDelta(delta)
		}
	}

	for round := 0; ; round++ {
		payload := openAIResponsesPayload(model, temperature, instructions, input)
		payload["stream"] = true
		// The tool definitions stay on the request even in the final round: the
		// input already carries function calls by then, and the API rejects those
		// unless the tools they name are declared. tool_choice is what actually
		// ends the loop.
		l.attachOpenAITools(payload, round >= maxToolRounds)

		calls, err := l.streamOpenAIRound(ctx, payload, emit, &text)
		if err != nil {
			return "", err
		}
		if len(calls) == 0 || l.tools == nil || round >= maxToolRounds {
			break
		}
		for _, call := range calls {
			input = append(input, call.item, map[string]any{
				"type":    "function_call_output",
				"call_id": call.CallID,
				"output":  l.tools.Run(ctx, call.Name, call.Arguments),
			})
		}
	}

	if result := strings.TrimSpace(text.String()); result != "" {
		return result, nil
	}
	return "", fmt.Errorf("OpenAI returned no text")
}

// attachOpenAITools declares the available tools on a Responses request. When
// final is set the model is barred from calling them again, which is how a turn
// that keeps reaching for tools is forced to produce an answer instead.
func (l conversationLLM) attachOpenAITools(payload map[string]any, final bool) {
	if l.tools == nil {
		return
	}
	definitions := l.tools.Definitions()
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

// streamOpenAIRound streams one response, emitting its text as it arrives and
// returning any tool calls it ended with.
func (l conversationLLM) streamOpenAIRound(ctx context.Context, payload map[string]any, emit func(string), text *strings.Builder) ([]openAIToolCall, error) {
	resp, err := l.postStream(ctx, l.openAIEndpoint, l.openAIHeaders(), payload)
	for attempt := 0; attempt < maxPayloadRetries && retryAfterRejection("openai", payload, err); attempt++ {
		resp, err = l.postStream(ctx, l.openAIEndpoint, l.openAIHeaders(), payload)
	}
	if err != nil {
		return nil, fmt.Errorf("OpenAI response: %w", err)
	}
	defer resp.Body.Close()

	textAtStart := text.Len()
	var calls []openAIToolCall
	seen := make(map[string]bool)
	// Items arrive both one at a time and again in the terminal event, so a call
	// is recorded once, by id, whichever event carried it first.
	collect := func(item json.RawMessage) {
		var call openAIToolCall
		if json.Unmarshal(item, &call) != nil || call.Type != "function_call" || call.CallID == "" || seen[call.CallID] {
			return
		}
		seen[call.CallID] = true
		call.item = item
		calls = append(calls, call)
	}

	err = scanSSE(resp.Body, func(data []byte) error {
		var event struct {
			Type string `json:"type"`
			// Only the text-delta event carries a string here; other event types
			// use the same field name for other shapes, so it stays raw until
			// the type is known.
			Delta    json.RawMessage `json:"delta"`
			Text     string          `json:"text"`
			Item     json.RawMessage `json:"item"`
			Error    *apiError       `json:"error"`
			Response *struct {
				Error  *apiError         `json:"error"`
				Output []json.RawMessage `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return nil
		}
		switch event.Type {
		case "response.output_text.delta":
			var delta string
			if json.Unmarshal(event.Delta, &delta) == nil {
				emit(delta)
			}
		case "response.output_text.done":
			// Only used when no deltas arrived at all.
			if text.Len() == textAtStart {
				emit(event.Text)
			}
		case "response.output_item.done":
			collect(event.Item)
		case "response.completed":
			if event.Response != nil {
				for _, item := range event.Response.Output {
					collect(item)
				}
			}
		case "error", "response.failed", "response.incomplete":
			message := event.Error.message()
			if message == "" && event.Response != nil {
				message = event.Response.Error.message()
			}
			if message == "" {
				message = strings.TrimPrefix(event.Type, "response.")
			}
			return fmt.Errorf("OpenAI: %s", message)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return calls, nil
}

// streamAnthropic is streamOpenAI's twin for the Messages API: it streams one
// reply and, when the model asks for a tool, appends its own turn and the tool
// results to the conversation and streams the continuation.
func (l conversationLLM) streamAnthropic(ctx context.Context, model string, temperature float64, instructions string, messages []conversationMessage, onDelta func(string)) (string, error) {
	if model = strings.TrimSpace(model); model == "" {
		return "", fmt.Errorf("Anthropic model is required")
	}
	input := anthropicInput(messages)

	var text strings.Builder
	emit := func(delta string) {
		if delta == "" {
			return
		}
		text.WriteString(delta)
		if onDelta != nil {
			onDelta(delta)
		}
	}

	for round := 0; ; round++ {
		payload := anthropicPayload(model, temperature, instructions, input)
		payload["stream"] = true
		// As on the OpenAI side, the tools stay declared through the final round
		// because the conversation already refers to them; tool_choice is what
		// stops another call.
		l.attachAnthropicTools(payload, round >= maxToolRounds)

		blocks, err := l.streamAnthropicRound(ctx, payload, emit)
		if err != nil {
			return "", err
		}
		toolUses := anthropicToolUses(blocks)
		if len(toolUses) == 0 || l.tools == nil || round >= maxToolRounds {
			break
		}

		results := make([]any, 0, len(toolUses))
		for _, use := range toolUses {
			results = append(results, map[string]any{
				"type":        "tool_result",
				"tool_use_id": use.id,
				"content":     l.tools.Run(ctx, use.name, string(use.input())),
			})
		}
		input = append(input,
			map[string]any{"role": "assistant", "content": anthropicContentBlocks(blocks)},
			map[string]any{"role": "user", "content": results},
		)
	}

	if result := strings.TrimSpace(text.String()); result != "" {
		return result, nil
	}
	return "", fmt.Errorf("Anthropic returned no text")
}

// attachAnthropicTools declares the available tools on a Messages request, with
// the same final-round rule as attachOpenAITools.
func (l conversationLLM) attachAnthropicTools(payload map[string]any, final bool) {
	if l.tools == nil {
		return
	}
	definitions := l.tools.Definitions()
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

// anthropicBlock is one content block of the model's turn, assembled from the
// stream. Text and tool arguments both arrive in fragments, so both are built
// up here and read back once the block is complete.
type anthropicBlock struct {
	kind string
	text strings.Builder
	id   string
	name string
	args strings.Builder
}

// input is the tool's arguments as JSON. A tool called with no arguments emits
// no fragments at all, which is an empty object rather than invalid JSON.
func (b *anthropicBlock) input() json.RawMessage {
	if raw := strings.TrimSpace(b.args.String()); raw != "" {
		return json.RawMessage(raw)
	}
	return json.RawMessage("{}")
}

func anthropicToolUses(blocks []*anthropicBlock) []*anthropicBlock {
	var uses []*anthropicBlock
	for _, block := range blocks {
		if block.kind == "tool_use" && block.id != "" {
			uses = append(uses, block)
		}
	}
	return uses
}

// anthropicContentBlocks renders the model's turn back into the request shape,
// which is what a tool result has to be attached to.
func anthropicContentBlocks(blocks []*anthropicBlock) []any {
	content := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.kind {
		case "text":
			if text := block.text.String(); strings.TrimSpace(text) != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
		case "tool_use":
			if block.id == "" {
				continue
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    block.id,
				"name":  block.name,
				"input": block.input(),
			})
		}
	}
	return content
}

// streamAnthropicRound streams one message, emitting its text as it arrives and
// returning the content blocks it produced, in order.
func (l conversationLLM) streamAnthropicRound(ctx context.Context, payload map[string]any, emit func(string)) ([]*anthropicBlock, error) {
	resp, err := l.postStream(ctx, l.claudeEndpoint, l.anthropicHeaders(), payload)
	for attempt := 0; attempt < maxPayloadRetries && retryAfterRejection("anthropic", payload, err); attempt++ {
		resp, err = l.postStream(ctx, l.claudeEndpoint, l.anthropicHeaders(), payload)
	}
	if err != nil {
		return nil, fmt.Errorf("Anthropic response: %w", err)
	}
	defer resp.Body.Close()

	var blocks []*anthropicBlock
	byIndex := make(map[int]*anthropicBlock)
	openBlock := func(index int, kind, id, name string) *anthropicBlock {
		block := &anthropicBlock{kind: kind, id: id, name: name}
		byIndex[index] = block
		blocks = append(blocks, block)
		return block
	}

	err = scanSSE(resp.Body, func(data []byte) error {
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Error *apiError `json:"error"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return nil
		}
		switch event.Type {
		case "content_block_start":
			openBlock(event.Index, event.ContentBlock.Type, event.ContentBlock.ID, event.ContentBlock.Name)
		case "content_block_delta":
			// Text is spoken as it arrives, so a delta whose block start was never
			// seen still opens one rather than being dropped: losing a word of the
			// reply is worse than tracking a block the model did not announce.
			block := byIndex[event.Index]
			if block == nil {
				block = openBlock(event.Index, "text", "", "")
			}
			switch event.Delta.Type {
			case "text_delta":
				block.text.WriteString(event.Delta.Text)
				emit(event.Delta.Text)
			case "input_json_delta":
				block.args.WriteString(event.Delta.PartialJSON)
			}
		case "error":
			message := event.Error.message()
			if message == "" {
				message = "stream failed"
			}
			return fmt.Errorf("Anthropic: %s", message)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

type apiError struct {
	Message string `json:"message"`
}

func (e *apiError) message() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

// postStream posts payload and hands back the still-open response body for
// server-sent-event reading. The caller closes the body.
func (l conversationLLM) postStream(ctx context.Context, endpoint string, headers map[string]string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	return resp, nil
}

func (l conversationLLM) postJSON(ctx context.Context, endpoint string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	return responseBody, nil
}
