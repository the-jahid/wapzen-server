package models

import "time"

// Knowledge base indexing statuses. These mirror the knowledge_bases.status
// CHECK constraint; any value written back to the table must be one of these.
const (
	KnowledgeBaseStatusInProgress           = "in_progress"
	KnowledgeBaseStatusComplete             = "complete"
	KnowledgeBaseStatusError                = "error"
	KnowledgeBaseStatusRefreshingInProgress = "refreshing_in_progress"
)

// Chunk-size bounds and defaults, mirroring the knowledge_bases CHECK
// constraints and column defaults. The handler validates against these before
// the insert so a bad value reads as a field error instead of a 500.
const (
	KnowledgeBaseDefaultMaxChunkSize = 2000
	KnowledgeBaseMinMaxChunkSize     = 600
	KnowledgeBaseMaxMaxChunkSize     = 6000
	KnowledgeBaseDefaultMinChunkSize = 400
	KnowledgeBaseMinMinChunkSize     = 200
	KnowledgeBaseMaxMinChunkSize     = 2000
	KnowledgeBaseMaxNameLength       = 40
)

// Knowledge base source variants, mirroring the knowledge_base_sources.type
// CHECK constraint. A file source is text too by the time it is stored — the
// server extracts it on upload — so the type records where the content came
// from rather than how it is indexed. The url variant arrives with its fetcher.
const (
	KnowledgeBaseSourceTypeText = "text"
	KnowledgeBaseSourceTypeFile = "file"
)

// Bounds on the sources accepted in one add-sources request. The title length
// mirrors the knowledge_base_sources CHECK constraint; the count is a request
// bound, kept in line with the file limit the create contract documents.
const (
	KnowledgeBaseMaxSourceTitleLength = 200
	KnowledgeBaseMaxTextsPerRequest   = 25
)

// Bounds on an upload. Files are read, extracted and indexed inside the request,
// so these keep one call from turning into an unbounded amount of embedding
// work: the caller gets a clear refusal instead of a request that runs for
// minutes.
//
// The text length is a bound on the extracted result rather than on the file,
// since the two are only loosely related — a 200 KB PDF of scanned pages yields
// nothing, while a 200 KB text file is 200,000 characters to embed.
const (
	KnowledgeBaseMaxFilesPerRequest   = 10
	KnowledgeBaseMaxFileBytes         = 10 << 20 // 10 MiB
	KnowledgeBaseMaxSourceTextLength  = 300_000
	KnowledgeBaseMaxUploadTotalBytes  = KnowledgeBaseMaxFilesPerRequest * KnowledgeBaseMaxFileBytes
	KnowledgeBaseUploadFilesFormField = "files"
	// Titles ride along with the files so an upload can name its sources rather
	// than being stuck with the filenames; the nth title belongs to the nth file.
	KnowledgeBaseUploadTitlesFormField = "titles"
)

// KnowledgeBase is one knowledge base owned by an application user. The JSON
// shape is the documented KnowledgeBaseResource: the id is exposed as
// knowledge_base_id, and the last refresh is exposed as a millisecond
// timestamp rather than a time, both matching the published API contract.
//
// Sources is left unset when the knowledge base has none, so it is omitted from
// responses rather than reported as an empty list.
type KnowledgeBase struct {
	ID     string `json:"knowledge_base_id" example:"knowledge_base_a456426614174000"`
	UserID string `json:"-"`
	Name   string `json:"knowledge_base_name" example:"Sample KB"`
	Status string `json:"status" example:"in_progress"`
	// NamespaceID is the vector-store namespace holding this knowledge base's
	// chunks (the pinecone_namespace column). It is assigned at creation, so
	// every knowledge base created through this API has one; the pointer covers
	// rows predating that, which the backfill migration has since filled in.
	NamespaceID *string `json:"namespace_id,omitempty" example:"kb_a456426614174000"`
	// AgentID is the agent this knowledge base belongs to, or null while it
	// belongs to none. Read-only here: a knowledge base is attached by writing
	// the owning agent's knowledge_base.knowledge_base_ids, so the fact has one
	// write path rather than two that can disagree. It is returned so a client
	// can see which bases are already spoken for before it tries to attach one.
	AgentID                *string               `json:"agent_id" example:"agent_12345"`
	Sources                []KnowledgeBaseSource `json:"knowledge_base_sources,omitempty"`
	EnableAutoRefresh      bool                  `json:"enable_auto_refresh" example:"false"`
	LastRefreshedTimestamp *int64                `json:"last_refreshed_timestamp,omitempty" example:"1703413636133"`
	MaxChunkSize           int                   `json:"max_chunk_size" example:"2000"`
	MinChunkSize           int                   `json:"min_chunk_size" example:"400"`
}

