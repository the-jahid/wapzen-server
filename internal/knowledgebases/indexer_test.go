package knowledgebases

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/pinecone"
)

// fakeQuerier stands in for the pool. Only Exec is reached from the indexer,
// which records chunk counts; the read paths are not part of indexing.
type fakeQuerier struct {
	execs [][]any
	err   error
}

func (f *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("Query is not used by the indexer")
}

func (f *fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (f *fakeQuerier) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, args)
	return pgconn.NewCommandTag("UPDATE 1"), f.err
}

type fakeEmbedder struct {
	inputs []string
	err    error
}

func (f *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.inputs = append(f.inputs, inputs...)
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = []float32{float32(i), 1, 2}
	}
	return out, nil
}

func (f *fakeEmbedder) Model() string { return "fake-embedding-model" }
func (f *fakeEmbedder) Enabled() bool { return true }

type fakeVectorStore struct {
	upserted []pinecone.Vector
	deleted  []string
	// namespace and deletedFrom record which namespace each kind of write went
	// to, kept apart so a test can tell an upsert's namespace from a delete's.
	namespace   string
	deletedFrom string
	upsertErr   error
	deleteErr   error
}

func (f *fakeVectorStore) Upsert(_ context.Context, namespace string, vectors []pinecone.Vector) error {
	f.namespace = namespace
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserted = append(f.upserted, vectors...)
	return nil
}

func (f *fakeVectorStore) DeleteByIDs(_ context.Context, namespace string, ids []string) error {
	f.deletedFrom = namespace
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, ids...)
	return nil
}

func (f *fakeVectorStore) DeleteNamespace(context.Context, string) error { return nil }
func (f *fakeVectorStore) Enabled() bool                                 { return true }

func testKnowledgeBase() models.KnowledgeBase {
	namespace := "kb_test"
	return models.KnowledgeBase{
		ID:           "kb-1",
		NamespaceID:  &namespace,
		MaxChunkSize: 600,
		MinChunkSize: 200,
	}
}

func textSource(id, title, content string) models.KnowledgeBaseSourceWithContent {
	return models.KnowledgeBaseSourceWithContent{
		KnowledgeBaseSource: models.KnowledgeBaseSource{
			SourceID: id,
			Type:     models.KnowledgeBaseSourceTypeText,
			Title:    title,
		},
		Content: content,
	}
}

func TestIndexSourcesWritesVectorsAndChunkCounts(t *testing.T) {
	db := &fakeQuerier{}
	embedder := &fakeEmbedder{}
	vectors := &fakeVectorStore{}
	indexer := NewIndexer(&Repository{db: db}, embedder, vectors)

	// Long enough to produce more than one chunk at a 600-character maximum.
	content := strings.Repeat("Refunds are processed within fourteen days. ", 60)
	err := indexer.IndexSources(context.Background(), testKnowledgeBase(), []models.KnowledgeBaseSourceWithContent{
		textSource("source-1", "Refund policy", content),
	})
	if err != nil {
		t.Fatalf("IndexSources: %v", err)
	}

	if len(vectors.upserted) < 2 {
		t.Fatalf("upserted %d vectors, want the source split into several", len(vectors.upserted))
	}
	if vectors.namespace != "kb_test" {
		t.Errorf("namespace = %q, want the knowledge base's own", vectors.namespace)
	}
	if len(embedder.inputs) != len(vectors.upserted) {
		t.Errorf("embedded %d chunks but upserted %d vectors", len(embedder.inputs), len(vectors.upserted))
	}

	// The first chunk is stored under the title verbatim, so knowledge base
	// content is listed in the vector store under the name it was given.
	if vectors.upserted[0].ID != "Refund policy" {
		t.Errorf("first vector id = %q, want the title verbatim", vectors.upserted[0].ID)
	}
	for i, vector := range vectors.upserted {
		if !strings.HasPrefix(vector.ID, "Refund policy") {
			t.Errorf("vector %d id = %q, want it to start with the title", i, vector.ID)
		}
		if vector.Metadata["source_id"] != "source-1" || vector.Metadata["knowledge_base_id"] != "kb-1" {
			t.Errorf("vector %d metadata = %v, want the source and knowledge base ids", i, vector.Metadata)
		}
		if vector.Metadata["text"] == "" {
			t.Errorf("vector %d carries no chunk text", i)
		}
	}

	if len(db.execs) != 1 {
		t.Fatalf("chunk-count updates = %d, want 1 per source", len(db.execs))
	}
	if got, want := db.execs[0][1], len(vectors.upserted); got != want {
		t.Errorf("recorded chunk count = %v, want %d", got, want)
	}
}

