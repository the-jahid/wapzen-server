package knowledgebases

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/pinecone"
)

// defaultRetrieveTopK is how many passages a search returns when the caller does
// not say. It is per search, not per namespace: an agent attached to several
// knowledge bases still gets one ranked list back.
const defaultRetrieveTopK = 5

// minRelevanceScore is the similarity a passage needs to be worth reading. The
// vector store answers every query with its nearest topK records whether or not
// any of them are about the question, so without a floor a knowledge base on one
// subject returns four confident-looking passages about something else — which
// costs a live call tokens and latency, and invites an answer assembled from
// text that has nothing to do with what was asked.
//
// The value is drawn from the gap the index actually shows: against a product
// datasheet, questions it answers score 0.17–0.46, while questions it knows
// nothing about score 0.03–0.07. Anything in between is genuinely marginal, so
// the floor sits below the real hits rather than between them.
const minRelevanceScore = 0.15

// VectorSearcher reads a namespace's vectors back. Implemented by
// internal/pinecone.Client, and kept separate from VectorStore so the write path
// and the read path can be faked independently in tests.
type VectorSearcher interface {
	Query(ctx context.Context, namespace string, vector []float32, topK int) ([]pinecone.Match, error)
	Enabled() bool
}

// Retriever answers a question against a set of knowledge base namespaces: it
// embeds the question with the same model the sources were indexed with, then
// queries each namespace and merges the hits into one ranked list.
//
// It is the read half of Indexer, and the two must agree on the embedding model
// — a query embedded by a different model than the chunks were is not comparable
// to them. Sharing one Embedder between them is what guarantees that.
type Retriever struct {
	embedder Embedder
	vectors  VectorSearcher
}

// NewRetriever wires a retriever to the same upstream clients the indexer uses.
func NewRetriever(embedder Embedder, vectors VectorSearcher) *Retriever {
	return &Retriever{embedder: embedder, vectors: vectors}
}

// Enabled reports whether both upstreams are configured. A disabled retriever
// retrieves nothing rather than failing, so a server without vector-store
// credentials still answers calls — just from the agent's prompt alone.
func (r *Retriever) Enabled() bool {
	return r != nil && r.embedder != nil && r.vectors != nil &&
		r.embedder.Enabled() && r.vectors.Enabled()
}

// Search returns the passages across namespaces that best match query, most
// relevant first, at most topK of them. Passages below minRelevanceScore are
// dropped, so a question the knowledge base has no answer to comes back empty
// rather than as the nearest unrelated text.
//
// Namespaces are queried concurrently and their hits merged by score, so an
// agent attached to several knowledge bases gets the best passages overall
// rather than a fixed quota from each. One namespace failing does not fail the
// search: the rest of the knowledge the caller asked about is still worth
// answering from. Only a failure that leaves nothing at all — embedding the
// query, or every namespace erroring — is returned as an error.
func (r *Retriever) Search(ctx context.Context, namespaces []string, query string, topK int) ([]models.KnowledgeSnippet, error) {
	if !r.Enabled() {
		return nil, ErrIndexingNotConfigured
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	namespaces = normalizeNamespaces(namespaces)
	if len(namespaces) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = defaultRetrieveTopK
	}

	vectors, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("embed query: no vector returned")
	}
	embedded := vectors[0]

	var (
		mu       sync.Mutex
		snippets []models.KnowledgeSnippet
		failures []error
		wg       sync.WaitGroup
	)
	for _, namespace := range namespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			// Each namespace is asked for the full topK because the merge below
			// decides which of them are worth keeping; the best answer may well sit
			// entirely in one knowledge base.
			matches, err := r.vectors.Query(ctx, namespace, embedded, topK)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, fmt.Errorf("query namespace %s: %w", namespace, err))
				return
			}
			for _, match := range matches {
				if match.Score < minRelevanceScore {
					continue
				}
				if snippet, ok := snippetFromMatch(match); ok {
					snippets = append(snippets, snippet)
				}
			}
		}(namespace)
	}
	wg.Wait()

	if len(failures) == len(namespaces) {
		return nil, failures[0]
	}

	sort.SliceStable(snippets, func(i, j int) bool { return snippets[i].Score > snippets[j].Score })
	if len(snippets) > topK {
		snippets = snippets[:topK]
	}
	return snippets, nil
}

// normalizeNamespaces trims the supplied namespaces and drops blanks and
// duplicates, so a knowledge base attached twice is queried once.
func normalizeNamespaces(namespaces []string) []string {
	out := make([]string, 0, len(namespaces))
	seen := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		trimmed := strings.TrimSpace(namespace)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// snippetFromMatch reads back the metadata the indexer wrote with each chunk.
// A match whose text is missing is dropped rather than returned empty: it would
// occupy a slot in the ranked list without telling the model anything.
func snippetFromMatch(match pinecone.Match) (models.KnowledgeSnippet, bool) {
	text := strings.TrimSpace(metadataString(match.Metadata, "text"))
	if text == "" {
		return models.KnowledgeSnippet{}, false
	}
	title := strings.TrimSpace(metadataString(match.Metadata, "title"))
	if title == "" {
		// Chunk ids are the source title (plus a chunk number), so the id names the
		// source when the metadata does not.
		title = strings.TrimSpace(strings.SplitN(match.ID, ChunkIDSeparator, 2)[0])
	}
	return models.KnowledgeSnippet{
		KnowledgeBaseID: metadataString(match.Metadata, "knowledge_base_id"),
		Title:           title,
		Text:            text,
		Score:           match.Score,
	}, true
}

func metadataString(metadata map[string]any, key string) string {
	if value, ok := metadata[key].(string); ok {
		return value
	}
	return ""
}
