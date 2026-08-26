package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

func TestDecodeAddKnowledgeBaseSourcesRequest(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		// wantFields are the field names expected in the validation error body.
		wantFields []string
	}{
		{
			name:   "one text entry",
			body:   `{"knowledge_base_texts":[{"title":"Refund policy","text":"Refunds are processed within 14 days."}]}`,
			wantOK: true,
		},
		{
			name:       "empty body",
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"knowledge_base_texts":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "no sources at all",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts"},
		},
		{
			name:       "empty text list",
			body:       `{"knowledge_base_texts":[]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts"},
		},
		{
			name:       "text missing content",
			body:       `{"knowledge_base_texts":[{"title":"Q","text":"   "}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0]"},
		},
		{
			name:       "text missing title",
			body:       `{"knowledge_base_texts":[{"text":"Refunds are processed within 14 days."}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0]"},
		},
		{
			name:       "title too long",
			body:       `{"knowledge_base_texts":[{"title":"` + strings.Repeat("x", models.KnowledgeBaseMaxSourceTitleLength+1) + `","text":"Body"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0].title"},
		},
		{
			// The title is the vector store's record id, which the store requires
			// to be ASCII, so a title it would reject is caught here instead.
			name:       "non-ascii title rejected",
			body:       `{"knowledge_base_texts":[{"title":"退款政策","text":"Body"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0].title"},
		},
		{
			// A title carrying the chunk separator could collide with the id of
			// another source's second chunk.
			name:       "title with the chunk separator rejected",
			body:       `{"knowledge_base_texts":[{"title":"Refund policy#2","text":"Body"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_texts[0].title"},
		},
		{
			// Everything else printable is kept as-is: the id is the title.
			name:   "punctuation and spacing are accepted",
			body:   `{"knowledge_base_texts":[{"title":"Shipping & Delivery times! (v2)","text":"Body"}]}`,
			wantOK: true,
		},
		{
			// urls and files are accepted by the schema but cannot be indexed yet,
			// so they are refused rather than silently ignored.
			name:       "url sources rejected",
			body:       `{"knowledge_base_texts":[{"title":"Q","text":"A"}],"knowledge_base_urls":["https://docs.retellai.com"]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_urls"},
		},
		{
			name:       "file sources rejected",
			body:       `{"knowledge_base_files":[{"filename":"a.txt","file_url":"https://storage.example.com/a.txt"}]}`,
			wantStatus: http.StatusBadRequest,
			wantFields: []string{"knowledge_base_files", "knowledge_base_texts"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base/kb-1/sources", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			req, ok := decodeAddKnowledgeBaseSourcesRequest(w, r)
			if ok != tt.wantOK {
				t.Fatalf("decodeAddKnowledgeBaseSourcesRequest ok = %v, want %v (body: %s)", ok, tt.wantOK, w.Body.String())
			}
			if ok {
				if len(req.KnowledgeBaseTexts) == 0 {
					t.Errorf("accepted request carries no texts: %+v", req)
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

// The per-request text limit is a bound on the request, not on any single
// entry, so it is reported against the collection.
func TestDecodeAddKnowledgeBaseSourcesRequestTextLimit(t *testing.T) {
	entries := make([]string, 0, models.KnowledgeBaseMaxTextsPerRequest+1)
	for i := 0; i <= models.KnowledgeBaseMaxTextsPerRequest; i++ {
		entries = append(entries, `{"title":"Q","text":"A"}`)
	}
	body := `{"knowledge_base_texts":[` + strings.Join(entries, ",") + `]}`

	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base/kb-1/sources", strings.NewReader(body))
	w := httptest.NewRecorder()

	if _, ok := decodeAddKnowledgeBaseSourcesRequest(w, r); ok {
		t.Fatalf("accepted %d text entries, want rejection", models.KnowledgeBaseMaxTextsPerRequest+1)
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), `"field":"knowledge_base_texts"`) {
		t.Errorf("response missing knowledge_base_texts error: %s", w.Body.String())
	}
}

// The stored title and content are what gets indexed, so surrounding
// whitespace is trimmed before either reaches the database.
func TestDecodeAddKnowledgeBaseSourcesRequestTrims(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base/kb-1/sources",
		strings.NewReader(`{"knowledge_base_texts":[{"title":"  Refund policy  ","text":"  Refunds take 14 days.  "}]}`))
	w := httptest.NewRecorder()

	req, ok := decodeAddKnowledgeBaseSourcesRequest(w, r)
	if !ok {
		t.Fatalf("rejected a valid body: %s", w.Body.String())
	}
	if req.KnowledgeBaseTexts[0].Title != "Refund policy" {
		t.Errorf("title = %q, want it trimmed", req.KnowledgeBaseTexts[0].Title)
	}
	if req.KnowledgeBaseTexts[0].Text != "Refunds take 14 days." {
		t.Errorf("text = %q, want it trimmed", req.KnowledgeBaseTexts[0].Text)
	}
}
