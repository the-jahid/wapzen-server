package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireUserRejectsMissingBearer(t *testing.T) {
	auth := NewAuth(nil)
	called := false
	handler := auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/agents", nil))

	if called {
		t.Fatal("handler was called without a bearer token")
	}
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
}
