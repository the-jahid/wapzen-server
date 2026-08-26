package handlers

import (
	"net/url"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func fieldNames(errs []types.FieldError) []string {
	names := make([]string, 0, len(errs))
	for _, err := range errs {
		names = append(names, err.Field)
	}
	return names
}

func hasField(errs []types.FieldError, field string) bool {
	for _, err := range errs {
		if err.Field == field {
			return true
		}
	}
	return false
}

// TestValidateToolNameAcceptsFunctionNames pins the name rule to what the
// function-calling APIs accept: a name they reject makes every call carrying it
// fail, which is far harder to diagnose than a 400 at create time.
func TestValidateToolNameAcceptsFunctionNames(t *testing.T) {
	valid := []string{"check_availability", "a", "book_slot_2"}
	for _, name := range valid {
		if errs := validateToolName(name); len(errs) != 0 {
			t.Errorf("%q rejected: %v", name, errs)
		}
	}

	invalid := []string{"", "Check_Availability", "check-availability", "2fast", "check availability", strings.Repeat("a", 65)}
	for _, name := range invalid {
		if errs := validateToolName(name); len(errs) == 0 {
			t.Errorf("%q accepted, want rejected", name)
		}
	}
}

func TestValidateToolDescriptionRequiresText(t *testing.T) {
	if errs := validateToolDescription(""); len(errs) != 1 {
		t.Errorf("empty description accepted: %v", errs)
	}
	if errs := validateToolDescription(strings.Repeat("x", models.ToolMaxDescriptionLength+1)); len(errs) != 1 {
		t.Errorf("over-long description accepted: %v", errs)
	}
	if errs := validateToolDescription("Looks up open slots."); len(errs) != 0 {
		t.Errorf("valid description rejected: %v", errs)
	}
}

func TestValidateToolURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"https", "https://api.example.com/v1/availability", false},
		{"http", "http://api.example.com/v1/availability", false},
		{"empty", "", true},
		{"relative", "/v1/availability", true},
		{"no host", "https://", true},
		{"file scheme", "file:///etc/passwd", true},
		{"ftp scheme", "ftp://example.com/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateToolURL(tt.raw)
			if got := len(errs) > 0; got != tt.wantErr {
				t.Errorf("validateToolURL(%q) errors = %v, wantErr %v", tt.raw, errs, tt.wantErr)
			}
		})
	}
}

// TestValidateAPIRequestConfigReportsEveryProblem pins that a form full of
// mistakes comes back naming each one, rather than the caller having to fix
// them one round trip at a time.
func TestValidateAPIRequestConfigReportsEveryProblem(t *testing.T) {
	errs := validateAPIRequestConfig(models.ToolAPIRequestConfig{
		Method:         "TRACE",
		URL:            "not a url",
		TimeoutSeconds: 900,
		Headers:        []models.ToolHeader{{Key: "  ", Value: "x"}},
		Parameters: []models.ToolParameter{
			{Name: "date", Type: "date", Description: ""},
			{Name: "date", Type: "string", Description: "Duplicate."},
		},
	})

	want := []string{
		"api_request.method",
		"api_request.url",
		"api_request.timeout_seconds",
		"api_request.headers[0].key",
		"api_request.parameters[0].type",
		"api_request.parameters[0].description",
		"api_request.parameters[1].name",
	}
	for _, field := range want {
		if !hasField(errs, field) {
			t.Errorf("missing error for %s; got %v", field, fieldNames(errs))
		}
	}
}

func TestValidateAPIRequestConfigAcceptsMinimalConfig(t *testing.T) {
	// Method and timeout are optional: both fall back to documented defaults.
	errs := validateAPIRequestConfig(models.ToolAPIRequestConfig{
		URL: "https://api.example.com/v1/availability",
	})
	if len(errs) != 0 {
		t.Errorf("minimal config rejected: %v", fieldNames(errs))
	}
}

