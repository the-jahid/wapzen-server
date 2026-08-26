package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

// uploadPart is one file part of a test upload.
type uploadPart struct {
	filename string
	content  string
}

// uploadRequest builds a multipart request the way a browser sends one: repeated
// "files" parts, and one "titles" value per file in the same order.
func uploadRequest(t *testing.T, files []uploadPart, titles []string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, title := range titles {
		if err := form.WriteField(models.KnowledgeBaseUploadTitlesFormField, title); err != nil {
			t.Fatalf("write title field: %v", err)
		}
	}
	for _, file := range files {
		part, err := form.CreateFormFile(models.KnowledgeBaseUploadFilesFormField, file.filename)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write([]byte(file.content)); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base/kb-1/sources", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	return r
}

// An upload is stored as file sources whose text this server extracted, so what
// lands in the knowledge base never depends on what the client parsed.
func TestReadAddSourcesEntriesFromUpload(t *testing.T) {
	r := uploadRequest(t,
		[]uploadPart{
			{filename: "refund policy.txt", content: "Refunds are processed within 14 days.\r\n"},
			{filename: "faq.md", content: "# FAQ\n\nWe ship worldwide."},
		},
		[]string{"Refund policy"},
	)
	w := httptest.NewRecorder()

	entries, ok := readAddSourcesEntries(w, r)
	if !ok {
		t.Fatalf("upload rejected: %s", w.Body.String())
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	// The supplied title wins for the first file; the second falls back to its
	// filename, extension included.
	if entries[0].Title != "Refund policy" {
		t.Errorf("entries[0].Title = %q, want the supplied title", entries[0].Title)
	}
	if entries[1].Title != "faq.md" {
		t.Errorf("entries[1].Title = %q, want it derived from the filename", entries[1].Title)
	}
	for i, entry := range entries {
		if entry.Type != models.KnowledgeBaseSourceTypeFile {
			t.Errorf("entries[%d].Type = %q, want %q", i, entry.Type, models.KnowledgeBaseSourceTypeFile)
		}
	}
	// The stored text is the normalized extraction, not the raw bytes.
	if entries[0].Text != "Refunds are processed within 14 days." {
		t.Errorf("entries[0].Text = %q, want it extracted and normalized", entries[0].Text)
	}
	if !strings.Contains(entries[1].Text, "We ship worldwide.") {
		t.Errorf("entries[1].Text = %q, want the markdown body", entries[1].Text)
	}
}

// A blank title falls back to the filename rather than failing: the field is
// there to override the default, and an empty one states no preference.
func TestReadAddSourcesEntriesUploadBlankTitleFallsBack(t *testing.T) {
	r := uploadRequest(t, []uploadPart{{filename: "handbook.txt", content: "Body."}}, []string{"   "})
	w := httptest.NewRecorder()

	entries, ok := readAddSourcesEntries(w, r)
	if !ok {
		t.Fatalf("upload rejected: %s", w.Body.String())
	}
	if entries[0].Title != "handbook.txt" {
		t.Errorf("title = %q, want the filename", entries[0].Title)
	}
}

// One unreadable file fails the whole upload: indexing is all-or-nothing, so a
// partial answer would be a lie about what the knowledge base holds.
func TestReadAddSourcesEntriesUploadRejectsUnreadableFile(t *testing.T) {
	r := uploadRequest(t, []uploadPart{
		{filename: "good.txt", content: "Body."},
		{filename: "logo.png", content: "\x89PNG\x00"},
	}, nil)
	w := httptest.NewRecorder()

	if _, ok := readAddSourcesEntries(w, r); ok {
		t.Fatal("upload accepted, want the unsupported file rejected")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"field":"files[1]"`) {
		t.Errorf("response does not name the offending file: %s", body)
	}
	// The message has to say which file and what to send instead.
	if !strings.Contains(body, "logo.png") || !strings.Contains(body, ".pdf") {
		t.Errorf("response does not explain the rejection: %s", body)
	}
}

func TestReadAddSourcesEntriesUploadRequiresAFile(t *testing.T) {
	r := uploadRequest(t, nil, []string{"Refund policy"})
	w := httptest.NewRecorder()

	if _, ok := readAddSourcesEntries(w, r); ok {
		t.Fatal("empty upload accepted, want rejection")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), `"field":"files"`) {
		t.Errorf("response missing files error: %s", w.Body.String())
	}
}

func TestReadAddSourcesEntriesUploadEnforcesFileCount(t *testing.T) {
	files := make([]uploadPart, 0, models.KnowledgeBaseMaxFilesPerRequest+1)
	for i := 0; i <= models.KnowledgeBaseMaxFilesPerRequest; i++ {
		files = append(files, uploadPart{filename: "note.txt", content: "Body."})
	}

	w := httptest.NewRecorder()
	if _, ok := readAddSourcesEntries(w, uploadRequest(t, files, nil)); ok {
		t.Fatalf("accepted %d files, want rejection", len(files))
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// A JSON body still reads as text sources, so adding the upload path did not
// change what the documented endpoint does.
func TestReadAddSourcesEntriesFromJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/knowledge-base/kb-1/sources",
		strings.NewReader(`{"knowledge_base_texts":[{"title":"Refund policy","text":"Refunds take 14 days."}]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	entries, ok := readAddSourcesEntries(w, r)
	if !ok {
		t.Fatalf("body rejected: %s", w.Body.String())
	}
	if len(entries) != 1 || entries[0].Type != models.KnowledgeBaseSourceTypeText {
		t.Fatalf("entries = %+v, want one text source", entries)
	}
}

func TestSourceTitleFromFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{
			// The extension is kept: it says what the source is, and it keeps an
			// uploaded document from colliding with a pasted text of the same name.
			name:     "plain filename",
			filename: "Refund policy.pdf",
			want:     "Refund policy.pdf",
		},
		{
			name:     "path is dropped",
			filename: `C:\Users\me\Documents\handbook.docx`,
			want:     "handbook.docx",
		},
		{
			// The title is the vector store record id, which has to be ASCII.
			name:     "non-ascii is replaced",
			filename: "résumé.pdf",
			want:     "r-sum-.pdf",
		},
		{
			// A title carrying the separator could collide with the id of another
			// source's second chunk.
			name:     "chunk separator is replaced",
			filename: "invoice #42.pdf",
			want:     "invoice -42.pdf",
		},
		{
			name:     "whitespace collapses",
			filename: "  spaced   out.txt  ",
			want:     "spaced out.txt",
		},
		{
			// A filename with no ASCII left to keep still yields a storable title
			// rather than an empty one; the caller can send a better one alongside
			// the file.
			name:     "nothing usable",
			filename: "退款.txt",
			want:     ".txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceTitleFromFilename(tt.filename); got != tt.want {
				t.Errorf("sourceTitleFromFilename(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

// Whatever a filename turns into still has to be storable, since the derived
// title goes straight into the vector store as a record id.
func TestSourceTitleFromFilenameStaysValid(t *testing.T) {
	filenames := []string{
		"Refund policy.pdf",
		"退款.txt",
		"invoice #42.pdf",
		strings.Repeat("x", models.KnowledgeBaseMaxSourceTitleLength+50) + ".txt",
	}
	for _, filename := range filenames {
		title := sourceTitleFromFilename(filename)
		if message := describeInvalidSourceTitle(title); message != "" {
			t.Errorf("sourceTitleFromFilename(%q) = %q, which is not storable: %s", filename, title, message)
		}
	}
}
