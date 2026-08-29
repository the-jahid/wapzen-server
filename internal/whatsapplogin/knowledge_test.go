package whatsapplogin

import (
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

func TestChatKnowledgeSearchQueryIncludesRecentConversation(t *testing.T) {
	history := []aiMessage{
		{Role: "user", Content: "can you check salary receipt?"},
		{Role: "assistant", Content: "Please share the salary receipt you want me to check."},
	}

	query := chatKnowledgeSearchQuery(history, "check from your kb")
	for _, want := range []string{
		"Current user request: check from your kb",
		"User: can you check salary receipt?",
		"Assistant: Please share the salary receipt",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query %q does not contain %q", query, want)
		}
	}
}

func TestChatKnowledgeSearchQueryWithoutHistoryUsesCurrentMessage(t *testing.T) {
	if got := chatKnowledgeSearchQuery(nil, "  salary receipt policy  "); got != "salary receipt policy" {
		t.Fatalf("query = %q, want the trimmed current message", got)
	}
}

func TestFormatChatKnowledgeKeepsOversizedFirstPassage(t *testing.T) {
	material := formatChatKnowledge([]models.KnowledgeSnippet{{
		Title: "Salary receipts",
		Text:  strings.Repeat("receipt details ", 400),
	}})

	if material == "" {
		t.Fatal("an oversized first passage was dropped")
	}
	if len(material) > chatKnowledgeMaxChars {
		t.Fatalf("material length = %d, want at most %d", len(material), chatKnowledgeMaxChars)
	}
	if !strings.Contains(material, "Salary receipts") {
		t.Fatalf("material lost the source title: %q", material[:80])
	}
}

func TestFormatChatKnowledgeSkipsEmptyPassages(t *testing.T) {
	material := formatChatKnowledge([]models.KnowledgeSnippet{
		{Title: "Empty", Text: "  "},
		{Text: "Useful salary information."},
	})

	if !strings.Contains(material, "Untitled source 2") || !strings.Contains(material, "Useful salary information.") {
		t.Fatalf("material = %q", material)
	}
}
