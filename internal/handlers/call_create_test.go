package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

func TestIsDialableTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "e164 with plus", target: "+15557654321", want: true},
		{name: "bare digits", target: "15557654321", want: true},
		{name: "whatsapp jid passes through", target: "15557654321@s.whatsapp.net", want: true},
		{name: "lid jid passes through", target: "123456789@lid", want: true},
		{name: "spaces and dashes rejected", target: "+1 555-765-4321", want: false},
		{name: "letters rejected", target: "+1555CALLME", want: false},
		{name: "too short", target: "+123", want: false},
		{name: "too long for e164", target: "+1234567890123456", want: false},
		{name: "empty", target: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDialableTarget(tt.target); got != tt.want {
				t.Errorf("isDialableTarget(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestDecodeCreateCallRequest(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		// wantFields are the field names expected in the validation error body.
		wantFields []string
	}{
		{
			name:   "minimal valid body",
			body:   `{"phone_number_id":"pn_1","to":"+15557654321"}`,
			wantOK: true,
		},
		{
			name:   "agent_id accepted",
			body:   `{"phone_number_id":"pn_1","to":"+15557654321","agent_id":"agent_1"}`,
			wantOK: true,
		},
		{
			name:       "empty body",
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"phone_number_id":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing both required fields",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"phone_number_id", "to"},
		},
		{
			name:       "whitespace-only fields count as missing",
			body:       `{"phone_number_id":"  ","to":"  "}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"phone_number_id", "to"},
		},
		{
			name:       "malformed destination",
			body:       `{"phone_number_id":"pn_1","to":"not a number"}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"to"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/calls", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			req, ok := decodeCreateCallRequest(w, r)
			if ok != tt.wantOK {
				t.Fatalf("decodeCreateCallRequest ok = %v, want %v (body: %s)", ok, tt.wantOK, w.Body.String())
			}
			if ok {
				if req.PhoneNumberID == "" || req.To == "" {
					t.Errorf("accepted request has empty fields: %+v", req)
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			for _, field := range tt.wantFields {
				if !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
					t.Errorf("response missing field error for %q: %s", field, w.Body.String())
				}
			}
		})
	}
}

func TestDecodeCreateCallRequestTrimsFields(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/calls",
		strings.NewReader(`{"phone_number_id":" pn_1 ","to":" +15557654321 ","agent_id":" agent_1 "}`))
	w := httptest.NewRecorder()

	req, ok := decodeCreateCallRequest(w, r)
	if !ok {
		t.Fatalf("expected the padded body to be accepted, got %d: %s", w.Code, w.Body.String())
	}
	if req.PhoneNumberID != "pn_1" || req.To != "+15557654321" || req.AgentID != "agent_1" {
		t.Errorf("fields not trimmed: %+v", req)
	}
}

func TestPlaceCallErrorResponse(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "agent mismatch is the caller's mistake",
			err:        fmt.Errorf("%w: this number is assigned to agent a_1", whatsapplogin.ErrAgentMismatch),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "disconnected number is a conflict",
			err:        whatsapplogin.ErrNumberNotConnected,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "missing call handler is a conflict",
			err:        whatsapplogin.ErrCallHandlerUnavailable,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "no live agent is a conflict",
			err:        fmt.Errorf("%w: no active outbound-capable agent", whatsapplogin.ErrNoLiveAgent),
			wantStatus: http.StatusConflict,
		},
		{
			name:       "unconfigured server is unavailable",
			err:        whatsapplogin.ErrCallingUnavailable,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "unconfigured agent provider is unavailable",
			err:        fmt.Errorf("%w: agent a_1 selected voice provider %q", whatsapplogin.ErrAgentUnavailable, "11labs"),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "an unreachable peer is an upstream failure",
			err:        errors.New("place call: peer 15557654321@lid has no devices"),
			wantStatus: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, message := placeCallErrorResponse(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if strings.TrimSpace(message) == "" {
				t.Error("message is empty; the caller needs to know what went wrong")
			}
		})
	}
}
