package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func TestParseOutboundCampaignListQuery(t *testing.T) {
	tests := []struct {
		name       string
		query      url.Values
		wantPage   int
		wantLimit  int
		wantStatus string
		wantErrs   []types.FieldError
	}{
		{
			name:      "defaults when absent",
			query:     url.Values{},
			wantPage:  1,
			wantLimit: 20,
		},
		{
			name:      "blank values fall back to the defaults",
			query:     url.Values{"page": {"  "}, "limit": {""}, "status": {" "}},
			wantPage:  1,
			wantLimit: 20,
		},
		{
			name:       "valid values parsed",
			query:      url.Values{"page": {"3"}, "limit": {"100"}, "status": {"running"}},
			wantPage:   3,
			wantLimit:  100,
			wantStatus: "running",
		},
		{
			name:      "invalid page",
			query:     url.Values{"page": {"0"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs:  []types.FieldError{{Field: "page", Message: "page must be a positive integer"}},
		},
		{
			name:      "limit above the documented maximum",
			query:     url.Values{"limit": {"101"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs:  []types.FieldError{{Field: "limit", Message: "limit must be between 1 and 100"}},
		},
		{
			name:      "unknown status rejected",
			query:     url.Values{"status": {"archived"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs: []types.FieldError{{
				Field:   "status",
				Message: "status must be one of draft, running, paused, completed, failed",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, limit, status, errs := parseOutboundCampaignListQuery(tt.query)
			if page != tt.wantPage || limit != tt.wantLimit {
				t.Errorf("page/limit = %d/%d, want %d/%d", page, limit, tt.wantPage, tt.wantLimit)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if !reflect.DeepEqual(errs, tt.wantErrs) {
				t.Errorf("errs = %#v, want %#v", errs, tt.wantErrs)
			}
		})
	}
}

// TestBuildOutboundCampaignListLinksCarriesStatus pins the filter onto every
// page URL: paging through a filtered listing must stay filtered.
func TestBuildOutboundCampaignListLinksCarriesStatus(t *testing.T) {
	links := buildOutboundCampaignListLinks(2, 20, 45, "running")

	if links.Self != "/v1/outbound-campaigns?page=2&limit=20&status=running" {
		t.Errorf("self = %q", links.Self)
	}
	if links.First != "/v1/outbound-campaigns?page=1&limit=20&status=running" {
		t.Errorf("first = %q", links.First)
	}
	if links.Last != "/v1/outbound-campaigns?page=3&limit=20&status=running" {
		t.Errorf("last = %q", links.Last)
	}
	if links.Previous == nil || *links.Previous != "/v1/outbound-campaigns?page=1&limit=20&status=running" {
		t.Errorf("previous = %v", links.Previous)
	}
	if links.Next == nil || *links.Next != "/v1/outbound-campaigns?page=3&limit=20&status=running" {
		t.Errorf("next = %v", links.Next)
	}
}

func TestBuildOutboundCampaignListLinksWithoutStatus(t *testing.T) {
	links := buildOutboundCampaignListLinks(1, 20, 0, "")

	if links.Self != "/v1/outbound-campaigns?page=1&limit=20" {
		t.Errorf("self = %q", links.Self)
	}
	if links.Previous != nil || links.Next != nil {
		t.Errorf("previous/next = %v/%v, want nil/nil", links.Previous, links.Next)
	}
}

func TestDecodeCreateOutboundCampaignRequest(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		// wantFields are the field names expected in the validation error body.
		wantFields []string
		wantName   string
		wantBudget *float64
	}{
		{
			name:     "minimal valid body",
			body:     `{"campaign_name":"July reactivation"}`,
			wantOK:   true,
			wantName: "July reactivation",
		},
		{
			name:       "budget accepted",
			body:       `{"campaign_name":"July reactivation","budget_usd":250}`,
			wantOK:     true,
			wantName:   "July reactivation",
			wantBudget: float64Ptr(250),
		},
		{
			name:       "name is trimmed",
			body:       `{"campaign_name":"  July reactivation  "}`,
			wantOK:     true,
			wantName:   "July reactivation",
			wantBudget: nil,
		},
		{
			name:       "empty body",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"campaign_name":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "name required",
			body:       `{"budget_usd":250}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"campaign_name"},
		},
		{
			name:       "blank name rejected",
			body:       `{"campaign_name":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"campaign_name"},
		},
		{
			name:       "name over 80 characters",
			body:       `{"campaign_name":"` + strings.Repeat("a", 81) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"campaign_name"},
		},
		{
			name:       "negative budget",
			body:       `{"campaign_name":"July","budget_usd":-1}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"budget_usd"},
		},
		{
			name:       "counters are read-only",
			body:       `{"campaign_name":"July","calls_placed":10,"pickup_rate":0.5}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"calls_placed", "pickup_rate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/outbound-campaigns", strings.NewReader(tt.body))

			req, ok := decodeCreateOutboundCampaignRequest(rec, r)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (body: %s)", ok, tt.wantOK, rec.Body.String())
			}
			if !tt.wantOK {
				if rec.Code != tt.wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				assertFieldErrors(t, rec.Body.String(), tt.wantFields)
				return
			}

			if req.CampaignName != tt.wantName {
				t.Errorf("campaign_name = %q, want %q", req.CampaignName, tt.wantName)
			}
			switch {
			case tt.wantBudget == nil && req.BudgetUSD != nil:
				t.Errorf("budget_usd = %v, want nil", *req.BudgetUSD)
			case tt.wantBudget != nil && (req.BudgetUSD == nil || *req.BudgetUSD != *tt.wantBudget):
				t.Errorf("budget_usd = %v, want %v", req.BudgetUSD, *tt.wantBudget)
			}
		})
	}
}

func TestDecodeUpdateOutboundCampaignRequest(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantOK          bool
		wantStatus      int
		wantFields      []string
		wantBudget      *float64
		wantClearBudget bool
	}{
		{
			name:   "status only",
			body:   `{"status":"running"}`,
			wantOK: true,
		},
		{
			name:       "budget set",
			body:       `{"budget_usd":500}`,
			wantOK:     true,
			wantBudget: float64Ptr(500),
		},
		{
			// Null is the only way to uncap a campaign: an omitted field keeps the
			// stored budget, so the two cases must not collapse.
			name:            "budget cleared with null",
			body:            `{"budget_usd":null}`,
			wantOK:          true,
			wantClearBudget: true,
		},
		{
			name:       "empty object changes nothing",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"body"},
		},
		{
			name:       "empty body",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown status",
			body:       `{"status":"archived"}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"status"},
		},
		{
			name:       "blank name rejected",
			body:       `{"campaign_name":"  "}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"campaign_name"},
		},
		{
			name:       "negative budget",
			body:       `{"budget_usd":-5}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"budget_usd"},
		},
		{
			name:       "non-numeric budget",
			body:       `{"budget_usd":"lots"}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"budget_usd"},
		},
		{
			name:       "counters are read-only",
			body:       `{"status":"running","total_usage_seconds":900}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"total_usage_seconds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPatch, "/v1/outbound-campaigns/c1", strings.NewReader(tt.body))

			req, ok := decodeUpdateOutboundCampaignRequest(rec, r)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (body: %s)", ok, tt.wantOK, rec.Body.String())
			}
			if !tt.wantOK {
				if rec.Code != tt.wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				assertFieldErrors(t, rec.Body.String(), tt.wantFields)
				return
			}

			if req.clearBudget != tt.wantClearBudget {
				t.Errorf("clearBudget = %v, want %v", req.clearBudget, tt.wantClearBudget)
			}
			switch {
			case tt.wantBudget == nil && req.budget != nil:
				t.Errorf("budget = %v, want nil", *req.budget)
			case tt.wantBudget != nil && (req.budget == nil || *req.budget != *tt.wantBudget):
				t.Errorf("budget = %v, want %v", req.budget, *tt.wantBudget)
			}
		})
	}
}

func float64Ptr(v float64) *float64 { return &v }

// assertFieldErrors checks that the rendered error body names every expected
// field, without pinning the exact messages.
func assertFieldErrors(t *testing.T, body string, fields []string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(body, `"field":"`+field+`"`) {
			t.Errorf("error body is missing field %q: %s", field, body)
		}
	}
}
