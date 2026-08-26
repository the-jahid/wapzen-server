package knowledgebases

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/pinecone"
)

// Embedder turns chunk text into vectors. Implemented by
// internal/embeddings.Client; an interface so the indexer can be tested without
// calling OpenAI.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	Model() string
	Enabled() bool
}

// VectorStore writes and removes a namespace's vectors. Implemented by
// internal/pinecone.Client.
type VectorStore interface {
	Upsert(ctx context.Context, namespace string, vectors []pinecone.Vector) error
	DeleteByIDs(ctx context.Context, namespace string, ids []string) error
	DeleteNamespace(ctx context.Context, namespace string) error
	Enabled() bool
}

// ErrIndexingNotConfigured is returned when the embedding or vector-store
// credentials are missing, so the caller can answer "unavailable" rather than
// failing as if the request were wrong.
var ErrIndexingNotConfigured = errors.New("knowledge base indexing is not configured")

// Indexer turns stored sources into the vectors of a knowledge base's
// namespace: chunk with the knowledge base's own chunk sizes, embed each chunk,
// then upsert the batch under the namespace assigned at creation.
type Indexer struct {
	repo     *Repository
	embedder Embedder
	vectors  VectorStore
}

// NewIndexer wires an indexer to its repository and upstream clients.
func NewIndexer(repo *Repository, embedder Embedder, vectors VectorStore) *Indexer {
	return &Indexer{repo: repo, embedder: embedder, vectors: vectors}
}

// Enabled reports whether both upstreams are configured. A disabled indexer
// fails every call with ErrIndexingNotConfigured rather than panicking, so the
// server still starts and every other knowledge base endpoint keeps working.
func (ix *Indexer) Enabled() bool {
	return ix != nil && ix.embedder != nil && ix.vectors != nil &&
		ix.embedder.Enabled() && ix.vectors.Enabled()
}

// IndexSources indexes freshly stored sources into kb's namespace and records
// each source's chunk count. Every source is text by this point, whether it was
// supplied as text or extracted from an uploaded file, so one path indexes both.
//
// It is all-or-nothing: the first failure removes whatever was already written
// for this call, so a knowledge base never ends up holding half of a source.
// The sources' rows are the caller's to clean up — it created them.
func (ix *Indexer) IndexSources(ctx context.Context, kb models.KnowledgeBase, sources []models.KnowledgeBaseSourceWithContent) error {
	if !ix.Enabled() {
		return ErrIndexingNotConfigured
	}
	if kb.NamespaceID == nil || *kb.NamespaceID == "" {
		return fmt.Errorf("knowledge base %s has no vector-store namespace", kb.ID)
	}
	if len(sources) == 0 {
		return nil
	}
	namespace := *kb.NamespaceID

	// written accumulates every vector id upserted so far, so a failure at any
	// point can undo the whole call.
	var written []string
	fail := func(err error) error {
		if len(written) > 0 {
			if cleanupErr := ix.vectors.DeleteByIDs(context.WithoutCancel(ctx), namespace, written); cleanupErr != nil {
				log.Printf("knowledgebases: rolling back %d vectors in namespace %s failed: %v", len(written), namespace, cleanupErr)
			}
		}
		return err
	}

	for _, source := range sources {
		chunks := chunkText(source.Content, kb.MinChunkSize, kb.MaxChunkSize)
		if len(chunks) == 0 {
			continue
		}

		embeddings, err := ix.embedder.Embed(ctx, chunks)
		if err != nil {
			return fail(fmt.Errorf("embed source %s: %w", source.SourceID, err))
		}
		if len(embeddings) != len(chunks) {
			return fail(fmt.Errorf("embed source %s: got %d vectors for %d chunks", source.SourceID, len(embeddings), len(chunks)))
		}

		vectors := make([]pinecone.Vector, 0, len(chunks))
		ids := make([]string, 0, len(chunks))
		for i, chunk := range chunks {
			id := vectorID(source.Title, i)
			ids = append(ids, id)
			vectors = append(vectors, pinecone.Vector{
				ID:     id,
				Values: embeddings[i],
				Metadata: map[string]any{
					"knowledge_base_id": kb.ID,
					"source_id":         source.SourceID,
					// The stored type, so a retrieval hit can say whether it came
					// from a pasted text or an uploaded file.
					"type":        source.Type,
					"title":       source.Title,
					"chunk_index": i,
					// The chunk text rides along so a retrieval hit can be turned
					// into prompt context without a second round trip to Postgres.
					"text": chunk,
				},
			})
		}

		if err := ix.vectors.Upsert(ctx, namespace, vectors); err != nil {
			// The upsert batches internally, so part of this source may be
			// written; its ids join the rollback set before the error propagates.
			written = append(written, ids...)
			return fail(fmt.Errorf("upsert source %s: %w", source.SourceID, err))
		}
		written = append(written, ids...)

		if err := ix.repo.SetSourceChunkCount(ctx, source.SourceID, len(chunks)); err != nil {
			return fail(err)
		}
	}

	return nil
}

