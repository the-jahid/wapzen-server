package knowledgebases

import (
	"strings"
	"unicode"
)

// chunkText splits a source's text into the pieces that get embedded, honouring
// the knowledge base's own max_chunk_size and min_chunk_size.
//
// Sizes are in characters, matching how the columns are documented and how the
// API describes them, so everything here counts runes rather than bytes.
//
// The split prefers the largest boundary that fits: paragraphs stay whole where
// they can, then sentences, then words, and only text with no boundary at all
// (a single very long word) is cut mid-word. min is applied afterwards by
// merging a short chunk into its neighbour, since a chunk too small to carry
// context retrieves badly. The last chunk may still fall below min when the
// whole source does.
func chunkText(text string, minChunkSize, maxChunkSize int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChunkSize <= 0 {
		return []string{text}
	}
	if minChunkSize > maxChunkSize {
		minChunkSize = maxChunkSize
	}

	var chunks []string
	current := ""

	// flush closes the chunk being built.
	flush := func() {
		if trimmed := strings.TrimSpace(current); trimmed != "" {
			chunks = append(chunks, trimmed)
		}
		current = ""
	}

	// add appends one indivisible piece, starting a new chunk when it no longer
	// fits in the current one.
	add := func(piece, separator string) {
		if current == "" {
			current = piece
			return
		}
		if runeLen(current)+runeLen(separator)+runeLen(piece) <= maxChunkSize {
			current += separator + piece
			return
		}
		flush()
		current = piece
	}

	for _, paragraph := range splitParagraphs(text) {
		if runeLen(paragraph) <= maxChunkSize {
			add(paragraph, "\n\n")
			continue
		}
		for _, sentence := range splitSentences(paragraph) {
			if runeLen(sentence) <= maxChunkSize {
				add(sentence, " ")
				continue
			}
			for _, piece := range splitLongText(sentence, maxChunkSize) {
				add(piece, " ")
			}
		}
	}
	flush()

	return mergeShortChunks(chunks, minChunkSize, maxChunkSize)
}

// splitParagraphs breaks text on blank lines, which is the strongest boundary a
// plain-text source carries.
func splitParagraphs(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, part := range strings.Split(normalized, "\n\n") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// splitSentences breaks a paragraph after . ! ? or a newline. It is deliberately
// naive: a wrong split costs one slightly odd chunk boundary, so abbreviations
// and decimals are not worth a parser here.
func splitSentences(paragraph string) []string {
	var (
		out      []string
		sentence strings.Builder
	)
	runes := []rune(paragraph)
	for i, r := range runes {
		sentence.WriteRune(r)

		terminator := r == '.' || r == '!' || r == '?' || r == '\n'
		if !terminator {
			continue
		}
		// Only break when whitespace (or the end of the text) follows, so "3.5"
		// and "e.g." stay in one piece.
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}
		if trimmed := strings.TrimSpace(sentence.String()); trimmed != "" {
			out = append(out, trimmed)
		}
		sentence.Reset()
	}
	if trimmed := strings.TrimSpace(sentence.String()); trimmed != "" {
		out = append(out, trimmed)
	}
	return out
}

// splitLongText cuts text that has no usable sentence boundary into max-sized
// pieces, breaking between words where possible.
func splitLongText(text string, maxChunkSize int) []string {
	var (
		out     []string
		current string
	)
	for _, word := range strings.Fields(text) {
		switch {
		case current == "":
			current = word
		case runeLen(current)+1+runeLen(word) <= maxChunkSize:
			current += " " + word
		default:
			out = append(out, current)
			current = word
		}

		// A single word longer than the limit is cut mid-word; there is no
		// boundary left to prefer.
		for runeLen(current) > maxChunkSize {
			runes := []rune(current)
			out = append(out, string(runes[:maxChunkSize]))
			current = string(runes[maxChunkSize:])
		}
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

// mergeShortChunks folds any chunk below minChunkSize into its neighbour where
// the result still fits under maxChunkSize. A chunk with no neighbour it fits
// with is kept as it is: dropping it would lose the text, and splitting it
// further would only make it smaller.
func mergeShortChunks(chunks []string, minChunkSize, maxChunkSize int) []string {
	if len(chunks) < 2 || minChunkSize <= 0 {
		return chunks
	}

	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if len(out) == 0 {
			out = append(out, chunk)
			continue
		}
		previous := out[len(out)-1]
		tooShort := runeLen(previous) < minChunkSize || runeLen(chunk) < minChunkSize
		if tooShort && runeLen(previous)+1+runeLen(chunk) <= maxChunkSize {
			out[len(out)-1] = previous + "\n" + chunk
			continue
		}
		out = append(out, chunk)
	}
	return out
}

func runeLen(s string) int {
	return len([]rune(s))
}
