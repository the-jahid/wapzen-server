package voicecall

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"whatsapp-ai-caller-server/internal/agenttools"
	"whatsapp-ai-caller-server/internal/models"
)

// stubControls records what a tool asked the call to do, without a call on the
// wire.
type stubControls struct {
	mu       sync.Mutex
	ended    int
	reason   string
	messages []string
	sendErr  error
}

func (s *stubControls) EndCall(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended++
	s.reason = reason
}

func (s *stubControls) SendText(_ context.Context, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.messages = append(s.messages, body)
	return nil
}

func apiRequestTool(name string, cfg *models.ToolAPIRequestConfig) models.AgentTool {
	return models.AgentTool{
		ID:          "tool_" + name,
		Type:        models.ToolTypeAPIRequest,
		Name:        name,
		Description: "Does the thing.",
		APIRequest:  cfg,
	}
}

// TestRunAPIRequestReturnsResponseToModel exercises the whole path against a
// real server: the tool is invoked the way a provider invokes it, with a JSON
// argument string, and the result is what the model reads.
func TestRunAPIRequestReturnsResponseToModel(t *testing.T) {
	// The guard refuses loopback by default, which is exactly what a test server
	// is; the opt-in is the same one a self-hosted install uses for internal APIs.
	t.Setenv(agenttools.AllowPrivateTargetsEnv, "true")

	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"slots":["09:00","11:30"]}`)
	}))
	defer server.Close()

	box := newCustomToolbox([]models.AgentTool{
		apiRequestTool("check_availability", &models.ToolAPIRequestConfig{
			Method:         "GET",
			URL:            server.URL,
			TimeoutSeconds: 5,
			Parameters:     []models.ToolParameter{{Name: "date", Type: "string", Required: true}},
		}),
	}, nil, &stubControls{}, "CALL1")
	if box == nil {
		t.Fatal("newCustomToolbox returned nil for one tool")
	}

	result := box.Run(context.Background(), "check_availability", `{"date":"2026-09-01"}`)
	if !strings.Contains(result, "09:00") {
		t.Errorf("result = %q, want it to carry the response body", result)
	}
	if gotQuery != "date=2026-09-01" {
		t.Errorf("query = %q, want date=2026-09-01", gotQuery)
	}
}

// TestRunAPIRequestMissingArgumentAsksForIt pins that a required argument the
// model left out produces a retryable instruction rather than a request going
// out half-filled.
func TestRunAPIRequestMissingArgumentAsksForIt(t *testing.T) {
	t.Setenv(agenttools.AllowPrivateTargetsEnv, "true")

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	box := newCustomToolbox([]models.AgentTool{
		apiRequestTool("check_availability", &models.ToolAPIRequestConfig{
			Method:         "GET",
			URL:            server.URL,
			TimeoutSeconds: 5,
			Parameters:     []models.ToolParameter{{Name: "date", Required: true}},
		}),
	}, nil, &stubControls{}, "CALL1")

	result := box.Run(context.Background(), "check_availability", `{}`)
	if !strings.Contains(result, "date") {
		t.Errorf("result = %q, want it to name the missing argument", result)
	}
	if called {
		t.Error("the request was sent despite a missing required argument")
	}
}

// TestRunAPIRequestErrorStatusHidesBody pins that a failing endpoint's body is
// not handed to the model: an HTML error page read out to a caller is the exact
// failure the prompt rules warn against.
func TestRunAPIRequestErrorStatusHidesBody(t *testing.T) {
	t.Setenv(agenttools.AllowPrivateTargetsEnv, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "<html>stack trace goes here</html>")
	}))
	defer server.Close()

	box := newCustomToolbox([]models.AgentTool{
		apiRequestTool("lookup", &models.ToolAPIRequestConfig{
			Method: "GET", URL: server.URL, TimeoutSeconds: 5,
		}),
	}, nil, &stubControls{}, "CALL1")

	result := box.Run(context.Background(), "lookup", `{}`)
	if strings.Contains(result, "stack trace") {
		t.Errorf("result = %q, want the error body withheld", result)
	}
	if !strings.Contains(result, "500") {
		t.Errorf("result = %q, want the status reported", result)
	}
}

// TestEndCallArmsReleaseOnce pins that the hangup is armed rather than
// performed, and that a model calling it twice in one turn arms it once.
func TestEndCallArmsReleaseOnce(t *testing.T) {
	controls := &stubControls{}
	box := newCustomToolbox([]models.AgentTool{{
		ID: "tool_end", Type: models.ToolTypeEndCall, Name: "end_call",
		Description: "Hang up when the caller is done.",
		EndCall:     &models.ToolEndCallConfig{Message: "Thanks for calling, goodbye."},
	}}, nil, controls, "CALL1")

	result := box.Run(context.Background(), "end_call", `{"reason":"caller is done"}`)
	if !strings.Contains(result, "Thanks for calling") {
		t.Errorf("result = %q, want the configured closing line", result)
	}
	box.Run(context.Background(), "end_call", `{}`)

	if controls.ended != 1 {
		t.Errorf("EndCall called %d times, want 1", controls.ended)
	}
	if !strings.Contains(controls.reason, "caller is done") {
		t.Errorf("reason = %q, want the model's reason recorded", controls.reason)
	}
}

// TestTransferCallRecordsDestination pins that a transfer records where the
// caller was sent, which is the only auditable trace of a handover that WhatsApp
// cannot actually bridge.
func TestTransferCallRecordsDestination(t *testing.T) {
	controls := &stubControls{}
	box := newCustomToolbox([]models.AgentTool{{
		ID: "tool_transfer", Type: models.ToolTypeTransferCall, Name: "transfer_to_human",
		Description:  "Hand the caller to the support desk.",
		TransferCall: &models.ToolTransferCallConfig{Destination: "+8801639726992", Message: "One moment."},
	}}, nil, controls, "CALL1")

	result := box.Run(context.Background(), "transfer_to_human", `{}`)
	if !strings.Contains(result, "One moment.") {
		t.Errorf("result = %q, want the configured announcement", result)
	}
	if !strings.Contains(controls.reason, "+8801639726992") {
		t.Errorf("reason = %q, want the destination recorded", controls.reason)
	}
}

// TestSendTextPrefersConfiguredBody pins which message is sent when the tool
// carries one, and that a tool without one takes the model's.
func TestSendTextPrefersConfiguredBody(t *testing.T) {
	controls := &stubControls{}
	box := newCustomToolbox([]models.AgentTool{
		{
			ID: "tool_fixed", Type: models.ToolTypeSendText, Name: "send_link",
			Description: "Send the booking link.",
			SendText:    &models.ToolSendTextConfig{Body: "https://example.com/book"},
		},
		{
			ID: "tool_free", Type: models.ToolTypeSendText, Name: "send_note",
			Description: "Send the caller a note.",
		},
	}, nil, controls, "CALL1")

	box.Run(context.Background(), "send_link", `{"message":"ignored"}`)
	box.Run(context.Background(), "send_note", `{"message":"see you Tuesday"}`)

	want := []string{"https://example.com/book", "see you Tuesday"}
	if len(controls.messages) != len(want) {
		t.Fatalf("messages = %v, want %v", controls.messages, want)
	}
	for i, message := range want {
		if controls.messages[i] != message {
			t.Errorf("message %d = %q, want %q", i, controls.messages[i], message)
		}
	}
}

// TestNewCustomToolboxSkipsReservedNames pins that an agent tool cannot shadow
// the built-in knowledge lookup, which would leave the model unable to reach it.
func TestNewCustomToolboxSkipsReservedNames(t *testing.T) {
	box := newCustomToolbox([]models.AgentTool{
		{ID: "t1", Type: models.ToolTypeEndCall, Name: knowledgeToolName, Description: "Shadow."},
		{ID: "t2", Type: models.ToolTypeEndCall, Name: "end_call", Description: "Hang up."},
	}, []string{knowledgeToolName}, &stubControls{}, "CALL1")

	if box == nil {
		t.Fatal("newCustomToolbox returned nil")
	}
	if len(box.order) != 1 || box.order[0] != "end_call" {
		t.Errorf("tools = %v, want only end_call", box.order)
	}
}

// TestCombinedToolboxRoutesByName pins that the two halves are offered together
// and that each name reaches its own runner.
func TestCombinedToolboxRoutesByName(t *testing.T) {
	custom := newCustomToolbox([]models.AgentTool{{
		ID: "t1", Type: models.ToolTypeEndCall, Name: "end_call", Description: "Hang up.",
	}}, []string{knowledgeToolName}, &stubControls{}, "CALL1")

	combined := newCombinedToolbox(nil, custom)
	if combined == nil {
		t.Fatal("newCombinedToolbox returned nil with a custom toolbox")
	}
	definitions := combined.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "end_call" {
		t.Fatalf("definitions = %v, want one end_call", definitions)
	}
	if result := combined.Run(context.Background(), "nope", `{}`); !strings.Contains(result, "Unknown tool") {
		t.Errorf("result = %q, want an unknown-tool report", result)
	}
	if newCombinedToolbox(nil, nil) != nil {
		t.Error("newCombinedToolbox must be nil when the agent has neither half")
	}
}