// DeleteSourceVectors removes the vectors one source produced, leaving the rest
// of the namespace alone. The ids are rebuilt from the source row itself — its
// title and its stored chunk_count — which is what makes a source's chunks
// addressable again long after the text was indexed.
//
// A source whose chunk count is zero was stored but never indexed, so there is
// nothing in the vector store to remove and this succeeds without calling it;
// that is deliberately checked before Enabled, so such a source can still be
// deleted on a server where indexing is not configured.
func (ix *Indexer) DeleteSourceVectors(ctx context.Context, kb models.KnowledgeBase, source models.KnowledgeBaseSource) error {
	if source.ChunkCount <= 0 {
		return nil
	}
	if !ix.Enabled() {
		return ErrIndexingNotConfigured
	}
	if kb.NamespaceID == nil || *kb.NamespaceID == "" {
		return fmt.Errorf("knowledge base %s has no vector-store namespace", kb.ID)
	}
	return ix.vectors.DeleteByIDs(ctx, *kb.NamespaceID, sourceVectorIDs(source))
}

// sourceVectorIDs lists the ids of every chunk a source wrote, in the order they
// were written. It mirrors how IndexSources names them, so the two must
// change together.
func sourceVectorIDs(source models.KnowledgeBaseSource) []string {
	ids := make([]string, 0, source.ChunkCount)
	for i := 0; i < source.ChunkCount; i++ {
		ids = append(ids, vectorID(source.Title, i))
	}
	return ids
}

// PurgeNamespace removes every vector of a knowledge base's namespace. It is
// used when the knowledge base itself is deleted: the rows cascade in Postgres,
// but the vector store has no such link and would otherwise keep the orphans
// forever.
func (ix *Indexer) PurgeNamespace(ctx context.Context, kb models.KnowledgeBase) error {
	if !ix.Enabled() {
		return ErrIndexingNotConfigured
	}
	if kb.NamespaceID == nil || *kb.NamespaceID == "" {
		return nil
	}
	return ix.vectors.DeleteNamespace(ctx, *kb.NamespaceID)
}

// ChunkIDSeparator divides a source title from the chunk number in a vector id.
// Titles are rejected if they contain it, which is what makes a generated chunk
// id impossible to confuse with another source's title.
const ChunkIDSeparator = "#"

// vectorID is the id of one chunk in the vector store. It is the source's title
// verbatim, so the vector store lists knowledge base content under the names it
// was given rather than under opaque identifiers.
//
// A source that splits into several chunks cannot give them all one id — the
// vector store would keep only the last — so the chunks after the first carry
// the chunk number behind the separator. Ids stay derivable from the source row
// alone: its title, plus its stored chunk_count.
//
// Uniqueness rests on two guarantees outside this function: source titles are
// unique per knowledge base (a unique index), and a title may not contain the
// separator (validation). Without either, one source's chunk could overwrite
// another source's vector.
func vectorID(title string, chunkIndex int) string {
	if chunkIndex == 0 {
		return title
	}
	return title + ChunkIDSeparator + strconv.Itoa(chunkIndex+1)
}
