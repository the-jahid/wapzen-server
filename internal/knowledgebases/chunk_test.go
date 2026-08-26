package knowledgebases

import (
	"strings"
	"testing"
)

// Every chunk must stay within max, since the chunk size is the contract the
// caller configured and an oversized chunk is what the embedding request would
// choke on.
func TestChunkTextRespectsMaxChunkSize(t *testing.T) {
	text := strings.Repeat("Refunds are processed within fourteen days of the request. ", 200)

	chunks := chunkText(text, 400, 2000)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want the text split into several", len(chunks))
	}
	for i, chunk := range chunks {
		if runeLen(chunk) > 2000 {
			t.Errorf("chunk %d is %d characters, want at most 2000", i, runeLen(chunk))
		}
	}
}

// A source small enough to fit is one chunk: splitting it would only scatter
// the context an agent retrieves.
func TestChunkTextKeepsShortTextWhole(t *testing.T) {
	chunks := chunkText("  Refunds are processed within 14 days.  ", 400, 2000)

	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1: %q", len(chunks), chunks)
	}
	if chunks[0] != "Refunds are processed within 14 days." {
		t.Errorf("chunk = %q, want the trimmed text", chunks[0])
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if chunks := chunkText("   \n\n  ", 400, 2000); chunks != nil {
		t.Errorf("chunks = %q, want nil for blank text", chunks)
	}
}

// Paragraphs are the strongest boundary a text source carries, so they are kept
// whole while they fit.
func TestChunkTextSplitsOnParagraphs(t *testing.T) {
	first := strings.Repeat("a", 300)
	second := strings.Repeat("b", 300)

	chunks := chunkText(first+"\n\n"+second, 0, 400)

	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if chunks[0] != first || chunks[1] != second {
		t.Errorf("paragraphs were not kept whole: %q", chunks)
	}
}

// A chunk below min is merged into its neighbour where the result still fits,
// so a trailing fragment does not become a chunk of its own.
func TestChunkTextMergesShortChunks(t *testing.T) {
	body := strings.Repeat("word ", 100) // ~500 characters
	chunks := chunkText(body+"\n\nok", 200, 900)

	for i, chunk := range chunks {
		if runeLen(chunk) > 900 {
			t.Fatalf("chunk %d is %d characters, want at most 900", i, runeLen(chunk))
		}
	}
	for _, chunk := range chunks {
		if chunk == "ok" {
			t.Errorf("short trailing chunk was not merged: %q", chunks)
		}
	}
}

// A single word longer than the limit has no boundary left to prefer, so it is
// cut rather than emitted oversized.
func TestChunkTextSplitsUnbrokenText(t *testing.T) {
	chunks := chunkText(strings.Repeat("x", 2500), 0, 1000)

	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	total := 0
	for i, chunk := range chunks {
		if runeLen(chunk) > 1000 {
			t.Errorf("chunk %d is %d characters, want at most 1000", i, runeLen(chunk))
		}
		total += runeLen(chunk)
	}
	if total != 2500 {
		t.Errorf("total characters = %d, want 2500 — the split lost text", total)
	}
}

// Sizes are counted in characters, matching how the columns are documented, so
// multi-byte text must not be cut by byte length.
func TestChunkTextCountsCharactersNotBytes(t *testing.T) {
	// Each rune here is 3 bytes, so a byte-based limit would split at 200 runes.
	chunks := chunkText(strings.Repeat("日", 500), 0, 600)

	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1: a 500-character text fits a 600-character chunk", len(chunks))
	}
}

// Sentences end at punctuation followed by whitespace, so decimals and
// abbreviations do not create boundaries mid-number.
func TestChunkTextDoesNotSplitDecimals(t *testing.T) {
	sentence := "The fee is 3.5 percent. " + strings.Repeat("padding ", 40)

	for _, chunk := range chunkText(sentence, 0, 100) {
		if strings.HasSuffix(chunk, "3.") {
			t.Errorf("chunk split inside a decimal: %q", chunk)
		}
	}
}