// A failure part-way through must not leave the knowledge base holding half a
// source, so everything already written is deleted again.
func TestIndexSourcesRollsBackOnFailure(t *testing.T) {
	vectors := &fakeVectorStore{}
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, &fakeEmbedder{}, vectors)

	sources := []models.KnowledgeBaseSourceWithContent{
		textSource("source-1", "First", "Refunds are processed within fourteen days."),
		textSource("source-2", "Second", "Shipping takes three days."),
	}

	// The first source indexes; the second fails at the vector store.
	if err := indexer.IndexSources(context.Background(), testKnowledgeBase(), sources[:1]); err != nil {
		t.Fatalf("indexing the first source: %v", err)
	}
	written := len(vectors.upserted)
	vectors.upsertErr = errors.New("upstream is down")

	err := indexer.IndexSources(context.Background(), testKnowledgeBase(), sources[1:])
	if err == nil {
		t.Fatal("IndexSources succeeded, want the upstream failure")
	}
	if len(vectors.deleted) == 0 {
		t.Fatal("no vectors were deleted, want the failed call rolled back")
	}
	for _, id := range vectors.deleted {
		if !strings.HasPrefix(id, "Second") {
			t.Errorf("rollback deleted %q, want only the failed call's vectors", id)
		}
	}
	if len(vectors.upserted) != written {
		t.Errorf("upserted count changed to %d, want the earlier source untouched at %d", len(vectors.upserted), written)
	}
}

// Indexing without credentials must report itself as unconfigured rather than
// failing as if the request were wrong.
func TestIndexSourcesRequiresConfiguration(t *testing.T) {
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, nil, nil)

	if indexer.Enabled() {
		t.Fatal("Enabled() = true without upstream clients")
	}
	err := indexer.IndexSources(context.Background(), testKnowledgeBase(), []models.KnowledgeBaseSourceWithContent{
		textSource("source-1", "First", "Refunds are processed within fourteen days."),
	})
	if !errors.Is(err, ErrIndexingNotConfigured) {
		t.Errorf("error = %v, want ErrIndexingNotConfigured", err)
	}
}

// The record id is the title as given. Only the chunks after the first can
// carry anything extra, because a vector store keeps one record per id and
// would otherwise drop all but the last chunk of a split source.
func TestVectorID(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		chunkIndex int
		want       string
	}{
		{
			name:  "first chunk is the title verbatim",
			title: "Refund policy",
			want:  "Refund policy",
		},
		{
			name:  "punctuation and casing are preserved",
			title: "Shipping & Delivery times!",
			want:  "Shipping & Delivery times!",
		},
		{
			name:       "later chunks are numbered from two",
			title:      "Refund policy",
			chunkIndex: 1,
			want:       "Refund policy#2",
		},
		{
			name:       "numbering follows the chunk index",
			title:      "Refund policy",
			chunkIndex: 4,
			want:       "Refund policy#5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vectorID(tt.title, tt.chunkIndex); got != tt.want {
				t.Errorf("vectorID(%q, %d) = %q, want %q", tt.title, tt.chunkIndex, got, tt.want)
			}
		})
	}
}

// Deleting a source removes exactly the chunks it wrote — rebuilt from the row's
// title and chunk count — and leaves the rest of the namespace alone.
func TestDeleteSourceVectors(t *testing.T) {
	vectors := &fakeVectorStore{}
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, &fakeEmbedder{}, vectors)

	source := models.KnowledgeBaseSource{
		SourceID:   "source-1",
		Type:       models.KnowledgeBaseSourceTypeText,
		Title:      "Refund policy",
		ChunkCount: 3,
	}
	if err := indexer.DeleteSourceVectors(context.Background(), testKnowledgeBase(), source); err != nil {
		t.Fatalf("DeleteSourceVectors: %v", err)
	}

	want := []string{"Refund policy", "Refund policy#2", "Refund policy#3"}
	if len(vectors.deleted) != len(want) {
		t.Fatalf("deleted %v, want the source's %d chunks", vectors.deleted, len(want))
	}
	for i, id := range want {
		if vectors.deleted[i] != id {
			t.Errorf("deleted[%d] = %q, want %q", i, vectors.deleted[i], id)
		}
	}
	if vectors.deletedFrom != "kb_test" {
		t.Errorf("deleted from namespace %q, want the knowledge base's own", vectors.deletedFrom)
	}
}

