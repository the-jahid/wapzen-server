package agenttools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// TestTestAPIRequestReportsTheExchange pins that a test run returns what went
// out, what came back, and the same text a live agent would have read.
func TestTestAPIRequestReportsTheExchange(t *testing.T) {
	t.Setenv(AllowPrivateTargetsEnv, "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("checkin"); got != "20261029" {
			t.Errorf("checkin = %q, want 20261029", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"disponibile": true}`))
	}))
	defer server.Close()

	tool := apiRequestTool("check_rooms", &models.ToolAPIRequestConfig{
		Method:     "GET",
		URL:        server.URL,
		Headers:    []models.ToolHeader{{Key: "Authorization", Value: "Bearer sk-live-abcdef123456"}},
		Parameters: []models.ToolParameter{{Name: "checkin", Type: "string", Required: true}},
	})
	args := map[string]any{"checkin": "20261029"}

	result := NewRunner("test").TestAPIRequest(context.Background(), tool, args)
	if !result.OK || result.StatusCode != 200 || result.Error != "" {
		t.Fatalf("result = %+v, want a 200 with no error", result)
	}
	if result.Body != `{"disponibile": true}` {
		t.Errorf("body = %q", result.Body)
	}
	if !strings.Contains(result.Request.URL, "checkin=20261029") {
		t.Errorf("request url = %q, want the argument in the query", result.Request.URL)
	}
	// The credential is masked: a test result ends up in screenshots.
	if got := result.Request.Headers["Authorization"]; strings.Contains(got, "abcdef") {
		t.Errorf("Authorization header echoed unmasked: %q", got)
	}
	live := NewRunner("test").RunAPIRequest(context.Background(), tool, args)
	if result.ModelResult != live {
		t.Errorf("model result = %q, want what a live agent reads: %q", result.ModelResult, live)
	}
}

// TestTestAPIRequestShowsErrorBodies pins the difference from a live run: the
// agent is only told the status of a failure, but whoever is testing the tool
// needs the body that explains it.
func TestTestAPIRequestShowsErrorBodies(t *testing.T) {
	t.Setenv(AllowPrivateTargetsEnv, "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"checkout must be after checkin"}`))
	}))
	defer server.Close()

	tool := apiRequestTool("check_rooms", &models.ToolAPIRequestConfig{Method: "POST", URL: server.URL})
	result := NewRunner("test").TestAPIRequest(context.Background(), tool, map[string]any{})
	if result.OK || result.StatusCode != 400 {
		t.Fatalf("result = %+v, want a failed 400", result)
	}
	if !strings.Contains(result.Body, "checkout must be after checkin") {
		t.Errorf("body = %q, want the error body", result.Body)
	}
	if !strings.Contains(result.ModelResult, "status 400") {
		t.Errorf("model result = %q", result.ModelResult)
	}
}

// TestTestAPIRequestMissingArgument pins that a missing required argument is
// reported without anything being sent.
func TestTestAPIRequestMissingArgument(t *testing.T) {
	tool := apiRequestTool("check_rooms", &models.ToolAPIRequestConfig{
		Method:     "GET",
		URL:        "https://api.example.com/rooms",
		Parameters: []models.ToolParameter{{Name: "checkin", Type: "string", Required: true}},
	})
	result := NewRunner("test").TestAPIRequest(context.Background(), tool, map[string]any{})
	if result.Request != nil || !strings.Contains(result.Error, "checkin") {
		t.Fatalf("result = %+v, want a missing-argument error and no request", result)
	}
}

// TestTestAPIRequestKeepsTheAddressGuard pins that a test cannot be used to
// reach addresses a live tool could not.
func TestTestAPIRequestKeepsTheAddressGuard(t *testing.T) {
	t.Setenv(AllowPrivateTargetsEnv, "")
	tool := apiRequestTool("internal", &models.ToolAPIRequestConfig{Method: "GET", URL: "http://127.0.0.1:1/"})
	result := NewRunner("test").TestAPIRequest(context.Background(), tool, map[string]any{})
	if result.OK || !strings.Contains(result.Error, "refusing to reach internal address") {
		t.Fatalf("result = %+v, want the request refused", result)
	}
}

func TestLocation(t *testing.T) {
	cases := []struct{ timezone, phone, want string }{
		{"Europe/Berlin", "+393314682549", "Europe/Berlin"},
		{"", "393314682549", "Europe/Rome"},
		{"", "+8801639726992", "Asia/Dhaka"},
		{"", "971505581524", "Asia/Dubai"},
		{"Not/AZone", "393314682549", "Europe/Rome"},
		{"", "15551234567", "UTC"},
		{"", "", "UTC"},
	}
	for _, c := range cases {
		if got := Location(c.timezone, c.phone).String(); got != c.want {
			t.Errorf("Location(%q, %q) = %s, want %s", c.timezone, c.phone, got, c.want)
		}
	}
}

// TestDateContextUsesLocalDate pins that the date is the one where the
// business is: 23:30 UTC is already tomorrow in Rome in summer.
func TestDateContextUsesLocalDate(t *testing.T) {
	now := time.Date(2026, 7, 14, 23, 30, 0, 0, time.UTC)
	line := DateContext(now, Location("", "393314682549"))
	if !strings.Contains(line, "Wednesday 15 July 2026, 01:30") || !strings.Contains(line, "Europe/Rome") {
		t.Errorf("DateContext = %q", line)
	}
}
