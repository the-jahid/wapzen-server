package knowledgebases

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"whatsapp-ai-caller-server/internal/pinecone"
)

// fakeSearcher answers queries per namespace, so a test can hand each knowledge
// base its own hits and check how they are merged.
type fakeSearcher struct {
	mu sync.Mutex
	// matches maps a namespace to what querying it returns.
	matches map[string][]pinecone.Match
	// errs maps a namespace to a failure, taking precedence over matches.
	errs map[string]error
	// queried records every namespace asked, and topK the limit it was asked with.
	queried []string
	topK    int
	// vector records the embedding the namespaces were searched with.
	vector  []float32
	enabled bool
}

func (f *fakeSearcher) Query(_ context.Context, namespace string, vector []float32, topK int) ([]pinecone.Match, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried = append(f.queried, namespace)
	f.topK = topK
	f.vector = vector
	if err := f.errs[namespace]; err != nil {
		return nil, err
	}
	return f.matches[namespace], nil
}

func (f *fakeSearcher) Enabled() bool { return f.enabled }

func match(id string, score float32, metadata map[string]any) pinecone.Match {
	return pinecone.Match{ID: id, Score: score, Metadata: metadata}
}

func chunkMetadata(title, text string) map[string]any {
	return map[string]any{"knowledge_base_id": "kb_1", "title": title, "text": text}
}

func TestRetrieverSearchMergesNamespacesByScore(t *testing.T) {
	searcher := &fakeSearcher{
		enabled: true,
		matches: map[string][]pinecone.Match{
			"kb_refunds": {
				match("Refund policy", 0.91, chunkMetadata("Refund policy", "Refunds take five days.")),
				match("Refund policy#2", 0.40, chunkMetadata("Refund policy", "Shipping is not refunded.")),
			},
			"kb_hours": {
				match("Opening hours", 0.77, chunkMetadata("Opening hours", "Open nine to five.")),
			},
		},
	}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	snippets, err := retriever.Search(context.Background(), []string{"kb_refunds", "kb_hours"}, "when do refunds arrive?", 2)
	if err != nil {
		t.Fatal(err)
	}

	// Both namespaces are searched, and the two best passages win regardless of
	// which knowledge base they came from.
	if len(snippets) != 2 {
		t.Fatalf("snippets = %#v", snippets)
	}
	if snippets[0].Text != "Refunds take five days." || snippets[1].Text != "Open nine to five." {
		t.Fatalf("snippets are not ranked by score: %#v", snippets)
	}
	if snippets[0].Title != "Refund policy" || snippets[0].KnowledgeBaseID != "kb_1" {
		t.Fatalf("snippet lost its source metadata: %#v", snippets[0])
	}
	if len(searcher.queried) != 2 {
		t.Fatalf("queried = %#v, want both namespaces", searcher.queried)
	}
	// Each namespace is asked for the full topK; the merge decides what survives.
	if searcher.topK != 2 {
		t.Fatalf("topK = %d, want 2", searcher.topK)
	}
	if len(searcher.vector) == 0 {
		t.Fatal("namespaces were searched without an embedded query")
	}
}

func TestRetrieverSearchDeduplicatesNamespaces(t *testing.T) {
	searcher := &fakeSearcher{enabled: true, matches: map[string][]pinecone.Match{
		"kb_1": {match("Policy", 0.5, chunkMetadata("Policy", "Text."))},
	}}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	if _, err := retriever.Search(context.Background(), []string{"kb_1", " kb_1 ", "", "  "}, "question", 3); err != nil {
		t.Fatal(err)
	}
	if len(searcher.queried) != 1 {
		t.Fatalf("queried = %#v, want one query for the same namespace", searcher.queried)
	}
}

func TestRetrieverSearchSurvivesOneFailingNamespace(t *testing.T) {
	searcher := &fakeSearcher{
		enabled: true,
		errs:    map[string]error{"kb_broken": errors.New("index unreachable")},
		matches: map[string][]pinecone.Match{
			"kb_ok": {match("Policy", 0.6, chunkMetadata("Policy", "Still answerable."))},
		},
	}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	snippets, err := retriever.Search(context.Background(), []string{"kb_broken", "kb_ok"}, "question", 3)
	if err != nil {
		t.Fatalf("one bad namespace failed the whole search: %v", err)
	}
	if len(snippets) != 1 || snippets[0].Text != "Still answerable." {
		t.Fatalf("snippets = %#v", snippets)
	}
}