// The ids the delete builds have to be the ids indexing wrote, or a deleted
// source would leave its chunks behind and keep being retrieved.
func TestDeleteSourceVectorsMatchesIndexedIDs(t *testing.T) {
	db := &fakeQuerier{}
	vectors := &fakeVectorStore{}
	indexer := NewIndexer(&Repository{db: db}, &fakeEmbedder{}, vectors)

	content := strings.Repeat("Refunds are processed within fourteen days. ", 60)
	if err := indexer.IndexSources(context.Background(), testKnowledgeBase(), []models.KnowledgeBaseSourceWithContent{
		textSource("source-1", "Refund policy", content),
	}); err != nil {
		t.Fatalf("IndexSources: %v", err)
	}

	// The chunk count the delete works from is the one indexing recorded.
	chunkCount, ok := db.execs[0][1].(int)
	if !ok {
		t.Fatalf("recorded chunk count %v is not an int", db.execs[0][1])
	}
	if err := indexer.DeleteSourceVectors(context.Background(), testKnowledgeBase(), models.KnowledgeBaseSource{
		SourceID:   "source-1",
		Title:      "Refund policy",
		ChunkCount: chunkCount,
	}); err != nil {
		t.Fatalf("DeleteSourceVectors: %v", err)
	}

	if len(vectors.deleted) != len(vectors.upserted) {
		t.Fatalf("deleted %d ids for %d upserted vectors", len(vectors.deleted), len(vectors.upserted))
	}
	for i, vector := range vectors.upserted {
		if vectors.deleted[i] != vector.ID {
			t.Errorf("deleted[%d] = %q, want the upserted id %q", i, vectors.deleted[i], vector.ID)
		}
	}
}

// A source that was stored but never indexed has nothing in the vector store, so
// it can be deleted even where indexing is not configured.
func TestDeleteSourceVectorsSkipsUnindexedSource(t *testing.T) {
	vectors := &fakeVectorStore{}
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, &fakeEmbedder{}, vectors)

	source := models.KnowledgeBaseSource{SourceID: "source-1", Title: "Never indexed", ChunkCount: 0}
	if err := indexer.DeleteSourceVectors(context.Background(), testKnowledgeBase(), source); err != nil {
		t.Fatalf("DeleteSourceVectors: %v", err)
	}
	if len(vectors.deleted) != 0 {
		t.Errorf("deleted %v, want no call to the vector store", vectors.deleted)
	}

	disabled := NewIndexer(&Repository{db: &fakeQuerier{}}, nil, nil)
	if err := disabled.DeleteSourceVectors(context.Background(), testKnowledgeBase(), source); err != nil {
		t.Errorf("DeleteSourceVectors on an unconfigured server: %v, want it to succeed", err)
	}
}

// An indexed source cannot be deleted without reaching the vector store, so an
// unconfigured server has to say so rather than pretend the chunks are gone.
func TestDeleteSourceVectorsRequiresConfiguration(t *testing.T) {
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, nil, nil)

	err := indexer.DeleteSourceVectors(context.Background(), testKnowledgeBase(), models.KnowledgeBaseSource{
		SourceID:   "source-1",
		Title:      "Refund policy",
		ChunkCount: 2,
	})
	if !errors.Is(err, ErrIndexingNotConfigured) {
		t.Errorf("error = %v, want ErrIndexingNotConfigured", err)
	}
}

// A vector store that refuses the delete must surface as a failure: the caller
// keeps the source row on the strength of it.
func TestDeleteSourceVectorsReportsFailure(t *testing.T) {
	vectors := &fakeVectorStore{deleteErr: errors.New("upstream is down")}
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, &fakeEmbedder{}, vectors)

	err := indexer.DeleteSourceVectors(context.Background(), testKnowledgeBase(), models.KnowledgeBaseSource{
		SourceID:   "source-1",
		Title:      "Refund policy",
		ChunkCount: 2,
	})
	if err == nil {
		t.Fatal("DeleteSourceVectors succeeded, want the upstream failure")
	}
}

// A knowledge base predating the namespace assignment has nowhere to write to,
// which must be reported rather than silently writing to the default namespace.
func TestIndexSourcesRequiresNamespace(t *testing.T) {
	indexer := NewIndexer(&Repository{db: &fakeQuerier{}}, &fakeEmbedder{}, &fakeVectorStore{})

	kb := testKnowledgeBase()
	kb.NamespaceID = nil

	err := indexer.IndexSources(context.Background(), kb, []models.KnowledgeBaseSourceWithContent{
		textSource("source-1", "First", "Refunds are processed within fourteen days."),
	})
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Errorf("error = %v, want a missing-namespace failure", err)
	}
}
