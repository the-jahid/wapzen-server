package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPhoneNumberRoutesRequireUser(t *testing.T) {
	router := NewRouter(Dependencies{})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list", method: http.MethodGet, path: "/v1/phone-number"},
		{name: "login", method: http.MethodPost, path: "/v1/phone-number/login", body: `{}`},
		{name: "get", method: http.MethodGet, path: "/v1/phone-number/phone_123"},
		{name: "logout", method: http.MethodPost, path: "/v1/phone-number/logout", body: `{"phone_number_id":"phone_123"}`},
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