func TestRetrieverSearchFailsWhenEveryNamespaceFails(t *testing.T) {
	searcher := &fakeSearcher{
		enabled: true,
		errs: map[string]error{
			"kb_a": errors.New("index unreachable"),
			"kb_b": errors.New("index unreachable"),
		},
	}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	if _, err := retriever.Search(context.Background(), []string{"kb_a", "kb_b"}, "question", 3); err == nil {
		t.Fatal("a search that retrieved nothing at all reported success")
	}
}

func TestRetrieverSearchDropsIrrelevantMatches(t *testing.T) {
	// The vector store answers with its nearest records whether or not any of
	// them are about the question, so a knowledge base on another subject must
	// come back empty rather than as four confident-looking wrong passages.
	searcher := &fakeSearcher{enabled: true, matches: map[string][]pinecone.Match{
		"kb_products": {
			match("Datasheet", 0.07, chunkMetadata("Datasheet", "Nutritional values per 100g.")),
			match("Datasheet#2", 0.03, chunkMetadata("Datasheet", "Store away from light.")),
		},
	}}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	snippets, err := retriever.Search(context.Background(), []string{"kb_products"}, "do you know Daniel Carter?", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(snippets) != 0 {
		t.Fatalf("snippets = %#v, want nothing above the relevance floor", snippets)
	}
}

func TestRetrieverSearchKeepsMatchesAtTheFloor(t *testing.T) {
	searcher := &fakeSearcher{enabled: true, matches: map[string][]pinecone.Match{
		"kb_products": {
			match("Datasheet", 0.46, chunkMetadata("Datasheet", "Shelf life is 18 months.")),
			match("Datasheet#2", minRelevanceScore, chunkMetadata("Datasheet", "Store away from light.")),
			match("Datasheet#3", minRelevanceScore-0.01, chunkMetadata("Datasheet", "Unrelated packaging table.")),
		},
	}}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	snippets, err := retriever.Search(context.Background(), []string{"kb_products"}, "how long does it keep?", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(snippets) != 2 {
		t.Fatalf("snippets = %#v, want the two at or above the floor", snippets)
	}
	if snippets[0].Text != "Shelf life is 18 months." {
		t.Fatalf("snippets are not ranked by score: %#v", snippets)
	}
}

func TestRetrieverSearchSkipsMatchesWithoutText(t *testing.T) {
	searcher := &fakeSearcher{enabled: true, matches: map[string][]pinecone.Match{
		"kb_1": {
			match("Ghost", 0.99, map[string]any{"title": "Ghost"}),
			match("Real#3", 0.60, map[string]any{"text": "Body without a title."}),
		},
	}}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	snippets, err := retriever.Search(context.Background(), []string{"kb_1"}, "question", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(snippets) != 1 {
		t.Fatalf("a match carrying no text was kept: %#v", snippets)
	}
	// The chunk id names the source when the metadata does not.
	if snippets[0].Title != "Real" {
		t.Fatalf("title = %q, want it recovered from the chunk id", snippets[0].Title)
	}
}

func TestRetrieverSearchIsInertWithoutCredentials(t *testing.T) {
	retriever := NewRetriever(&fakeEmbedder{}, &fakeSearcher{enabled: false})
	if retriever.Enabled() {
		t.Fatal("a retriever with an unconfigured vector store reported enabled")
	}
	_, err := retriever.Search(context.Background(), []string{"kb_1"}, "question", 3)
	if !errors.Is(err, ErrIndexingNotConfigured) {
		t.Fatalf("err = %v, want ErrIndexingNotConfigured", err)
	}
}

func TestRetrieverSearchIgnoresEmptyRequests(t *testing.T) {
	searcher := &fakeSearcher{enabled: true}
	retriever := NewRetriever(&fakeEmbedder{}, searcher)

	for _, tc := range []struct {
		name       string
		namespaces []string
		query      string
	}{
		{name: "blank query", namespaces: []string{"kb_1"}, query: "   "},
		{name: "no namespaces", namespaces: nil, query: "question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snippets, err := retriever.Search(context.Background(), tc.namespaces, tc.query, 3)
			if err != nil || len(snippets) != 0 {
				t.Fatalf("snippets = %#v err = %v", snippets, err)
			}
			if len(searcher.queried) != 0 {
				t.Fatalf("the vector store was queried anyway: %#v", searcher.queried)
			}
		})
	}
}

func TestRetrieverSearchReportsEmbeddingFailure(t *testing.T) {
	retriever := NewRetriever(&fakeEmbedder{err: errors.New("rate limited")}, &fakeSearcher{enabled: true})

	_, err := retriever.Search(context.Background(), []string{"kb_1"}, "question", 3)
	if err == nil || !strings.Contains(err.Error(), "embed query") {
		t.Fatalf("err = %v, want the embedding failure surfaced", err)
	}
}
