package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeLoginBody(t *testing.T, body string) (loginPhoneNumberRequest, *httptest.ResponseRecorder, bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/phone-number/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	decoded, ok := decodeLoginPhoneNumberRequest(resp, req)
	return decoded, resp, ok
}

// TestDecodeLoginPhoneNumberRequestReadsAgentID pins the field the agent editor
// relies on: without it the scanned number reaches no agent and the user is back
// to assigning it by hand.
func TestDecodeLoginPhoneNumberRequestReadsAgentID(t *testing.T) {
	req, _, ok := decodeLoginBody(t, `{"agent_id":"  agent_123  ","label":"Support"}`)
	if !ok {
		t.Fatal("decode rejected a valid body")
	}
	if req.AgentID == nil {
		t.Fatal("agent_id = nil, want agent_123")
	}
	if *req.AgentID != "agent_123" {
		t.Fatalf("agent_id = %q, want %q", *req.AgentID, "agent_123")
	}
}

// A blank agent_id means "assign nothing" rather than "assign the agent named
// by an empty id", which the manager would otherwise have to sort out.
func TestDecodeLoginPhoneNumberRequestDropsBlankAgentID(t *testing.T) {
	for _, body := range []string{`{"agent_id":""}`, `{"agent_id":"   "}`, `{}`} {
		req, _, ok := decodeLoginBody(t, body)
		if !ok {
			t.Fatalf("decode rejected %s", body)
		}
		if req.AgentID != nil {
			t.Fatalf("%s: agent_id = %q, want nil", body, *req.AgentID)
		}
	}
}

// pairing_method is how the dashboard asks for a code to type instead of a QR
// to scan; anything but the two known methods is a client bug worth a 400.
func TestDecodeLoginPhoneNumberRequestChecksPairingMethod(t *testing.T) {
	req, _, ok := decodeLoginBody(t, `{"pairing_method":" phone_code ","phone_number":"+8801639726992"}`)
	if !ok {
		t.Fatal("decode rejected a phone_code body")
	}
	if req.PairingMethod != "phone_code" {
		t.Fatalf("pairing_method = %q, want phone_code", req.PairingMethod)
	}

	if _, resp, ok := decodeLoginBody(t, `{"pairing_method":"sms"}`); ok || resp.Code != http.StatusBadRequest {
		t.Fatalf("pairing_method sms: ok=%t status=%d, want rejected with 400", ok, resp.Code)
	}
}