// KnowledgeBaseSource is one indexed source of a knowledge base. Only the text
// variant can be indexed so far, so Title is the only variant-specific field
// modelled; the document and url fields arrive with their fetchers.
//
// The content itself is not returned: it is stored to be re-chunked, not to be
// read back, and a source can be far larger than a listing should carry.
// ChunkCount is how many vectors it produced, which is what callers actually
// need to see that indexing did something.
type KnowledgeBaseSource struct {
	Type       string `json:"type" example:"text"`
	SourceID   string `json:"source_id" example:"source_c656426614174002"`
	Title      string `json:"title,omitempty" example:"Refund policy"`
	ChunkCount int    `json:"chunk_count" example:"3"`
}

// NewKnowledgeBaseSource is one entry to store and index. Text is the content
// to chunk whatever the source is: a raw text arrives with it, and an uploaded
// file has been extracted into it before it gets here.
type NewKnowledgeBaseSource struct {
	Type  string
	Title string
	Text  string
}

// KnowledgeBaseSourceWithContent is a stored source together with the content
// the indexer chunks. The content stays out of KnowledgeBaseSource so it is
// never serialized into an API response by accident.
type KnowledgeBaseSourceWithContent struct {
	KnowledgeBaseSource
	Content string
}

// AgentKnowledgeBase is one knowledge base an agent is attached to, reduced to
// what a live call needs to retrieve from it: the namespace its chunks live in,
// and the name, which is what the model is told it can look things up in.
//
// It is deliberately not the full KnowledgeBase: a call resolves these on every
// inbound offer, and the chunk sizes and source list have no bearing on reading
// the vectors back.
type AgentKnowledgeBase struct {
	ID        string
	Name      string
	Namespace string
}

// KnowledgeSnippet is one passage retrieved from a knowledge base: the chunk
// text as it was indexed, the source title it came from, and how closely it
// matched the query (higher is closer).
type KnowledgeSnippet struct {
	KnowledgeBaseID string
	Title           string
	Text            string
	Score           float32
}

// NewKnowledgeBase is the data captured when a knowledge base is created. Only
// the name is required: the chunk sizes and the auto-refresh flag are optional
// and fall back to the column defaults when nil.
type NewKnowledgeBase struct {
	UserID            string
	Name              string
	EnableAutoRefresh *bool
	MaxChunkSize      *int
	MinChunkSize      *int
}

// KnowledgeBaseUpdate is a partial update of a knowledge base's name and
// indexing configuration. Every field is a pointer: a nil one is left untouched,
// so a caller changing one setting never has to restate the others. The indexed
// sources and the status are not settable here — those follow indexing.
type KnowledgeBaseUpdate struct {
	Name              *string
	EnableAutoRefresh *bool
	MaxChunkSize      *int
	MinChunkSize      *int
}

// IsEmpty reports whether the update would change nothing, which callers reject
// rather than issuing an UPDATE with no assignments.
func (u KnowledgeBaseUpdate) IsEmpty() bool {
	return u.Name == nil && u.EnableAutoRefresh == nil && u.MaxChunkSize == nil && u.MinChunkSize == nil
}

// SetLastRefreshed converts a nullable last_refreshed_at column into the
// millisecond timestamp the API exposes.
func (k *KnowledgeBase) SetLastRefreshed(at *time.Time) {
	if at == nil {
		k.LastRefreshedTimestamp = nil
		return
	}
	ms := at.UnixMilli()
	k.LastRefreshedTimestamp = &ms
}
