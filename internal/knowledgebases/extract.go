package knowledgebases

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dslipak/pdf"

	"whatsapp-ai-caller-server/internal/models"
)

// Extracting an uploaded file happens here rather than in the browser: the text
// that gets chunked, embedded and upserted is the text the server read itself,
// so what a knowledge base holds never depends on what a client chose to send.
// A client only ever hands over the bytes.

// ErrUnsupportedFileType is returned for a file whose extension has no
// extractor. It is a caller error, not a failure, so the handler answers with a
// field error naming the supported types rather than a 500.
var ErrUnsupportedFileType = errors.New("unsupported file type")

// ErrNoExtractableText is returned when a file was read successfully but holds
// no text — an empty document, or the common case of a PDF that is a scan with
// no text layer. Storing it would give the knowledge base a source that indexes
// nothing, so it is refused with an explanation instead.
var ErrNoExtractableText = errors.New("no text could be extracted from this file")

// ErrExtractedTextTooLong is returned when a file's text exceeds the per-source
// bound. Truncating would leave the knowledge base claiming a document it only
// holds the beginning of, so the file is refused and splitting it is the
// caller's call.
var ErrExtractedTextTooLong = errors.New("extracted text is too long")

// extractor reads the plain text out of one file format.
type extractor func(data []byte) (string, error)

// extractorsByExtension maps a lowercase file extension to its extractor. The
// extension is what selects it: the browser's reported content type is not
// trustworthy enough to route on, and several of these formats share one.
var extractorsByExtension = map[string]extractor{
	".txt":      extractPlainText,
	".text":     extractPlainText,
	".md":       extractPlainText,
	".markdown": extractPlainText,
	".rst":      extractPlainText,
	".log":      extractPlainText,
	".csv":      extractPlainText,
	".tsv":      extractPlainText,
	".json":     extractPlainText,
	".yaml":     extractPlainText,
	".yml":      extractPlainText,
	".xml":      extractPlainText,
	".html":     extractHTML,
	".htm":      extractHTML,
	".docx":     extractDOCX,
	".pdf":      extractPDF,
}

// SupportedFileExtensions lists the extensions ExtractText accepts, sorted so
// error messages and the API documentation read the same way every time.
func SupportedFileExtensions() []string {
	out := make([]string, 0, len(extractorsByExtension))
	for extension := range extractorsByExtension {
		out = append(out, extension)
	}
	sort.Strings(out)
	return out
}

// ExtractText returns the plain text of an uploaded file, chosen by the
// filename's extension.
//
// The text comes back normalized — one newline convention, no control
// characters, no runs of blank lines — because it is stored as the source's
// content and re-chunked from there: leaving formatting noise in would spend
// chunk budget on characters that carry no meaning, in every chunk, forever.
func ExtractText(filename string, data []byte) (string, error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))

	extract, ok := extractorsByExtension[extension]
	if !ok {
		// .doc is called out separately: it is not a variant of .docx but an
		// entirely different binary format, and "convert it" is the actionable
		// answer a bare "unsupported" would not give.
		if extension == ".doc" {
			return "", fmt.Errorf("%w: legacy .doc files cannot be read; save the document as .docx or .pdf", ErrUnsupportedFileType)
		}
		if extension == "" {
			return "", fmt.Errorf("%w: the file has no extension, so its format cannot be determined", ErrUnsupportedFileType)
		}
		return "", fmt.Errorf("%w: %s", ErrUnsupportedFileType, extension)
	}

	text, err := extract(data)
	if err != nil {
		return "", err
	}

	text = normalizeExtractedText(text)
	if text == "" {
		return "", ErrNoExtractableText
	}
	if utf8.RuneCountInString(text) > models.KnowledgeBaseMaxSourceTextLength {
		return "", fmt.Errorf("%w: %d characters, at most %d are indexed per source",
			ErrExtractedTextTooLong, utf8.RuneCountInString(text), models.KnowledgeBaseMaxSourceTextLength)
	}
	return text, nil
}

// extractPlainText reads a file that is already text. The bytes still have to be
// decoded rather than cast: an editor may have written UTF-16 with a byte order
// mark, and passing those bytes through as UTF-8 would store a source that is
// half NUL bytes.
func extractPlainText(data []byte) (string, error) {
	text, ok := decodeText(data)
	if !ok {
		return "", fmt.Errorf("%w: the file is not valid UTF-8 text", ErrUnsupportedFileType)
	}
	return text, nil
}

// decodeText turns a text file's bytes into a Go string, honouring a UTF-8 or
// UTF-16 byte order mark. It reports false for anything that is not decodable
// text, which is how a binary file renamed to .txt is caught before it is
// indexed as gibberish.
func decodeText(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return decodeUTF16(data[2:], false), true
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], true), true
	}

	if !utf8.Valid(data) {
		return "", false
	}
	// A NUL byte in valid UTF-8 is the tell of a binary file: no text format
	// this reads produces one.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false
	}
	return string(data), true
}

// decodeUTF16 decodes UTF-16 code units of the given endianness. A trailing odd
// byte is dropped rather than failing the file: it is a truncated code unit at
// the very end, and losing one character beats refusing the document.
func decodeUTF16(data []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
			continue
		}
		units = append(units, uint16(data[i+1])<<8|uint16(data[i]))
	}
	return string(utf16.Decode(units))
}

