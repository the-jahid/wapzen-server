package agenttools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// testBodyMaxChars bounds the response body a test hands back to the dashboard.
// It is far more than the model is given, because whoever is testing a tool
// needs to see what the endpoint really sent, not the excerpt the agent reads.
const testBodyMaxChars = 20000

// TestRequest is the request a test sent, as the endpoint received it.
type TestRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body,omitempty"`
}

// TestResult is one test run of an api_request tool: what went out, what came
// back, and the exact text an agent would have read in its place.
type TestResult struct {
	// OK is true when the endpoint answered with a 2xx or 3xx status.
	OK          bool         `json:"ok"`
	StatusCode  int          `json:"status_code,omitempty"`
	DurationMs  int64        `json:"duration_ms"`
	Request     *TestRequest `json:"request,omitempty"`
	ContentType string       `json:"content_type,omitempty"`
	Body        string       `json:"body"`
	Truncated   bool         `json:"truncated"`
	// Error explains a request that never got a status: an argument missing, a
	// timeout, a refused address.
	Error string `json:"error,omitempty"`
	// ModelResult is the tool result an agent would read for this response.
	ModelResult string `json:"model_result"`
}

// TestAPIRequest runs an api_request tool once with the given arguments and
// reports everything about the exchange. It goes through the same request
// builder, client and address guard as a live call, so a tool that passes here
// behaves the same way in a conversation.
//
// An async tool is still awaited: the point of a test is to see the response,
// even one a live agent would never wait for.
func (r *Runner) TestAPIRequest(ctx context.Context, tool models.AgentTool, args map[string]any) TestResult {
	cfg := tool.APIRequest
	if cfg == nil || strings.TrimSpace(cfg.URL) == "" {
		return TestResult{Error: "This tool has no URL configured."}
	}
	if missing := MissingRequired(cfg.Parameters, args); len(missing) > 0 {
		return TestResult{
			Error:       "Missing required argument(s): " + strings.Join(missing, ", "),
			ModelResult: missingResult(missing),
		}
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if cfg.TimeoutSeconds <= 0 {
		timeout = models.ToolDefaultTimeoutSeconds * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := BuildRequest(requestCtx, cfg, args)
	if err != nil {
		return TestResult{Error: "The request could not be prepared: " + err.Error()}
	}
	sent := describeRequest(request, cfg)

	startedAt := time.Now()
	response, err := r.client.Do(request)
	elapsed := time.Since(startedAt).Milliseconds()
	if err != nil {
		return TestResult{
			DurationMs:  elapsed,
			Request:     sent,
			Error:       err.Error(),
			ModelResult: "The request could not be completed. Tell them you cannot look that up right now.",
		}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, bodyReadLimit))
	if err != nil {
		return TestResult{
			StatusCode: response.StatusCode,
			DurationMs: elapsed,
			Request:    sent,
			Error:      "The response could not be read: " + err.Error(),
		}
	}
	body := strings.TrimSpace(string(raw))

	result := TestResult{
		OK:          response.StatusCode < 400,
		StatusCode:  response.StatusCode,
		DurationMs:  elapsed,
		Request:     sent,
		ContentType: response.Header.Get("Content-Type"),
		Body:        body,
		ModelResult: resultForModel(response.StatusCode, body),
	}
	if len(body) > testBodyMaxChars {
		result.Body = body[:testBodyMaxChars]
		result.Truncated = true
	}
	if cfg.Async {
		result.ModelResult = "The request was sent. Carry on with the conversation; there is no result to report."
	}
	return result
}

// describeRequest records the request as sent. Header values the tool owner
// typed are echoed back to that same owner, but credentials are masked anyway:
// a test result is the kind of thing that ends up in a screenshot.
func describeRequest(request *http.Request, cfg *models.ToolAPIRequestConfig) *TestRequest {
	sent := &TestRequest{
		Method:  request.Method,
		URL:     request.URL.String(),
		Headers: map[string]string{},
	}
	for key := range request.Header {
		value := request.Header.Get(key)
		if isSecretHeader(key) {
			value = maskSecret(value)
		}
		sent.Headers[key] = value
	}
	if request.GetBody != nil {
		if reader, err := request.GetBody(); err == nil {
			if encoded, err := io.ReadAll(reader); err == nil {
				sent.Body = string(encoded)
			}
		}
	}
	return sent
}

func isSecretHeader(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"authorization", "token", "secret", "key", "cookie", "password"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func maskSecret(value string) string {
	if len(value) <= 8 {
		return "••••"
	}
	return value[:4] + "••••" + value[len(value)-2:]
}
