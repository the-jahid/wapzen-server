package handlers

import (
	"net/url"
	"reflect"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func TestParseCallListQuery(t *testing.T) {
	tests := []struct {
		name      string
		query     url.Values
		wantPage  int
		wantLimit int
		wantErrs  []types.FieldError
	}{
		{
			name:      "defaults when absent",
			query:     url.Values{},
			wantPage:  1,
			wantLimit: 50,
		},
		{
			name:      "valid values parsed",
			query:     url.Values{"page": {"4"}, "limit": {"200"}},
			wantPage:  4,
			wantLimit: 200,
		},
		{
			name:      "invalid page",
			query:     url.Values{"page": {"0"}},
			wantPage:  1,
			wantLimit: 50,
			wantErrs:  []types.FieldError{{Field: "page", Message: "page must be a positive integer"}},
		},
		{
			name:      "limit out of range",
			query:     url.Values{"limit": {"201"}},
			wantPage:  1,
			wantLimit: 50,
			wantErrs:  []types.FieldError{{Field: "limit", Message: "limit must be between 1 and 200"}},
		},
		{
			name:      "non-numeric limit",
			query:     url.Values{"limit": {"many"}},
			wantPage:  1,
			wantLimit: 50,
			wantErrs:  []types.FieldError{{Field: "limit", Message: "limit must be between 1 and 200"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, limit, errs := parseCallListQuery(tt.query)
			if page != tt.wantPage || limit != tt.wantLimit {
				t.Errorf("page/limit = %d/%d, want %d/%d", page, limit, tt.wantPage, tt.wantLimit)
			}
			if !reflect.DeepEqual(errs, tt.wantErrs) {
				t.Errorf("errs = %#v, want %#v", errs, tt.wantErrs)
			}
		})
	}
}

// TestBuildCallListLinksUsesCallsPath pins the calls collection links to the
// /v1/calls base path with page-based query parameters.
func TestBuildCallListLinksUsesCallsPath(t *testing.T) {
	links := buildListLinks(callsListPath, 2, 50, 120, nil)

	if links.Self != "/v1/calls?page=2&limit=50" {
		t.Errorf("self = %q, want /v1/calls?page=2&limit=50", links.Self)
	}
	if links.First != "/v1/calls?page=1&limit=50" {
		t.Errorf("first = %q, want /v1/calls?page=1&limit=50", links.First)
	}
	if links.Last != "/v1/calls?page=3&limit=50" {
		t.Errorf("last = %q, want /v1/calls?page=3&limit=50", links.Last)
	}
	if links.Previous == nil || *links.Previous != "/v1/calls?page=1&limit=50" {
		t.Errorf("previous = %v, want /v1/calls?page=1&limit=50", links.Previous)
	}
	if links.Next == nil || *links.Next != "/v1/calls?page=3&limit=50" {
		t.Errorf("next = %v, want /v1/calls?page=3&limit=50", links.Next)
	}
}
