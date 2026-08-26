package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

func TestDecodeCreateKnowledgeBaseRequest(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		// wantFields are the field names expected in the validation error body.
		wantFields []string
	}{
		{
			name:   "name only",
			body:   `{"knowledge_base_name":"Sample KB"}`,
			wantOK: true,
		},
		{
			name: "settings and indexable texts",
			body: `{"knowledge_base_name":"Sample KB",
				"knowledge_base_texts":[{"title":"Q","text":"A"},{"title":"Q2","text":"B"}],
				"enable_auto_refresh":true,"max_chunk_size":2000,"min_chunk_size":400}`,
			wantOK: true,
		},
		{
			name:       "empty body",
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"knowledge_base_name":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing name",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_name"},
		},
		{
			name:       "whitespace-only name counts as missing",
			body:       `{"knowledge_base_name":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_name"},
		},
		{
			name:       "name too long",
			body:       `{"knowledge_base_name":"` + strings.Repeat("x", 41) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_name"},
		},
		{
			name:       "chunk sizes out of range",
			body:       `{"knowledge_base_name":"KB","max_chunk_size":100,"min_chunk_size":9000}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"max_chunk_size", "min_chunk_size"},
		},
		{
			name:       "min above max",
			body:       `{"knowledge_base_name":"KB","max_chunk_size":600,"min_chunk_size":2000}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"min_chunk_size"},
		},
		{
			name:       "text entry missing content",
			body:       `{"knowledge_base_name":"KB","knowledge_base_texts":[{"title":"Q","text":"  "}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0]"},
		},
		{
			// A title becomes the vector store record id verbatim, so it has to be
			// usable as one before the knowledge base row is written.
			name:       "title not usable as a vector id",
			body:       `{"knowledge_base_name":"KB","knowledge_base_texts":[{"title":"Refund#policy","text":"A"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0].title"},
		},
		{
			name:       "duplicate titles in one request",
			body:       `{"knowledge_base_name":"KB","knowledge_base_texts":[{"title":"Q","text":"A"},{"title":"Q","text":"B"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[1].title"},
		},
		{
			// Urls have no fetcher, so accepting them would promise content that
			// never gets indexed — the behaviour Create just stopped.
			name:       "urls refused rather than dropped",
			body:       `{"knowledge_base_name":"KB","knowledge_base_urls":["https://www.example.com"]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_urls"},
		},
		{
			name:       "linked files refused rather than dropped",
			body:       `{"knowledge_base_name":"KB","knowledge_base_files":[{"filename":"a.txt","file_url":"https://storage.example.com/a.txt"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_files"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			req, ok := decodeCreateKnowledgeBaseRequest(w, r)
			if ok != tt.wantOK {
				t.Fatalf("decodeCreateKnowledgeBaseRequest ok = %v, want %v (body: %s)", ok, tt.wantOK, w.Body.String())
			}
			if ok {
				if req.KnowledgeBaseName == "" {
					t.Errorf("accepted request has empty name: %+v", req)
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			for _, field := range tt.wantFields {
				if !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
					t.Errorf("response missing field error for %q: %s", field, w.Body.String())
				}
			}
		})
	}
}

// Too many texts is a limit on the request, not on any single entry, so it is
// reported against the collection. Create indexes what it is given inside the
// request, so this is what stops one call turning into unbounded embedding work.
func TestDecodeCreateKnowledgeBaseRequestTextLimit(t *testing.T) {
	texts := make([]string, 0, models.KnowledgeBaseMaxTextsPerRequest+1)
	for i := 0; i <= models.KnowledgeBaseMaxTextsPerRequest; i++ {
		texts = append(texts, fmt.Sprintf(`{"title":"T%d","text":"body"}`, i))
	}
	body := `{"knowledge_base_name":"KB","knowledge_base_texts":[` + strings.Join(texts, ",") + `]}`

	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base", strings.NewReader(body))
	w := httptest.NewRecorder()

	if _, ok := decodeCreateKnowledgeBaseRequest(w, r); ok {
		t.Fatalf("decodeCreateKnowledgeBaseRequest accepted %d texts, want rejection", models.KnowledgeBaseMaxTextsPerRequest+1)
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), `"field":"knowledge_base_texts"`) {
		t.Errorf("response missing knowledge_base_texts error: %s", w.Body.String())
	}
}

// The texts a valid create carries have to survive decoding, trimmed and in
// order — they are what gets indexed, so losing them here would be the original
// bug in a new place.
func TestDecodeCreateKnowledgeBaseRequestKeepsTexts(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base", strings.NewReader(
		`{"knowledge_base_name":"KB","knowledge_base_texts":[{"title":"  Refund policy  ","text":"  Refunds take five days.  "}]}`))
	w := httptest.NewRecorder()

	req, ok := decodeCreateKnowledgeBaseRequest(w, r)
	if !ok {
		t.Fatalf("decodeCreateKnowledgeBaseRequest rejected a valid body: %s", w.Body.String())
	}
	if len(req.KnowledgeBaseTexts) != 1 {
		t.Fatalf("texts = %#v, want the supplied one", req.KnowledgeBaseTexts)
	}
	if req.KnowledgeBaseTexts[0].Title != "Refund policy" || req.KnowledgeBaseTexts[0].Text != "Refunds take five days." {
		t.Fatalf("text entry was not trimmed: %#v", req.KnowledgeBaseTexts[0])
	}
}

// Optional settings absent from the body must stay nil so the insert falls back
// to the column defaults rather than storing a zero value.
func TestDecodeCreateKnowledgeBaseRequestLeavesOptionalSettingsNil(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base",
		strings.NewReader(`{"knowledge_base_name":"  Sample KB  "}`))
	w := httptest.NewRecorder()

	req, ok := decodeCreateKnowledgeBaseRequest(w, r)
	if !ok {
		t.Fatalf("decodeCreateKnowledgeBaseRequest rejected a valid body: %s", w.Body.String())
	}
	if req.KnowledgeBaseName != "Sample KB" {
		t.Errorf("name = %q, want %q", req.KnowledgeBaseName, "Sample KB")
	}
	if req.EnableAutoRefresh != nil || req.MaxChunkSize != nil || req.MinChunkSize != nil {
		t.Errorf("absent settings should stay nil, got %+v", req)
	}
}
