package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The voice catalogue is proxied with the server's ElevenLabs key, so every
// route must sit behind authentication.
func TestVoiceRoutesRequireUser(t *testing.T) {
	router := NewRouter(Dependencies{})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "workspace voices", method: http.MethodGet, path: "/v1/voices/elevenlabs"},
		{name: "library voices", method: http.MethodGet, path: "/v1/voices/elevenlabs/library?search=calm"},
		{
			name:   "add library voice",
			method: http.MethodPost,
			path:   "/v1/voices/elevenlabs/library/owner_123/voice_123",
			body:   `{"name":"Brian"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp := httptest.NewRecorder()

			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusUnauthorized, resp.Body.String())
			}
			if !strings.Contains(resp.Body.String(), "Authentication required") {
				t.Fatalf("body = %q, want authentication error", resp.Body.String())
			}
		})
	}
}
