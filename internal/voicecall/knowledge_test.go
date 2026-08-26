package voicecall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"whatsapp-ai-caller-server/internal/models"
)

// fakeRetriever records what a call asked for and answers with canned passages.
type fakeRetriever struct {
	namespaces []string
	query      string
	topK       int
	snippets   []models.KnowledgeSnippet
	err        error
	enabled    bool
}

func (f *fakeRetriever) Search(_ context.Context, namespaces []string, query string, topK int) ([]models.KnowledgeSnippet, error) {
	f.namespaces = namespaces
	f.query = query
	f.topK = topK
	return f.snippets, f.err
}

func (f *fakeRetriever) Enabled() bool { return f.enabled }

func testKnowledgeBases() []models.AgentKnowledgeBase {
	return []models.AgentKnowledgeBase{
		{ID: "kb_1", Name: "Refund policy", Namespace: "ns_refunds"},
		{ID: "kb_2", Name: "Opening hours", Namespace: "ns_hours"},
	}
}

func TestNewKnowledgeToolboxCollectsEveryNamespace(t *testing.T) {
	box := newKnowledgeToolbox(&fakeRetriever{enabled: true}, testKnowledgeBases(), "call-1")
	if box == nil {
		t.Fatal("an agent with knowledge bases got no toolbox")
	}
	if strings.Join(box.namespaces, ",") != "ns_refunds,ns_hours" {
		t.Fatalf("namespaces = %#v, want both in attachment order", box.namespaces)
	}

	definitions := box.Definitions()
	if len(definitions) != 1 || definitions[0].Name != knowledgeToolName {
		t.Fatalf("definitions = %#v", definitions)
	}
	// The model has to be able to tell whether a question is one this agent can
	// look up at all, so the knowledge bases are named to it.
	if !strings.Contains(definitions[0].Description, "Refund policy") || !strings.Contains(definitions[0].Description, "Opening hours") {
		t.Fatalf("description does not name the knowledge bases: %q", definitions[0].Description)
	}
	if !strings.Contains(box.Instructions(), knowledgeToolName) {
		t.Fatalf("instructions do not name the tool: %q", box.Instructions())
	}
}

func TestNewKnowledgeToolboxIsNilWhenThereIsNothingToSearch(t *testing.T) {
	tests := []struct {
		name      string
		retriever KnowledgeRetriever
		bases     []models.AgentKnowledgeBase
	}{
		{name: "no retriever", retriever: nil, bases: testKnowledgeBases()},
		{name: "retrieval not configured", retriever: &fakeRetriever{enabled: false}, bases: testKnowledgeBases()},
		{name: "no knowledge bases", retriever: &fakeRetriever{enabled: true}, bases: nil},
		{
			name:      "knowledge bases without namespaces",
			retriever: &fakeRetriever{enabled: true},
			bases:     []models.AgentKnowledgeBase{{ID: "kb_1", Name: "Unindexed", Namespace: "  "}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if box := newKnowledgeToolbox(tc.retriever, tc.bases, "call-1"); box != nil {
				t.Fatalf("box = %#v, want nil so no tool is offered", box)
			}
		})
	}
}

func TestKnowledgeToolboxRunSearchesEveryNamespace(t *testing.T) {
	retriever := &fakeRetriever{
		enabled: true,
		snippets: []models.KnowledgeSnippet{
			{Title: "Refund policy", Text: "Refunds take five days.", Score: 0.9},
			{Title: "", Text: "Open nine to five.", Score: 0.5},
		},
	}
	box := newKnowledgeToolbox(retriever, testKnowledgeBases(), "call-1")

	result := box.Run(context.Background(), knowledgeToolName, `{"query":"how long do refunds take?"}`)

	if retriever.query != "how long do refunds take?" {
		t.Fatalf("query = %q", retriever.query)
	}
	if strings.Join(retriever.namespaces, ",") != "ns_refunds,ns_hours" {
		t.Fatalf("namespaces = %#v, want every attached knowledge base searched", retriever.namespaces)
	}
	if retriever.topK != knowledgeToolTopK {
		t.Fatalf("topK = %d, want %d", retriever.topK, knowledgeToolTopK)
	}
	if !strings.Contains(result, "Refunds take five days.") || !strings.Contains(result, "Open nine to five.") {
		t.Fatalf("result dropped a passage: %q", result)
	}
	if !strings.Contains(result, "Refund policy") {
		t.Fatalf("result does not attribute the passage to its source: %q", result)
	}
}

func TestKnowledgeToolboxRunReportsEmptyAndFailedSearches(t *testing.T) {
	tests := []struct {
		name      string
		retriever *fakeRetriever
		arguments string
		want      string
	}{
		{
			name:      "nothing found",
			retriever: &fakeRetriever{enabled: true},
			arguments: `{"query":"anything"}`,
			want:      "No relevant information",
		},
		{
			name:      "search failed",
			retriever: &fakeRetriever{enabled: true, err: errors.New("index unreachable")},
			arguments: `{"query":"anything"}`,
			want:      "could not be reached",
		},
		{
			name:      "malformed arguments",
			retriever: &fakeRetriever{enabled: true},
			arguments: `{"query":`,
			want:      "could not be read",
		},
		{
			name:      "blank query",
			retriever: &fakeRetriever{enabled: true},
			arguments: `{"query":"   "}`,
			want:      "No search query",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			box := newKnowledgeToolbox(tc.retriever, testKnowledgeBases(), "call-1")
			// A tool never fails the turn: the model is told what happened so the
			// agent can say so instead of going silent on the caller.
			if result := box.Run(context.Background(), knowledgeToolName, tc.arguments); !strings.Contains(result, tc.want) {
				t.Fatalf("result = %q, want it to mention %q", result, tc.want)
			}
		})
	}
}

func TestKnowledgeToolboxRunRejectsUnknownTools(t *testing.T) {
	box := newKnowledgeToolbox(&fakeRetriever{enabled: true}, testKnowledgeBases(), "call-1")
	if result := box.Run(context.Background(), "delete_everything", `{}`); !strings.Contains(result, "Unknown tool") {
		t.Fatalf("result = %q", result)
	}
}

func TestFormatKnowledgeSnippetsIsBounded(t *testing.T) {
	long := strings.Repeat("a", knowledgeToolMaxChars)
	result := formatKnowledgeSnippets([]models.KnowledgeSnippet{
		{Title: "First", Text: long},
		{Title: "Second", Text: "This one does not fit."},
	})
	if len(result) > knowledgeToolMaxChars {
		t.Fatalf("result is %d chars, over the %d budget", len(result), knowledgeToolMaxChars)
	}
	if strings.Contains(result, "This one does not fit.") {
		t.Fatal("a passage past the budget was included")
	}
}
