package handlers

import (
	"reflect"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func boolPtr(b bool) *bool    { return &b }

func TestValidateUpdateKnowledgeBaseRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      updateKnowledgeBaseRequest
		wantErrs []types.FieldError
	}{
		{
			name: "empty body changes nothing",
			req:  updateKnowledgeBaseRequest{},
			wantErrs: []types.FieldError{{
				Field:   "body",
				Message: "supply at least one of knowledge_base_name, enable_auto_refresh, max_chunk_size or min_chunk_size",
			}},
		},
		{
			name: "auto refresh alone is a valid update",
			req:  updateKnowledgeBaseRequest{EnableAutoRefresh: boolPtr(true)},
		},
		{
			name: "settings at their bounds are accepted",
			req: updateKnowledgeBaseRequest{
				MaxChunkSize: intPtr(6000),
				MinChunkSize: intPtr(200),
			},
		},
		{
			name: "rename is accepted",
			req:  updateKnowledgeBaseRequest{KnowledgeBaseName: strPtr("Support handbook")},
		},
		{
			name: "name present but blank",
			req:  updateKnowledgeBaseRequest{KnowledgeBaseName: strPtr("")},
			wantErrs: []types.FieldError{{
				Field:   "knowledge_base_name",
				Message: "knowledge_base_name must not be empty",
			}},
		},
		{
			name: "name too long",
			req:  updateKnowledgeBaseRequest{KnowledgeBaseName: strPtr("0123456789012345678901234567890123456789x")},
			wantErrs: []types.FieldError{{
				Field:   "knowledge_base_name",
				Message: "knowledge_base_name must be at most 40 characters",
			}},
		},
		{
			name: "max chunk size below its floor",
			req:  updateKnowledgeBaseRequest{MaxChunkSize: intPtr(599)},
			wantErrs: []types.FieldError{{
				Field:   "max_chunk_size",
				Message: "max_chunk_size must be between 600 and 6000",
			}},
		},
		{
			name: "min chunk size above its ceiling",
			req:  updateKnowledgeBaseRequest{MinChunkSize: intPtr(2001)},
			wantErrs: []types.FieldError{{
				Field:   "min_chunk_size",
				Message: "min_chunk_size must be between 200 and 2000",
			}},
		},
		{
			name: "both chunk sizes out of range report both",
			req: updateKnowledgeBaseRequest{
				MaxChunkSize: intPtr(6001),
				MinChunkSize: intPtr(199),
			},
			wantErrs: []types.FieldError{
				{Field: "max_chunk_size", Message: "max_chunk_size must be between 600 and 6000"},
				{Field: "min_chunk_size", Message: "min_chunk_size must be between 200 and 2000"},
			},
		},
		{
			// min > max is legal here: the ordering is checked by the handler
			// against the stored row, since the body may supply only one side.
			name: "ordering is not checked without the stored row",
			req: updateKnowledgeBaseRequest{
				MaxChunkSize: intPtr(600),
				MinChunkSize: intPtr(2000),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateUpdateKnowledgeBaseRequest(tt.req)
			if !reflect.DeepEqual(errs, tt.wantErrs) {
				t.Errorf("errs = %#v, want %#v", errs, tt.wantErrs)
			}
		})
	}
}
