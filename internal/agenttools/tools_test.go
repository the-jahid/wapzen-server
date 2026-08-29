package agenttools

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

func apiRequestTool(name string, cfg *models.ToolAPIRequestConfig) models.AgentTool {
	return models.AgentTool{
		ID:          "tool_" + name,
		Type:        models.ToolTypeAPIRequest,
		Name:        name,
		Description: "Does the thing.",
		APIRequest:  cfg,
	}
}

// TestBuildRequestGETUsesQueryString pins that a GET carries the model's
// arguments in the query string, where an API expects them, rather than in a
// body a GET is not supposed to have.
func TestBuildRequestGETUsesQueryString(t *testing.T) {
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

	request, err := BuildRequest(context.Background(), cfg, args)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
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

// TestBuildRequestPOSTSendsJSONBody pins the other half: every non-GET verb
// sends the arguments as a JSON object.
func TestBuildRequestPOSTSendsJSONBody(t *testing.T) {
	cfg := &models.ToolAPIRequestConfig{
		Method:     "POST",
		URL:        "https://api.example.com/v1/bookings",
		Parameters: []models.ToolParameter{{Name: "name", Type: "string"}},
	}

	request, err := BuildRequest(context.Background(), cfg, map[string]any{"name": "Ada"})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
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

// TestBuildRequestDropsUndeclaredArguments guards the case where the model
// invents a field: only what the tool declares may reach somebody's API.
func TestBuildRequestDropsUndeclaredArguments(t *testing.T) {
	cfg := &models.ToolAPIRequestConfig{
		Method:     "GET",
		URL:        "https://api.example.com/v1/lookup",
		Parameters: []models.ToolParameter{{Name: "id", Type: "string"}},
	}

	request, err := BuildRequest(context.Background(), cfg, map[string]any{
		"id":       "abc",
		"is_admin": true,
	})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if request.URL.Query().Has("is_admin") {
		t.Errorf("undeclared argument was forwarded: %s", request.URL.RawQuery)
	}
}

func TestMissingRequired(t *testing.T) {
	parameters := []models.ToolParameter{
		{Name: "date", Required: true},
		{Name: "clinician", Required: false},
	}

	if missing := MissingRequired(parameters, map[string]any{"date": "2026-09-01"}); len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	// An empty string is as unusable as an absent one — the request would go out
	// asking for nothing.
	missing := MissingRequired(parameters, map[string]any{"date": "  "})
	if len(missing) != 1 || missing[0] != "date" {
		t.Errorf("missing = %v, want [date]", missing)
	}
}

// TestParameterSchemaDeclaresRequiredArguments pins the schema handed to the
// provider, which is what the model fills in.
func TestParameterSchemaDeclaresRequiredArguments(t *testing.T) {
	schema := ParameterSchema(apiRequestTool("book", &models.ToolAPIRequestConfig{
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

// TestIsBlockedIP pins which addresses a tool may not reach. A server that will
// fetch any URL on an account holder's behalf is a way to reach services that
// are only reachable because they sit next to it.
func TestIsBlockedIP(t *testing.T) {
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
		if !isBlockedIP(net.ParseIP(raw)) {
			t.Errorf("%s should be blocked", raw)
		}
	}

	allowed := []string{"8.8.8.8", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, raw := range allowed {
		if isBlockedIP(net.ParseIP(raw)) {
			t.Errorf("%s should be allowed", raw)
		}
	}
}

// TestGuardDialRefusesLoopback pins that the guard is enforced at dial time,
// where the address is the one actually being connected to.
func TestGuardDialRefusesLoopback(t *testing.T) {
	if err := guardDial("tcp", "127.0.0.1:8080", nil); err == nil {
		t.Error("dialing loopback was allowed")
	}
	if err := guardDial("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("dialing a public address failed: %v", err)
	}

	t.Setenv(AllowPrivateTargetsEnv, "true")
	if err := guardDial("tcp", "127.0.0.1:8080", nil); err != nil {
		t.Errorf("opt-in did not allow loopback: %v", err)
	}
}

// TestParseArgumentsToleratesMalformedJSON pins that a truncated argument
// string does not take the turn down: the tool reports what is missing instead.
func TestParseArgumentsToleratesMalformedJSON(t *testing.T) {
	args := ParseArguments("voicecall: call CALL1", "check_availability", `{"date":`)
	if args == nil {
		t.Fatal("ParseArguments returned nil")
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
