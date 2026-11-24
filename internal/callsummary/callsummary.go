// Package callsummary writes a short summary of each voice call with Gemini
// (generateContent over REST) and stores it in calls.summary (whiskey
// migration 20261123000003_call_summaries).
//
// Summaries are written automatically once a call's transcript is saved
// (Summarizer.AfterTranscript, registered on the calls repository at
// startup), and on demand from the dashboard via
// POST /v1/dashboard/calls/{call_id}/summary for older calls or to redo one.
//
// Configuration: GEMINI_API_KEY (or GOOGLE_API_KEY), the same key the Gemini
// Live voice engine uses; GEMINI_SUMMARY_MODEL overrides the model
// (default gemini-3.8-flash). Without a key the feature is off: nothing runs
// after calls and the endpoint answers 503.
package callsummary

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

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/calls"
	"whatsapp-ai-caller-server/internal/httpx"
	"whatsapp-ai-caller-server/internal/models"
)

const (
	defaultModel   = "gemini-3.8-flash"
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	// maxTranscriptChars bounds what is sent; very long calls keep their
	// opening and their end, which is where intent and outcome live.
	maxTranscriptChars = 60000
	autoTimeout        = 90 * time.Second
	// maxConcurrent caps background summaries so a burst of call endings
	// cannot open unbounded requests.
	maxConcurrent = 4
)

const instructions = `You summarise phone calls between a business's AI voice agent and a contact, for the business owner reading their dashboard.

Write 2 to 4 short sentences of plain text (no markdown, no bullet points, no headings) in English, even if the call was in another language. Cover: what the contact wanted or how they responded, the important details actually said (names, dates, times, products, quantities, prices, addresses), and how the call ended or the agreed next step. If the contact barely spoke or the call ended early, say that plainly. Use only what is in the transcript; never invent details.`

// ErrDisabled is returned when no Gemini API key is configured.
var ErrDisabled = errors.New("call summaries are not configured (GEMINI_API_KEY is missing)")

// ErrNoTranscript is returned when the call has nothing to summarise.
var ErrNoTranscript = errors.New("this call has no transcript to summarize")

// Summarizer writes and stores call summaries.
type Summarizer struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
	repo    *calls.Repository
	slots   chan struct{}
}

// New reads the configuration from the environment.
func New(repo *calls.Repository) *Summarizer {
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
	model := strings.TrimSpace(os.Getenv("GEMINI_SUMMARY_MODEL"))
	if model == "" {
		model = defaultModel
	}
	return &Summarizer{
		apiKey:  key,
		model:   model,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
		repo:    repo,
		slots:   make(chan struct{}, maxConcurrent),
	}
}

// Enabled reports whether a Gemini API key is configured.
func (s *Summarizer) Enabled() bool { return s != nil && s.apiKey != "" }

// AfterTranscript summarises a just-saved transcript in the background. It
// never blocks the caller and only logs failures. A transcript with fewer than
// two turns (a greeting nobody answered) is not worth a model call.
func (s *Summarizer) AfterTranscript(callID string, messages []models.CallMessage) {
	if !s.Enabled() || len(messages) < 2 {
		return
	}
	copied := append([]models.CallMessage(nil), messages...)
	go func() {
		s.slots <- struct{}{}
		defer func() { <-s.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), autoTimeout)
		defer cancel()
		if _, err := s.SummarizeAndStore(ctx, callID, copied); err != nil {
			log.Printf("callsummary: call %s: %v", callID, err)
			return
		}
		log.Printf("callsummary: wrote summary for call %s", callID)
	}()
}

// SummarizeAndStore writes the summary for callID and saves it.
func (s *Summarizer) SummarizeAndStore(ctx context.Context, callID string, messages []models.CallMessage) (string, error) {
	summary, err := s.Summarize(ctx, messages)
	if err != nil {
		return "", err
	}
	if err := s.repo.SetSummary(ctx, callID, summary); err != nil {
		return "", err
	}
	return summary, nil
}

// Summarize asks Gemini for the summary of one transcript.
func (s *Summarizer) Summarize(ctx context.Context, messages []models.CallMessage) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	transcript := formatTranscript(messages)
	if transcript == "" {
		return "", ErrNoTranscript
	}

	body, err := json.Marshal(map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": instructions}}},
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": "Transcript:\n\n" + transcript}},
		}},
		"generationConfig": map[string]any{
			"temperature":     0.2,
			"maxOutputTokens": 1024,
			"thinkingConfig":  map[string]string{"thinkingLevel": "low"},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/models/%s:generateContent", s.baseURL, s.model), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", s.apiKey)

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("gemini response (%d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := resp.Status
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", fmt.Errorf("gemini: %s", msg)
	}
	var b strings.Builder
	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			if !p.Thought {
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			break
		}
	}
	summary := strings.TrimSpace(b.String())
	if summary == "" {
		reason := ""
		if len(out.Candidates) > 0 {
			reason = out.Candidates[0].FinishReason
		}
		return "", fmt.Errorf("gemini returned no summary (finish reason %q)", reason)
	}
	return summary, nil
}

// formatTranscript renders the turns as "Agent: …" / "Contact: …" lines.
func formatTranscript(messages []models.CallMessage) string {
	lines := make([]string, 0, len(messages))
	for _, m := range messages {
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		who := "Contact"
		if m.Role == "assistant" || m.Role == "agent" {
			who = "Agent"
		}
		lines = append(lines, who+": "+text)
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxTranscriptChars {
		half := maxTranscriptChars / 2
		out = out[:half] + "\n[… middle of the call omitted …]\n" + out[len(out)-half:]
	}
	return out
}

// Controller serves the on-demand summary endpoint.
type Controller struct {
	s    *Summarizer
	repo *calls.Repository
}

// NewController creates the summary controller.
func NewController(s *Summarizer, repo *calls.Repository) *Controller {
	return &Controller{s: s, repo: repo}
}

// Generate handles POST /v1/dashboard/calls/{call_id}/summary: it (re)writes
// the call's summary from its saved transcript and returns it.
func (c *Controller) Generate(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	call, err := c.repo.GetByUser(r.Context(), user.ID, chi.URLParam(r, "call_id"))
	if errors.Is(err, calls.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "call not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to load call")
		return
	}
	if !c.s.Enabled() {
		httpx.WriteError(w, http.StatusServiceUnavailable, ErrDisabled.Error())
		return
	}
	messages, err := c.repo.ListMessages(r.Context(), call.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to load call transcript")
		return
	}
	if len(messages) == 0 {
		httpx.WriteError(w, http.StatusConflict, ErrNoTranscript.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	summary, err := c.s.SummarizeAndStore(ctx, call.ID, messages)
	if err != nil {
		log.Printf("callsummary: call %s on demand: %v", call.ID, err)
		httpx.WriteError(w, http.StatusBadGateway, "could not write the summary, try again")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Summary created successfully", Data: map[string]string{"summary": summary}})
}
