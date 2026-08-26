package voicecall

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

// TestBuildToolRequestGETUsesQueryString pins that a GET carries the model's
// arguments in the query string, where an API expects them, rather than in a
// body a GET is not supposed to have.
func TestBuildToolRequestGETUsesQueryString(t *testing.T) {
	cfg := &models.ToolAPIRequestConfig{
		Method: "GET",
		URL:    "https://api.example.com/v1/availability?tenant=acme",
		Parameters: []models.ToolParameter{
			{Name: "date", Type: "string"},
			{Name: "party", Type: "number"},
		},
		Headers: []models.ToolHeader{{Key: "Authorization", Value: "Bearer token"}},
	}
	args := map[string]any{"date": "2026-09-01", "party": float64(3)}

	request, err := buildToolRequest(context.Background(), cfg, args)
	if err != nil {
		t.Fatalf("buildToolRequest: %v", err)
	}
	if request.Body != nil {
		t.Errorf("GET request carries a body")
	}
	query := request.URL.Query()
	if got := query.Get("date"); got != "2026-09-01" {
		t.Errorf("date = %q, want 2026-09-01", got)
	}
	// A JSON number must not reach the endpoint as "3.000000".
	if got := query.Get("party"); got != "3" {
		t.Errorf("party = %q, want 3", got)
	}
	// The URL's own query survives the model's arguments being added to it.
	if got := query.Get("tenant"); got != "acme" {
		t.Errorf("tenant = %q, want acme", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer token" {
		t.Errorf("Authorization = %q, want Bearer token", got)
	}
}

// TestBuildToolRequestPOSTSendsJSONBody pins the other half: every non-GET verb
// sends the arguments as a JSON object.
func TestBuildToolRequestPOSTSendsJSONBody(t *testing.T) {
	cfg := &models.ToolAPIRequestConfig{
		Method:     "POST",
		URL:        "https://api.example.com/v1/bookings",
		Parameters: []models.ToolParameter{{Name: "name", Type: "string"}},
	}

	request, err := buildToolRequest(context.Background(), cfg, map[string]any{"name": "Ada"})
	if err != nil {
		t.Fatalf("buildToolRequest: %v", err)
	}
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	if decoded["name"] != "Ada" {
		t.Errorf("body = %s, want name=Ada", body)
	}
}

// TestBuildToolRequestDropsUndeclaredArguments guards the case where the model
// invents a field: only what the tool declares may reach somebody's API.
func TestBuildToolRequestDropsUndeclaredArguments(t *testing.T) {
	cfg := &models.ToolAPIRequestConfig{
		Method:     "GET",
		URL:        "https://api.example.com/v1/lookup",
		Parameters: []models.ToolParameter{{Name: "id", Type: "string"}},
	}

	request, err := buildToolRequest(context.Background(), cfg, map[string]any{
		"id":       "abc",
		"is_admin": true,
	})
	if err != nil {
		t.Fatalf("buildToolRequest: %v", err)
	}
	if request.URL.Query().Has("is_admin") {
		t.Errorf("undeclared argument was forwarded: %s", request.URL.RawQuery)
	}
}

func TestMissingRequiredArguments(t *testing.T) {
	parameters := []models.ToolParameter{
		{Name: "date", Required: true},
		{Name: "clinician", Required: false},
	}

	if missing := missingRequiredArguments(parameters, map[string]any{"date": "2026-09-01"}); len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	// An empty string is as unusable as an absent one — the request would go out
	// asking for nothing.
	missing := missingRequiredArguments(parameters, map[string]any{"date": "  "})
	if len(missing) != 1 || missing[0] != "date" {
		t.Errorf("missing = %v, want [date]", missing)
	}
}

// TestRunAPIRequestReturnsResponseToModel exercises the whole path against a
// real server: the tool is invoked the way a provider invokes it, with a JSON
// argument string, and the result is what the model reads.
func TestRunAPIRequestReturnsResponseToModel(t *testing.T) {
	// The guard refuses loopback by default, which is exactly what a test server
	// is; the opt-in is the same one a self-hosted install uses for internal APIs.
	t.Setenv(allowPrivateTargetsEnv, "true")

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
	t.Setenv(allowPrivateTargetsEnv, "true")

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
	t.Setenv(allowPrivateTargetsEnv, "true")

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

// TestToolParameterSchemaDeclaresRequiredArguments pins the schema handed to the
// provider, which is what the model fills in.
func TestToolParameterSchemaDeclaresRequiredArguments(t *testing.T) {
	schema := toolParameterSchema(apiRequestTool("book", &models.ToolAPIRequestConfig{
		Parameters: []models.ToolParameter{
			{Name: "date", Type: "string", Description: "The day.", Required: true},
			{Name: "note", Type: "string", Description: "Anything else.", Required: false},
		},
	}))

	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 2 {
		t.Fatalf("properties = %v, want two entries", schema["properties"])
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "date" {
		t.Errorf("required = %v, want [date]", schema["required"])
	}
	// A nil required list would serialize as null, which the providers reject.
	if schema["required"] == nil {
		t.Error("required must be an empty list rather than nil")
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

// TestIsBlockedToolIP pins which addresses a tool may not reach. A server that
// will fetch any URL on an account holder's behalf is a way to reach services
// that are only reachable because they sit next to it.
func TestIsBlockedToolIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"10.1.2.3",        // private
		"192.168.0.5",     // private
		"172.16.4.4",      // private
		"169.254.169.254", // cloud metadata
		"100.64.0.1",      // carrier-grade NAT
		"0.0.0.0",         // unspecified
		"::1",             // IPv6 loopback
		"fd00::1",         // IPv6 unique-local
	}
	for _, raw := range blocked {
		if !isBlockedToolIP(net.ParseIP(raw)) {
			t.Errorf("%s should be blocked", raw)
		}
	}

	allowed := []string{"8.8.8.8", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, raw := range allowed {
		if isBlockedToolIP(net.ParseIP(raw)) {
			t.Errorf("%s should be allowed", raw)
		}
	}
}

// TestGuardToolDialRefusesLoopback pins that the guard is enforced at dial time,
// where the address is the one actually being connected to.
func TestGuardToolDialRefusesLoopback(t *testing.T) {
	if err := guardToolDial("tcp", "127.0.0.1:8080", nil); err == nil {
		t.Error("dialing loopback was allowed")
	}
	if err := guardToolDial("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("dialing a public address failed: %v", err)
	}

	t.Setenv(allowPrivateTargetsEnv, "true")
	if err := guardToolDial("tcp", "127.0.0.1:8080", nil); err != nil {
		t.Errorf("opt-in did not allow loopback: %v", err)
	}
}

// TestParseToolArgumentsTolerateMalformedJSON pins that a truncated argument
// string does not take the turn down: the tool reports what is missing instead.
func TestParseToolArgumentsTolerateMalformedJSON(t *testing.T) {
	args := parseToolArguments("CALL1", "check_availability", `{"date":`)
	if args == nil {
		t.Fatal("parseToolArguments returned nil")
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}
}

func TestRedactedURLDropsQueryString(t *testing.T) {
	got := redactedURL("https://api.example.com/v1/lookup?phone=%2B15551234567")
	if strings.Contains(got, "5551234567") {
		t.Errorf("redactedURL = %q, want the query string dropped", got)
	}
	if got != "https://api.example.com/v1/lookup" {
		t.Errorf("redactedURL = %q, want the path kept", got)
	}
}