func TestValidateTransferCallConfig(t *testing.T) {
	if errs := validateTransferCallConfig(models.ToolTransferCallConfig{Destination: "+8801639726992"}); len(errs) != 0 {
		t.Errorf("valid destination rejected: %v", errs)
	}
	for _, destination := range []string{"", "the support desk", "01639726992", "+123"} {
		if errs := validateTransferCallConfig(models.ToolTransferCallConfig{Destination: destination}); len(errs) == 0 {
			t.Errorf("destination %q accepted, want rejected", destination)
		}
	}
}

// TestValidateToolConfigRequiresBlockOnCreate pins which types cannot be
// created without their configuration: a tool with no URL or no destination has
// nothing to do when the model calls it.
func TestValidateToolConfigRequiresBlockOnCreate(t *testing.T) {
	if errs := validateToolConfig(models.ToolTypeAPIRequest, toolRequest{}, true); !hasField(errs, "api_request") {
		t.Errorf("api_request without its block was accepted: %v", fieldNames(errs))
	}
	if errs := validateToolConfig(models.ToolTypeTransferCall, toolRequest{}, true); !hasField(errs, "transfer_call") {
		t.Errorf("transfer_call without its block was accepted: %v", fieldNames(errs))
	}
	// end_call and send_text act without configuration — the model supplies what
	// they need at call time.
	if errs := validateToolConfig(models.ToolTypeEndCall, toolRequest{}, true); len(errs) != 0 {
		t.Errorf("end_call without a block was rejected: %v", fieldNames(errs))
	}
	if errs := validateToolConfig(models.ToolTypeSendText, toolRequest{}, true); len(errs) != 0 {
		t.Errorf("send_text without a block was rejected: %v", fieldNames(errs))
	}
	// On update the block may be omitted for every type: the stored one stands.
	if errs := validateToolConfig(models.ToolTypeAPIRequest, toolRequest{}, false); len(errs) != 0 {
		t.Errorf("update without a block was rejected: %v", fieldNames(errs))
	}
}

// TestValidateCreateToolRejectsForeignConfig is the regression for a create that
// accepted a configuration block its type never reads and silently dropped it,
// leaving a tool that looked configured and did nothing. Update refused the same
// body, so the two paths disagreed.
func TestValidateCreateToolRejectsForeignConfig(t *testing.T) {
	errs := validateCreateTool("end_call", "wrong_block", "Carries a block it never reads.", toolRequest{
		APIRequest: &models.ToolAPIRequestConfig{URL: "https://api.example.com"},
	})
	if !hasField(errs, "api_request") {
		t.Errorf("an api_request block on an end_call tool was accepted: %v", fieldNames(errs))
	}
}

// TestValidateCreateToolAcceptsValidRequest guards the other direction: the
// refusal above must not reject a tool carrying its own block.
func TestValidateCreateToolAcceptsValidRequest(t *testing.T) {
	errs := validateCreateTool("api_request", "check_availability", "Look up open slots.", toolRequest{
		APIRequest: &models.ToolAPIRequestConfig{
			Method: "GET",
			URL:    "https://api.example.com/v1/availability",
			Parameters: []models.ToolParameter{
				{Name: "date", Type: "string", Description: "The day.", Required: true},
			},
		},
	})
	if len(errs) != 0 {
		t.Errorf("a valid create was rejected: %v", fieldNames(errs))
	}
}

// TestValidateCreateToolReportsMissingBasics pins that the required fields are
// reported together rather than one round trip at a time.
func TestValidateCreateToolReportsMissingBasics(t *testing.T) {
	errs := validateCreateTool("", "", "", toolRequest{})
	for _, field := range []string{"type", "name", "description"} {
		if !hasField(errs, field) {
			t.Errorf("missing error for %s; got %v", field, fieldNames(errs))
		}
	}
}