var (
	// Script and style hold code, not content: their text would be indexed and
	// retrieved as if an agent could quote it. They are two patterns rather than
	// one alternation because Go's regexp has no backreference to tie the closing
	// tag to the opening one.
	htmlScriptPattern  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>`)
	htmlStylePattern   = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</\s*style\s*>`)
	htmlCommentPattern = regexp.MustCompile(`(?s)<!--.*?-->`)
	// Tags that end a line of prose. Turning them into newlines before the tags
	// are stripped is what keeps two paragraphs from being welded into one word.
	htmlBreakPattern = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|section|article|header|footer|li|ul|ol|tr|table|h[1-6]|blockquote|pre)\b[^>]*>`)
	htmlTagPattern   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// extractHTML reduces a page to the text a reader would see: code and comments
// dropped, block boundaries kept as line breaks, tags removed and entities
// decoded.
//
// It is deliberately a stripper rather than a parser. The input here is a file
// somebody uploaded to be quoted from, not a page to render, so the cost of
// getting the structure slightly wrong is a stray line break — while a parser
// dependency would have to be maintained for the same result.
func extractHTML(data []byte) (string, error) {
	text, ok := decodeText(data)
	if !ok {
		return "", fmt.Errorf("%w: the file is not valid UTF-8 text", ErrUnsupportedFileType)
	}

	text = htmlCommentPattern.ReplaceAllString(text, " ")
	text = htmlScriptPattern.ReplaceAllString(text, " ")
	text = htmlStylePattern.ReplaceAllString(text, " ")
	text = htmlBreakPattern.ReplaceAllString(text, "\n")
	text = htmlTagPattern.ReplaceAllString(text, " ")

	return html.UnescapeString(text), nil
}

// extractDOCX reads the text of a Word document. A .docx is a zip archive of
// XML parts, so this needs no dependency: the document body is one entry in it,
// and the runs of text inside are what a reader sees.
func extractDOCX(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: the file is not a readable .docx archive", ErrUnsupportedFileType)
	}

	var document *zip.File
	for _, file := range archive.File {
		if file.Name == "word/document.xml" {
			document = file
			break
		}
	}
	if document == nil {
		return "", fmt.Errorf("%w: the .docx archive has no word/document.xml", ErrUnsupportedFileType)
	}

	body, err := document.Open()
	if err != nil {
		return "", fmt.Errorf("open .docx document part: %w", err)
	}
	defer body.Close()

	return decodeWordML(body)
}

// decodeWordML walks the WordprocessingML body and writes out its text.
//
// Only four elements matter for plain text: w:t holds the characters, w:tab and
// w:br are whitespace the author put there, and the end of a w:p is a paragraph
// break — which is the boundary the chunker splits on first, so getting it right
// here is what lets a Word document chunk along its own paragraphs.
func decodeWordML(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var out strings.Builder

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read .docx document part: %w", err)
		}

		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "t":
				var text string
				if err := decoder.DecodeElement(&text, &element); err != nil {
					return "", fmt.Errorf("read .docx text run: %w", err)
				}
				out.WriteString(text)
			case "tab":
				out.WriteString("\t")
			case "br", "cr":
				out.WriteString("\n")
			}
		case xml.EndElement:
			if element.Name.Local == "p" {
				out.WriteString("\n\n")
			}
		}
	}

	return out.String(), nil
}

// extractPDF reads a PDF's text layer. A PDF that is a scan has none, so this
// legitimately returns nothing for a file that looks full of words to whoever
// uploaded it; ExtractText turns that into ErrNoExtractableText, which says so.
func extractPDF(data []byte) (text string, err error) {
	// The reader panics on some malformed and encrypted files instead of
	// returning an error. A panic here would be recovered per request, but it
	// would surface as a 500 on a file the caller can be told about, so it is
	// turned into an error where the format is known.
	defer func() {
		if recovered := recover(); recovered != nil {
			text = ""
			err = fmt.Errorf("%w: the PDF could not be read (%v)", ErrUnsupportedFileType, recovered)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: the PDF could not be read (%v)", ErrUnsupportedFileType, err)
	}

	plain, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("%w: the PDF could not be read (%v)", ErrUnsupportedFileType, err)
	}

	var out strings.Builder
	if _, err := io.Copy(&out, plain); err != nil {
		return "", fmt.Errorf("read PDF text: %w", err)
	}
	return out.String(), nil
}

// blankLinesPattern matches a run of blank lines, which collapses to one.
var blankLinesPattern = regexp.MustCompile(`\n{3,}`)

// normalizeExtractedText puts extracted text into the shape the chunker expects:
// \n line endings, no control or zero-width characters, no trailing spaces and
// at most one blank line between paragraphs.
//
// The blank-line rule is not cosmetic — the chunker treats a blank line as a
// paragraph boundary, and a PDF or Word export that separates every line with
// several of them would otherwise be split into one chunk per line.
func normalizeExtractedText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var cleaned strings.Builder
	cleaned.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			cleaned.WriteRune(r)
		case r == '\ufeff' || r == '\u200b' || r == '\u200c' || r == '\u200d':
			// Zero-width characters: invisible to a reader, but they would sit
			// inside indexed words and break a match on them.
		case r == '\u00a0' || r == '\u2007' || r == '\u202f':
			// A non-breaking space is a space once the layout is gone. PDFs are
			// full of them, and leaving one in makes a search for "14 days" miss
			// the line it is on.
			cleaned.WriteRune(' ')
		case r == utf8.RuneError || unicode.IsControl(r):
			// Undecodable bytes and control codes carry nothing to retrieve.
		default:
			cleaned.WriteRune(r)
		}
	}

	lines := strings.Split(cleaned.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}

	return strings.TrimSpace(blankLinesPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
