package knowledgebases

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

func TestExtractTextPlainFormats(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		data     []byte
		want     string
	}{
		{
			name:     "plain text",
			filename: "policy.txt",
			data:     []byte("Refunds are processed within 14 days."),
			want:     "Refunds are processed within 14 days.",
		},
		{
			// Windows editors write CRLF and often a byte order mark; neither is
			// content, and both would otherwise end up in the indexed text.
			name:     "utf-8 bom and crlf",
			filename: "notes.md",
			data:     append([]byte{0xEF, 0xBB, 0xBF}, []byte("# Title\r\n\r\nBody line.\r\n")...),
			want:     "# Title\n\nBody line.",
		},
		{
			name:     "utf-16 little endian",
			filename: "notes.txt",
			data:     append([]byte{0xFF, 0xFE}, 'H', 0, 'i', 0),
			want:     "Hi",
		},
		{
			name:     "utf-16 big endian",
			filename: "notes.txt",
			data:     append([]byte{0xFE, 0xFF}, 0, 'H', 0, 'i'),
			want:     "Hi",
		},
		{
			// The chunker splits on blank lines, so a run of them would turn one
			// document into one chunk per line.
			name:     "blank line runs collapse",
			filename: "spaced.txt",
			data:     []byte("First.\n\n\n\n\nSecond."),
			want:     "First.\n\nSecond.",
		},
		{
			// A non-breaking space reads as a space but does not match one, which
			// would make the indexed text unsearchable in the places PDFs use them.
			name:     "non-breaking spaces become spaces",
			filename: "spaced.txt",
			data:     []byte("14\u00a0days\u200b."),
			want:     "14 days.",
		},
		{
			name:     "extension casing is ignored",
			filename: "READ.MD",
			data:     []byte("Body."),
			want:     "Body.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractText(tt.filename, tt.data)
			if err != nil {
				t.Fatalf("ExtractText(%q): %v", tt.filename, err)
			}
			if got != tt.want {
				t.Errorf("ExtractText(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestExtractTextHTML(t *testing.T) {
	page := `<!doctype html>
<html><head><title>Refunds</title>
<style>body { color: red; }</style>
<script>console.log("tracking");</script>
</head>
<body>
<!-- internal note -->
<h1>Refund&nbsp;policy</h1>
<p>Refunds take 14 days.</p><p>Shipping &amp; handling is not refunded.</p>
</body></html>`

	got, err := ExtractText("policy.html", []byte(page))
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}

	for _, unwanted := range []string{"color: red", "console.log", "internal note", "<p>", "&amp;"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("extracted text still carries %q:\n%s", unwanted, got)
		}
	}
	for _, wanted := range []string{"Refund policy", "Refunds take 14 days.", "Shipping & handling is not refunded."} {
		if !strings.Contains(got, wanted) {
			t.Errorf("extracted text is missing %q:\n%s", wanted, got)
		}
	}
	// Two paragraphs must not be welded into one word by stripping their tags.
	if strings.Contains(got, "daysShipping") {
		t.Errorf("paragraph boundary was lost:\n%s", got)
	}
}

// A .docx is a zip of XML parts, so the fixture is built the same way Word
// writes one: the body holds paragraphs of text runs.
func TestExtractTextDOCX(t *testing.T) {
	document := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Refund </w:t></w:r><w:r><w:t>policy</w:t></w:r></w:p>
    <w:p><w:r><w:t>Refunds take 14 days.</w:t><w:br/><w:t>No exceptions.</w:t></w:r></w:p>
  </w:body>
</w:document>`

	got, err := ExtractText("policy.docx", docxFixture(t, map[string]string{"word/document.xml": document}))
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}

	// Runs inside one paragraph are one line; the paragraph break is a blank
	// line, which is the boundary the chunker prefers.
	want := "Refund policy\n\nRefunds take 14 days.\nNo exceptions."
	if got != want {
		t.Errorf("ExtractText = %q, want %q", got, want)
	}
}

func TestExtractTextDOCXWithoutBody(t *testing.T) {
	_, err := ExtractText("broken.docx", docxFixture(t, map[string]string{"word/styles.xml": "<styles/>"}))
	if !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("error = %v, want ErrUnsupportedFileType", err)
	}
}

// A PDF is the format this feature exists for, so the fixture is a real one,
// built here rather than checked in as a binary: the offsets a PDF's cross
// reference table needs are computed, so the file is valid without a tool.
func TestExtractTextPDF(t *testing.T) {
	got, err := ExtractText("policy.pdf", minimalPDF(t, "Refunds take 14 days."))
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if !strings.Contains(got, "Refunds take 14 days.") {
		t.Errorf("ExtractText = %q, want the page's text", got)
	}
}

func TestExtractTextRejections(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		data     []byte
		want     error
	}{
		{
			name:     "unknown extension",
			filename: "installer.exe",
			data:     []byte("MZ"),
			want:     ErrUnsupportedFileType,
		},
		{
			name:     "no extension",
			filename: "README",
			data:     []byte("Body."),
			want:     ErrUnsupportedFileType,
		},
		{
			// .doc is a different format, not an older .docx, so it cannot be
			// read by unzipping it.
			name:     "legacy doc",
			filename: "policy.doc",
			data:     []byte{0xD0, 0xCF, 0x11, 0xE0},
			want:     ErrUnsupportedFileType,
		},
		{
			// A binary renamed to .txt would otherwise be indexed as gibberish.
			name:     "binary posing as text",
			filename: "logo.txt",
			data:     []byte{0x89, 'P', 'N', 'G', 0x00, 0x1A},
			want:     ErrUnsupportedFileType,
		},
		{
			name:     "empty file",
			filename: "empty.txt",
			data:     []byte("   \n\n  "),
			want:     ErrNoExtractableText,
		},
		{
			// Truncating would leave the knowledge base holding the first half of
			// a document while claiming the whole of it.
			name:     "text past the per-source bound",
			filename: "huge.txt",
			data:     []byte(strings.Repeat("a", models.KnowledgeBaseMaxSourceTextLength+1)),
			want:     ErrExtractedTextTooLong,
		},
		{
			name:     "unreadable pdf",
			filename: "policy.pdf",
			data:     []byte("not really a pdf"),
			want:     ErrUnsupportedFileType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExtractText(tt.filename, tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

// The legacy .doc message has to say what to do about it, since "unsupported"
// alone reads as a bug to somebody whose file plainly is a Word document.
func TestExtractTextLegacyDocExplainsItself(t *testing.T) {
	_, err := ExtractText("policy.doc", []byte{0xD0, 0xCF})
	if err == nil || !strings.Contains(err.Error(), ".docx") {
		t.Fatalf("error = %v, want it to point at .docx", err)
	}
}

func TestSupportedFileExtensionsAreSortedAndDotted(t *testing.T) {
	extensions := SupportedFileExtensions()
	if len(extensions) == 0 {
		t.Fatal("no supported extensions reported")
	}
	for i, extension := range extensions {
		if !strings.HasPrefix(extension, ".") {
			t.Errorf("extension %q has no leading dot", extension)
		}
		if i > 0 && extensions[i-1] >= extension {
			t.Errorf("extensions are not sorted: %q before %q", extensions[i-1], extension)
		}
	}
}

// minimalPDF builds a one-page PDF whose content stream draws text. The offsets
// in the cross-reference table are computed from the bytes written, since a
// reader uses them to find the objects.
func minimalPDF(t *testing.T, text string) []byte {
	t.Helper()

	content := "BT /F1 24 Tf 72 700 Td (" + text + ") Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Length " + strconv.Itoa(len(content)) + " >>\nstream\n" + content + "endstream",
	}

	var (
		out     bytes.Buffer
		offsets = make([]int, 0, len(objects))
	)
	out.WriteString("%PDF-1.4\n")
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)

	return out.Bytes()
}

// docxFixture builds an in-memory .docx from its parts.
func docxFixture(t *testing.T, parts map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for name, content := range parts {
		part, err := archive.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return buf.Bytes()
}