// TestRejectForeignToolConfig pins that a block the tool's type never reads is
// refused rather than dropped — a caller who sent it believes it took effect.
func TestRejectForeignToolConfig(t *testing.T) {
	errs := rejectForeignToolConfig(models.ToolTypeEndCall, toolRequest{
		APIRequest:   &models.ToolAPIRequestConfig{URL: "https://example.com"},
		TransferCall: &models.ToolTransferCallConfig{Destination: "+8801639726992"},
	})
	if !hasField(errs, "api_request") || !hasField(errs, "transfer_call") {
		t.Errorf("foreign blocks accepted: %v", fieldNames(errs))
	}

	if errs := rejectForeignToolConfig(models.ToolTypeAPIRequest, toolRequest{
		APIRequest: &models.ToolAPIRequestConfig{URL: "https://example.com"},
	}); len(errs) != 0 {
		t.Errorf("the tool's own block was refused: %v", fieldNames(errs))
	}
}

// TestNormalizedAPIRequestAppliesDefaults pins that what is stored is complete,
// so the runtime that reads it back later does not have to re-apply defaults the
// caller never saw.
func TestNormalizedAPIRequestAppliesDefaults(t *testing.T) {
	got := normalizedAPIRequest(&models.ToolAPIRequestConfig{
		URL:        "  https://api.example.com/v1/availability  ",
		Headers:    []models.ToolHeader{{Key: " Authorization ", Value: " Bearer x "}, {Key: "   ", Value: "dropped"}},
		Parameters: []models.ToolParameter{{Name: " date ", Description: " The day. "}},
	})

	if got.Method != "POST" {
		t.Errorf("method = %q, want the documented POST default", got.Method)
	}
	if got.TimeoutSeconds != models.ToolDefaultTimeoutSeconds {
		t.Errorf("timeout = %d, want %d", got.TimeoutSeconds, models.ToolDefaultTimeoutSeconds)
	}
	if got.URL != "https://api.example.com/v1/availability" {
		t.Errorf("url = %q, want it trimmed", got.URL)
	}
	if len(got.Headers) != 1 || got.Headers[0].Key != "Authorization" || got.Headers[0].Value != "Bearer x" {
		t.Errorf("headers = %#v, want one trimmed Authorization header", got.Headers)
	}
	if len(got.Parameters) != 1 || got.Parameters[0].Name != "date" || got.Parameters[0].Type != "string" {
		t.Errorf("parameters = %#v, want one trimmed string parameter", got.Parameters)
	}
	if normalizedAPIRequest(nil) != nil {
		t.Error("normalizedAPIRequest(nil) must stay nil")
	}
}

func TestParseToolListQuery(t *testing.T) {
	page, limit, toolType, errs := parseToolListQuery(url.Values{})
	if page != defaultToolPage || limit != defaultToolLimit || toolType != "" || len(errs) != 0 {
		t.Errorf("defaults = (%d, %d, %q, %v)", page, limit, toolType, errs)
	}

	page, limit, toolType, errs = parseToolListQuery(url.Values{
		"page":  {"3"},
		"limit": {"10"},
		"type":  {models.ToolTypeAPIRequest},
	})
	if page != 3 || limit != 10 || toolType != models.ToolTypeAPIRequest || len(errs) != 0 {
		t.Errorf("parsed = (%d, %d, %q, %v)", page, limit, toolType, errs)
	}

	_, _, _, errs = parseToolListQuery(url.Values{
		"page":  {"0"},
		"limit": {"5000"},
		"type":  {"dtmf"},
	})
	for _, field := range []string{"page", "limit", "type"} {
		if !hasField(errs, field) {
			t.Errorf("missing error for %s; got %v", field, fieldNames(errs))
		}
	}
}

// TestBuildToolListLinksCarriesTypeFilter pins that paging a filtered collection
// stays filtered — following "next" must not silently widen the result.
func TestBuildToolListLinksCarriesTypeFilter(t *testing.T) {
	links := buildToolListLinks(1, 50, 120, models.ToolTypeAPIRequest)
	if !strings.Contains(links.Self, "type=api_request") {
		t.Errorf("self = %q, want the type filter carried", links.Self)
	}
	if links.Next == nil || !strings.Contains(*links.Next, "type=api_request") {
		t.Errorf("next = %v, want the type filter carried", links.Next)
	}

	unfiltered := buildToolListLinks(1, 50, 120, "")
	if strings.Contains(unfiltered.Self, "type=") {
		t.Errorf("self = %q, want no type filter", unfiltered.Self)
	}
}
