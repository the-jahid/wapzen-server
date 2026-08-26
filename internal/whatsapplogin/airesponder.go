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
)

// This is a minimal, test-only AI reply layer for incoming WhatsApp text
// messages. It mirrors the OpenAI Responses API call used by meowcaller-test so
// the server can prove end-to-end "message in -> AI reply out" without pulling
// in any new dependency or config. Swap the single Reply() call for another
// provider (e.g. Claude) later if desired.

const (
	openAIResponsesURL     = "https://api.openai.com/v1/responses"
	openAIResponseModel    = "gpt-4o-mini"
	openAIResponseMaxTok   = 200
	openAIResponseInstruct = "You are a helpful WhatsApp assistant. Reply in one or two short, friendly sentences."

	// OpenAI occasionally returns transient 5xx errors; retry a few times with a
	// short linear backoff so a single hiccup does not drop the WhatsApp reply.
	openAIMaxAttempts = 3
	openAIRetryDelay  = 800 * time.Millisecond
)

// aiResponder turns caller text into a short reply via the OpenAI Responses API.
type aiResponder struct {
	apiKey     string
	httpClient *http.Client
}

// newAIResponder returns a responder when OPENAI_API_KEY is set to a real value,
// or nil so message handling stays a no-op when the key is missing/placeholder.
// config.Load() (godotenv) has already populated the process environment, so no
// extra wiring through main.go is needed for this test.
func newAIResponder() *aiResponder {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" || key == "openai_replace_me" {
		return nil
	}
	return &aiResponder{
		apiKey:     key,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Reply sends userText to OpenAI and returns the model's short answer, retrying
// transient 5xx/transport failures a few times before giving up.
func (r *aiResponder) Reply(ctx context.Context, userText string) (string, error) {
	payload := map[string]any{
		"model":             openAIResponseModel,
		"instructions":      openAIResponseInstruct,
		"input":             userText,
		"max_output_tokens": openAIResponseMaxTok,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 1; attempt <= openAIMaxAttempts; attempt++ {
		text, retryable, err := r.attempt(ctx, body)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable || attempt == openAIMaxAttempts {
			break
		}
		log.Printf("whatsapp ai: openai attempt %d/%d failed, retrying: %v", attempt, openAIMaxAttempts, err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(openAIRetryDelay * time.Duration(attempt)):
		}
	}
	return "", lastErr
}

// attempt performs one OpenAI request. retryable is true when the failure is a
// transient 5xx status or a transport error worth retrying.
func (r *aiResponder) attempt(ctx context.Context, body []byte) (reply string, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIResponsesURL, bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		// Transport errors (timeouts, resets) are worth another try.
		return "", true, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", true, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode >= 500, fmt.Errorf("openai status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var parsed openAIResponsesResult
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, err
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", false, errors.New(parsed.Error.Message)
	}
	return extractOpenAIText(parsed), false, nil
}

type openAIResponsesResult struct {
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

// extractOpenAIText prefers the convenience output_text field and falls back to
// walking the structured output blocks, matching the raw Responses API shape.
func extractOpenAIText(resp openAIResponsesResult) string {
	if text := strings.TrimSpace(resp.OutputText); text != "" {
		return text
	}
	var parts []string
	for _, item := range resp.Output {
		for _, content := range item.Content {
			if text := strings.TrimSpace(content.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
