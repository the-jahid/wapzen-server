package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatAgentRoutesRequireAuthentication(t *testing.T) {
	router := NewRouter(Dependencies{})
	for _, path := range []string{"/v1/chat-agents", "/v1/dashboard/chat-agents"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status=%d, want 401", path, resp.Code)
		}
	}
}
