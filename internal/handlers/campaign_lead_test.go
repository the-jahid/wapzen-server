package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

func TestIsE164(t *testing.T) {
	for _, value := range []string{"+14155550123", "+8801712345678", "+12345678"} {
		if !isCampaignLeadE164(value) {
			t.Errorf("isE164(%q) = false", value)
		}
	}
	for _, value := range []string{"14155550123", "+012345678", "+12 345678", "+123", "+1234567890123456"} {
		if isCampaignLeadE164(value) {
			t.Errorf("isE164(%q) = true", value)
		}
	}
}

func TestDecodeCreateCampaignLeadRequest(t *testing.T) {
	tests := []struct {
		name, body string
		ok         bool
		fields     []string
	}{
		{"valid", `{"phone_number":"+14155550123","email":"maya@example.com","first_name":" Maya "}`, true, nil},
		{"missing phone", `{"email":"maya@example.com"}`, false, []string{"phone_number"}},
		{"invalid email", `{"phone_number":"+14155550123","email":"bad"}`, false, []string{"email"}},
		{"read only", `{"phone_number":"+14155550123","attempts":2}`, false, []string{"attempts"}},
		{"create status is server managed", `{"phone_number":"+14155550123","status":"contacted"}`, false, []string{"status"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			got, ok := decodeCreateCampaignLeadRequest(rec, req)
			if ok != tt.ok {
				t.Fatalf("ok=%v body=%s", ok, rec.Body.String())
			}
			if ok && (got.FirstName == nil || *got.FirstName != "Maya") {
				t.Errorf("first_name=%v", got.FirstName)
			}
			assertFieldErrors(t, rec.Body.String(), tt.fields)
		})
	}
}

func TestDecodeUpdateCampaignLeadRequestClearsOptionalFields(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"email":null,"first_name":""}`))
	got, ok := decodeUpdateCampaignLeadRequest(rec, req)
	if !ok {
		t.Fatalf("body rejected: %s", rec.Body.String())
	}
	if !got.Email.Present || got.Email.Value != nil || !got.FirstName.Present || got.FirstName.Value != nil {
		t.Fatalf("nullable fields not preserved: %#v", got)
	}
}

func TestParseCampaignLeadListQuery(t *testing.T) {
	page, limit, status, errs := parseCampaignLeadListQuery(url.Values{"page": {"2"}, "limit": {"50"}, "status": {"contacted"}})
	if page != 2 || limit != 50 || status != "contacted" || len(errs) != 0 {
		t.Fatalf("got %d %d %q %#v", page, limit, status, errs)
	}
}

// TestCampaignDialBlockReason pins which campaigns dial a lead the moment it is
// added. A draft dials — that is how a campaign is tried out before it is put on
// the air — but one with nothing to dial from, or one that has stopped dialling,
// takes the lead without calling it.
func TestCampaignDialBlockReason(t *testing.T) {
	agent, number := "agent_1", "phone_number_1"
	campaign := func(status string, agentID, phoneNumberID *string) models.OutboundCampaign {
		return models.OutboundCampaign{Status: status, AgentID: agentID, PhoneNumberID: phoneNumberID}
	}
	blank := ""

	tests := []struct {
		name     string
		campaign models.OutboundCampaign
		want     string
	}{
		{"draft dials", campaign(models.CampaignStatusDraft, &agent, &number), ""},
		{"running dials", campaign(models.CampaignStatusRunning, &agent, &number), ""},
		{"no agent", campaign(models.CampaignStatusDraft, nil, nil), "this campaign has no agent to dial with, so the lead was added without calling it"},
		{"blank agent", campaign(models.CampaignStatusDraft, &blank, &number), "this campaign has no agent to dial with, so the lead was added without calling it"},
		{"agent without number", campaign(models.CampaignStatusDraft, &agent, nil), "this campaign's agent has no phone number assigned, so the lead was added without calling it"},
		{"paused", campaign(models.CampaignStatusPaused, &agent, &number), "the campaign is paused, so the lead was added without calling it"},
		{"completed", campaign(models.CampaignStatusCompleted, &agent, &number), "the campaign is completed, so the lead was added without calling it"},
		{"failed", campaign(models.CampaignStatusFailed, &agent, &number), "the campaign is failed, so the lead was added without calling it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := campaignDialBlockReason(tt.campaign); got != tt.want {
				t.Errorf("campaignDialBlockReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildCampaignCallsListLinks pins the campaign call history to its own
// collection URL, so paging through it does not point back at /v1/calls.
func TestBuildCampaignCallsListLinks(t *testing.T) {
	links := buildListLinks(outboundCampaignsListPath+"/campaign_1/calls", 1, 50, 120, nil)

	if links.Self != "/v1/outbound-campaigns/campaign_1/calls?page=1&limit=50" {
		t.Errorf("self = %q", links.Self)
	}
	if links.Next == nil || *links.Next != "/v1/outbound-campaigns/campaign_1/calls?page=2&limit=50" {
		t.Errorf("next = %v", links.Next)
	}
}
