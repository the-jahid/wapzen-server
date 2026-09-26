package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsRequest(method, origin string, allowedOrigins ...string) *httptest.ResponseRecorder {
	handler := CORS(allowedOrigins)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, "/v1/agents", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		req.Header.Set("Access-Control-Request-Headers", "authorization")
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func TestCORSAllowsWapzenOrigin(t *testing.T) {
	resp := corsRequest(http.MethodOptions, "https://wapzen.io")
	if resp.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", resp.Code, http.StatusNoContent)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "https://wapzen.io" {
		t.Fatalf("Allow-Origin = %q, want https://wapzen.io", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Headers"); got != "authorization" {
		t.Fatalf("Allow-Headers = %q, want authorization", got)
	}

	resp = corsRequest(http.MethodGet, "https://wapzen.io")
	if resp.Code != http.StatusOK || resp.Header().Get("Access-Control-Allow-Origin") != "https://wapzen.io" {
		t.Fatalf("GET status = %d, Allow-Origin = %q", resp.Code, resp.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSRejectsOtherOrigins(t *testing.T) {
	for _, origin := range []string{"https://evil.example", "http://wapzen.io", "https://www.wapzen.io", "http://localhost:3000"} {
		resp := corsRequest(http.MethodOptions, origin)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("%s preflight status = %d, want %d", origin, resp.Code, http.StatusForbidden)
		}
		if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s got Allow-Origin %q, want none", origin, got)
		}
	}
}

func TestCORSAllowsConfiguredOrigins(t *testing.T) {
	allowed := []string{"https://wapzen.io", "http://localhost:3000"}
	for _, origin := range allowed {
		resp := corsRequest(http.MethodOptions, origin, allowed...)
		if resp.Code != http.StatusNoContent {
			t.Fatalf("%s preflight status = %d, want %d", origin, resp.Code, http.StatusNoContent)
		}
		if got := resp.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("%s got Allow-Origin %q, want %q", origin, got, origin)
		}
	}

	resp := corsRequest(http.MethodOptions, "http://localhost:3001", allowed...)
	if resp.Code != http.StatusForbidden || resp.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin: status = %d, Allow-Origin = %q", resp.Code, resp.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSPassesRequestsWithoutOrigin(t *testing.T) {
	resp := corsRequest(http.MethodGet, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want none", got)
	}
}
