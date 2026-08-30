package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The saved chat threads are one user's private messages, so every way into the
// collection — the flat listing, one agent's listing, and a single thread — has
// to refuse an unauthenticated request rather than fall through to the handler.
func TestChatConversationRoutesRequireAuthentication(t *testing.T) {
	router := NewRouter(Dependencies{})
	paths := []string{
		"/v1/chat-conversations",
		"/v1/dashboard/chat-conversations",
		"/v1/chat-conversations/conversation_1",
		"/v1/dashboard/chat-conversations/conversation_1",
		"/v1/chat-agents/chat_agent_1/conversations",
		"/v1/dashboard/chat-agents/chat_agent_1/conversations",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status=%d, want 401", path, resp.Code)
		}
	}

	// Sending is the one route here that leaves the server, so it has to be
	// refused before the handler can reach a WhatsApp session.
	for _, path := range []string{
		"/v1/chat-conversations/conversation_1/messages",
		"/v1/dashboard/chat-conversations/conversation_1/messages",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"content":"hello"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("POST %s status=%d, want 401", path, resp.Code)
		}
	}
}
