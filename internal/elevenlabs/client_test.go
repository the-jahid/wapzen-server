package elevenlabs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newTestClient points a client at a stub server so the request it builds can
// be inspected without calling ElevenLabs.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := New("test-key")
	client.baseURL = server.URL
	return client
}

func TestSharedVoicesBuildsQuery(t *testing.T) {
	var got struct {
		path   string
		query  url.Values
		apiKey string
	}

	include := true
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.query = r.URL.Query()
		got.apiKey = r.Header.Get("xi-api-key")
		_, _ = w.Write([]byte(`{"voices":[{"voice_id":"v1","name":"Runa"}],"has_more":true,"total_count":42}`))
	})

	page, err := client.SharedVoices(context.Background(), SharedVoicesQuery{
		Search:             " calm ",
		Gender:             "female",
		UseCases:           []string{"conversational", "narrative_story"},
		Page:               2,
		PageSize:           500,
		Accent:             "",
		IncludeCustomRates: &include,
	})
	if err != nil {
		t.Fatalf("SharedVoices() error = %v", err)
	}

	if got.path != "/v1/shared-voices" {
		t.Errorf("path = %q, want /v1/shared-voices", got.path)
	}
	if got.apiKey != "test-key" {
		t.Errorf("xi-api-key = %q, want test-key", got.apiKey)
	}
	if search := got.query.Get("search"); search != "calm" {
		t.Errorf("search = %q, want trimmed %q", search, "calm")
	}
	if useCases := got.query["use_cases"]; len(useCases) != 2 {
		t.Errorf("use_cases = %v, want both values repeated", useCases)
	}
	// Oversized pages are clamped, absent filters are omitted entirely.
	if size := got.query.Get("page_size"); size != "100" {
		t.Errorf("page_size = %q, want clamped 100", size)
	}
	if got.query.Get("page") != "2" {
		t.Errorf("page = %q, want 2", got.query.Get("page"))
	}
	if _, ok := got.query["accent"]; ok {
		t.Error("accent should be omitted when blank")
	}
	if rates := got.query.Get("include_custom_rates"); rates != "true" {
		t.Errorf("include_custom_rates = %q, want true", rates)
	}

	if len(page.Voices) != 1 || page.Voices[0].Name != "Runa" {
		t.Errorf("voices = %+v, want the decoded stub voice", page.Voices)
	}
	if !page.HasMore || page.TotalCount != 42 {
		t.Errorf("has_more/total = %v/%d, want true/42", page.HasMore, page.TotalCount)
	}
}

func TestAddSharedVoiceReturnsWorkspaceID(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if want := "/v1/voices/add/owner-1/voice-1"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		_, _ = w.Write([]byte(`{"voice_id":"workspace-1"}`))
	})

	voiceID, err := client.AddSharedVoice(context.Background(), "owner-1", "voice-1", "Brian")
	if err != nil {
		t.Fatalf("AddSharedVoice() error = %v", err)
	}
	if voiceID != "workspace-1" {
		t.Errorf("voice id = %q, want workspace-1", voiceID)
	}
}

func TestAddSharedVoiceSurfacesUpstreamStatus(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":{"status":"voice_already_exists","message":"Voice already in your library"}}`))
	})

	_, err := client.AddSharedVoice(context.Background(), "owner-1", "voice-1", "Brian")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.Code != "voice_already_exists" {
		t.Errorf("code = %q, want voice_already_exists", apiErr.Code)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if apiErr.Message != "Voice already in your library" {
		t.Errorf("message = %q, want the upstream detail message", apiErr.Message)
	}
}

func TestCallsWithoutAPIKeyFail(t *testing.T) {
	client := New("  ")
	if client.Enabled() {
		t.Fatal("Enabled() = true for a blank key")
	}

	if _, err := client.Voices(context.Background(), VoicesQuery{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}

	if placeholder := New(placeholderAPIKey); placeholder.Enabled() {
		t.Error("Enabled() = true for the .env.example placeholder key")
	}
}
