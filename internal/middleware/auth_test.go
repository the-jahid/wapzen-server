package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireAPIKeyUserRejectsMissingBearer(t *testing.T) {
	auth := NewAuth(nil, nil)
	called := false
	handler := auth.RequireAPIKeyUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/agents", nil))

	if called {
		t.Fatal("handler was called without an API key")
	}
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(resp.Body.String(), "valid API key") {
		t.Fatalf("body = %q, want API-key-specific error", resp.Body.String())
	}
}

func TestRequireAPIKeyUserRejectsNonAPIKeyBearer(t *testing.T) {
	auth := NewAuth(nil, nil)
	called := false
	handler := auth.RequireAPIKeyUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer clerk_session_token")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if called {
		t.Fatal("handler was called with a non-API-key bearer token")
	}
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(resp.Body.String(), "valid API key") {
		t.Fatalf("body = %q, want API-key-specific error", resp.Body.String())
	}
}
