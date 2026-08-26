package handlers

import (
	"net/url"
	"reflect"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func TestParseKnowledgeBaseListQuery(t *testing.T) {
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
			wantLimit: 20,
		},
		{
			name:      "blank values fall back to the defaults",
			query:     url.Values{"page": {"  "}, "limit": {""}},
			wantPage:  1,
			wantLimit: 20,
		},
		{
			name:      "valid values parsed",
			query:     url.Values{"page": {"3"}, "limit": {"100"}},
			wantPage:  3,
			wantLimit: 100,
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
			name:      "non-numeric limit",
			query:     url.Values{"limit": {"many"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs:  []types.FieldError{{Field: "limit", Message: "limit must be between 1 and 100"}},
		},
		{
			name:      "both invalid report both fields",
			query:     url.Values{"page": {"-2"}, "limit": {"0"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs: []types.FieldError{
				{Field: "page", Message: "page must be a positive integer"},
				{Field: "limit", Message: "limit must be between 1 and 100"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, limit, errs := parseKnowledgeBaseListQuery(tt.query)
			if page != tt.wantPage || limit != tt.wantLimit {
				t.Errorf("page/limit = %d/%d, want %d/%d", page, limit, tt.wantPage, tt.wantLimit)
			}
			if !reflect.DeepEqual(errs, tt.wantErrs) {
				t.Errorf("errs = %#v, want %#v", errs, tt.wantErrs)
			}
		})
	}
}

// TestBuildKnowledgeBaseListLinksUsesCollectionPath pins the knowledge base
// collection links to the /v1/knowledge-base base path with page-based query
// parameters, matching the documented listKnowledgeBases example.
func TestBuildKnowledgeBaseListLinksUsesCollectionPath(t *testing.T) {
	links := buildListLinks(knowledgeBasesListPath, 2, 20, 45, nil)

	if links.Self != "/v1/knowledge-base?page=2&limit=20" {
		t.Errorf("self = %q, want /v1/knowledge-base?page=2&limit=20", links.Self)
	}
	if links.First != "/v1/knowledge-base?page=1&limit=20" {
		t.Errorf("first = %q, want /v1/knowledge-base?page=1&limit=20", links.First)
	}
	if links.Last != "/v1/knowledge-base?page=3&limit=20" {
		t.Errorf("last = %q, want /v1/knowledge-base?page=3&limit=20", links.Last)
	}
	if links.Previous == nil || *links.Previous != "/v1/knowledge-base?page=1&limit=20" {
		t.Errorf("previous = %v, want /v1/knowledge-base?page=1&limit=20", links.Previous)
	}
	if links.Next == nil || *links.Next != "/v1/knowledge-base?page=3&limit=20" {
		t.Errorf("next = %v, want /v1/knowledge-base?page=3&limit=20", links.Next)
	}
}

// TestBuildKnowledgeBaseListLinksOnEmptyCollection pins the link block a user
// with no knowledge bases sees: a single first/last page and no neighbours.
func TestBuildKnowledgeBaseListLinksOnEmptyCollection(t *testing.T) {
	links := buildListLinks(knowledgeBasesListPath, 1, 20, 0, nil)

	if links.Last != "/v1/knowledge-base?page=1&limit=20" {
		t.Errorf("last = %q, want /v1/knowledge-base?page=1&limit=20", links.Last)
	}
	if links.Previous != nil || links.Next != nil {
		t.Errorf("previous/next = %v/%v, want nil/nil", links.Previous, links.Next)
	}
}
