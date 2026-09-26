package agents

import (
	"net/url"
	"reflect"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/examples"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func TestParseListQuery(t *testing.T) {
	tests := []struct {
		name       string
		query      url.Values
		wantPage   int
		wantLimit  int
		wantFields []string
		wantErrs   []types.FieldError
	}{
		{
			name:      "defaults when absent",
			query:     url.Values{},
			wantPage:  1,
			wantLimit: 20,
		},
		{
			name:       "valid values parsed",
			query:      url.Values{"page": {"3"}, "limit": {"50"}, "fields": {"id,agent.status"}},
			wantPage:   3,
			wantLimit:  50,
			wantFields: []string{"id", "agent.status"},
		},
		{
			name:      "invalid page",
			query:     url.Values{"page": {"0"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs:  []types.FieldError{{Field: "page", Message: "page must be a positive integer"}},
		},
		{
			name:      "limit out of range",
			query:     url.Values{"limit": {"101"}},
			wantPage:  1,
			wantLimit: 20,
			wantErrs:  []types.FieldError{{Field: "limit", Message: "limit must be between 1 and 100"}},
		},
		{
			name:      "unknown field rejected",
			query:     url.Values{"fields": {"id,agent.unknown"}},
			wantPage:  1,
			wantLimit: 20,
			// id is still collected; the unknown entry produces one error.
			wantFields: []string{"id"},
			wantErrs:   []types.FieldError{{Field: "fields", Message: "unknown field 'agent.unknown' in fields selection"}},
		},
		{
			name:       "blank field entries skipped",
			query:      url.Values{"fields": {"id, ,agent.status,"}},
			wantPage:   1,
			wantLimit:  20,
			wantFields: []string{"id", "agent.status"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, errs := parseListQuery(tt.query)
			page, limit, fields := query.page, query.limit, query.fields
			if page != tt.wantPage || limit != tt.wantLimit {
				t.Errorf("page/limit = %d/%d, want %d/%d", page, limit, tt.wantPage, tt.wantLimit)
			}
			if !reflect.DeepEqual(fields, tt.wantFields) {
				t.Errorf("fields = %#v, want %#v", fields, tt.wantFields)
			}
			if !reflect.DeepEqual(errs, tt.wantErrs) {
				t.Errorf("errs = %#v, want %#v", errs, tt.wantErrs)
			}
		})
	}
}

// TestApplyFieldSelectionEmptyIsNonNilSlice guards against an empty page
// serializing as JSON null instead of [].
func TestApplyFieldSelectionEmptyIsNonNilSlice(t *testing.T) {
	data, err := applyFieldSelection(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := data.([]types.AgentResource)
	if !ok {
		t.Fatalf("expected []types.AgentResource, got %T", data)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("expected non-nil empty slice, got %#v", got)
	}
}

// TestSelectFieldsMatchesDocumentedExample confirms the sparse-fieldset
// projection produces exactly the shape advertised by the static docs example.
func TestSelectFieldsMatchesDocumentedExample(t *testing.T) {
	fields := []string{"id", "agent.status", "agent.language", "agent.call_direction"}
	data, err := applyFieldSelection([]types.AgentResource{examples.AgentResourceExample}, fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := data.([]map[string]any)
	if !ok || len(got) != 1 {
		t.Fatalf("expected one projected map, got %T len %d", data, len(got))
	}

	want := map[string]any{
		"id": "agent_12345",
		"agent": map[string]any{
			"status":         "active",
			"language":       "en-US",
			"call_direction": "outbound",
		},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("projection = %#v, want %#v", got[0], want)
	}
}
