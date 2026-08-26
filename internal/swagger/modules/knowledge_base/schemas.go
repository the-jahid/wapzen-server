package knowledge_base

import (
	"fmt"
	"strings"

	"whatsapp-ai-caller-server/internal/knowledgebases"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// The knowledge base field contract mirrors Retell AI's Create Knowledge Base
// API (https://docs.retellai.com/api-references/create-knowledge-base) — the
// same form field names, chunk-size bounds and source variants. The REST
// structure around it follows this project's Agents module: JSON request
// bodies, the {success, message, data} envelope with links, paginated lists,
// and the shared ErrorResponse envelope for failures.
var knowledgeBaseStatusOptions = []string{"in_progress", "complete", "error", "refreshing_in_progress"}

const (
	defaultMaxChunkSize        = 2000
	minMaxChunkSize            = 600
	maxMaxChunkSize            = 6000
	defaultMinChunkSize        = 400
	minMinChunkSize            = 200
	maxMinChunkSize            = 2000
	maxKnowledgeBaseNameLength = 40
	maxKnowledgeBaseFiles      = 25
	maxSourceTitleLength       = 200
	maxTextsPerRequest         = 25
)

// knowledgeBaseNameSchema is written as a raw schema object because the fluent
// builder has no maxLength setter.
func knowledgeBaseNameSchema() oas.Object {
	return oas.Object{
		"type":        "string",
		"description": "Name of the knowledge base.",
		"maxLength":   maxKnowledgeBaseNameLength,
		"example":     "Sample KB",
	}
}

func maxChunkSizeSchema() *oas.Schema {
	return oas.Int().
		Desc("Maximum number of characters per chunk. Larger chunks keep more context together; smaller chunks retrieve more precisely.").
		Min(minMaxChunkSize).Max(maxMaxChunkSize).Default(defaultMaxChunkSize).Example(defaultMaxChunkSize)
}

func minChunkSizeSchema() *oas.Schema {
	return oas.Int().
		Desc("Minimum number of characters per chunk. Chunks smaller than this are merged into the next one.").
		Min(minMinChunkSize).Max(maxMinChunkSize).Default(defaultMinChunkSize).Example(defaultMinChunkSize)
}

func enableAutoRefreshSchema() *oas.Schema {
	return oas.Bool().
		Desc("Refresh the knowledge base URL sources automatically every 12 hours. Only applies to url sources.").
		Default(false).Example(true)
}

// ---------------------------------------------------------------------------
// Source input schemas (request side).
// ---------------------------------------------------------------------------

// knowledgeBaseTextSchema documents the title constraints that follow from the
// title being the vector store's record id.
func knowledgeBaseTextSchema() oas.Object {
	return oas.Obj().Desc("A raw text entry to index.").Req("title", "text").
		P("title", oas.Object{
			"type":        "string",
			"description": "Title of the text entry. It is used verbatim as the record id in the vector store, so it must be printable ASCII, must not contain '#', must be unique within the knowledge base, and is at most 200 characters. A text long enough to be split into several chunks stores the first under the title and the rest under \"<title>#2\", \"<title>#3\" and so on.",
			"maxLength":   maxSourceTitleLength,
			"example":     "Refund policy",
		}).
		P("text", oas.Str().Desc("Content of the text entry. It is split into chunks using the knowledge base's max_chunk_size and min_chunk_size.").Example("Refunds are processed within 14 days.")).
		Build()
}

func knowledgeBaseFileSchema() oas.Object {
	return oas.Obj().Desc("A file to fetch and index. Upload the file first, then reference it here by URL.").Req("filename", "file_url").
		P("filename", oas.Str().Desc("Name of the file.").Example("test_data.txt")).
		P("file_url", oas.Str().Desc("URL the file can be fetched from. Up to 50MB.").Example("https://storage.example.com/kb/test_data.txt")).
		Build()
}

func knowledgeBaseTextsSchema() *oas.Schema {
	return oas.Arr(oas.Ref("KnowledgeBaseText")).Desc("Raw text entries to index.")
}

func knowledgeBaseUrlsSchema() *oas.Schema {
	return oas.Arr(oas.Str().Example("https://www.retellai.com")).Desc("URLs to scrape and index.")
}

func knowledgeBaseFilesSchema() oas.Object {
	return oas.Object{
		"type":        "array",
		"description": "Files to fetch and index. At most 25 files, each up to 50MB.",
		"maxItems":    maxKnowledgeBaseFiles,
		"items":       oas.Ref("KnowledgeBaseFile"),
	}
}

// ---------------------------------------------------------------------------
// Source resource schemas (response side): the three Retell variants.
// ---------------------------------------------------------------------------

func documentSourceSchema() oas.Object {
	return oas.Obj().Desc("A source created from a file.").
		P("type", oas.Str().Desc("Source variant discriminator.").Enum([]string{"document"}).Example("document")).
		P("source_id", oas.Str().Desc("Unique identifier of the source.").Example("source_a456426614174000")).
		P("filename", oas.Str().Desc("Name of the file.").Example("test_data.txt")).
		P("file_url", oas.Str().Desc("URL of the stored file.").Example("https://storage.example.com/kb/test_data.txt")).
		P("file_size", oas.Int().Desc("Size of the file in bytes.").Example(24576)).
		Req("type", "source_id", "filename", "file_url", "file_size").
		Build()
}

// textSourceSchema reports the indexing result rather than the text: the
// content is stored to be re-chunked, not read back, and a source can be far
// larger than a listing should carry.
func textSourceSchema() oas.Object {
	return oas.Obj().Desc("A source created from a raw text entry.").
		P("type", oas.Str().Desc("Source variant discriminator.").Enum([]string{"text"}).Example("text")).
		P("source_id", oas.Str().Desc("Unique identifier of the source.").Example("source_b556426614174001")).
		P("title", oas.Str().Desc("Title of the text entry.").Example("Sample Question")).
		P("chunk_count", oas.Int().Desc("Number of chunks this source was split into and indexed as.").Example(3)).
		Req("type", "source_id", "title", "chunk_count").
		Build()
}

// fileSourceSchema is an uploaded document after indexing. It reports the same
// fields a text source does, because that is what it became: the server
// extracted the upload's text and chunked it exactly like a pasted one. The type
// is what still says where the content came from.
func fileSourceSchema() oas.Object {
	return oas.Obj().Desc("A source created from an uploaded file.").
		P("type", oas.Str().Desc("Source variant discriminator.").Enum([]string{"file"}).Example("file")).
		P("source_id", oas.Str().Desc("Unique identifier of the source.").Example("source_d756426614174003")).
		P("title", oas.Str().Desc("Title of the source. Defaults to the uploaded filename when the request supplies none.").Example("Employee handbook.pdf")).
		P("chunk_count", oas.Int().Desc("Number of chunks the extracted text was split into and indexed as.").Example(12)).
		Req("type", "source_id", "title", "chunk_count").
		Build()
}

func urlSourceSchema() oas.Object {
	return oas.Obj().Desc("A source created from a scraped URL.").
		P("type", oas.Str().Desc("Source variant discriminator.").Enum([]string{"url"}).Example("url")).
		P("source_id", oas.Str().Desc("Unique identifier of the source.").Example("source_c656426614174002")).
		P("url", oas.Str().Desc("URL that was scraped.").Example("https://www.retellai.com")).
		Req("type", "source_id", "url").
		Build()
}

// knowledgeBaseSourceSchema is the discriminated union of the three variants.
func knowledgeBaseSourceSchema() oas.Object {
	return oas.Object{
		"description": "A single indexed source. The type field selects the variant.",
		"oneOf": []any{
			oas.Ref("KnowledgeBaseDocumentSource"),
			oas.Ref("KnowledgeBaseFileSource"),
			oas.Ref("KnowledgeBaseTextSource"),
			oas.Ref("KnowledgeBaseUrlSource"),
		},
		"discriminator": oas.Object{
			"propertyName": "type",
			"mapping": oas.Object{
				"document": "#/components/schemas/KnowledgeBaseDocumentSource",
				"file":     "#/components/schemas/KnowledgeBaseFileSource",
				"text":     "#/components/schemas/KnowledgeBaseTextSource",
				"url":      "#/components/schemas/KnowledgeBaseUrlSource",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Resource and request schemas.
// ---------------------------------------------------------------------------

func knowledgeBaseResourceSchema() oas.Object {
	return oas.Obj().Desc("A knowledge base owned by the authenticated user.").
		P("knowledge_base_id", oas.Str().Desc("Unique identifier of the knowledge base.").Example("knowledge_base_a456426614174000")).
		P("knowledge_base_name", knowledgeBaseNameSchema()).
		P("status", oas.Str().Desc("Indexing status of the knowledge base.").Enum(knowledgeBaseStatusOptions).Example("in_progress")).
		P("namespace_id", oas.Str().Desc("Vector-store namespace holding this knowledge base's chunks. Assigned when the knowledge base is created and stable for its lifetime.").Example("kb_a456426614174000")).
		P("agent_id", oas.Str().Nullable().Desc("Agent this knowledge base belongs to, or null while it belongs to none. Read-only here: attach a knowledge base by writing the owning agent's `knowledge_base.knowledge_base_ids`. A knowledge base belongs to one agent, so a non-null value means attaching it elsewhere is rejected with 409 until it is detached here. Deleting that agent deletes this knowledge base with it.").Example(nil)).
		P("knowledge_base_sources", oas.Arr(oas.Ref("KnowledgeBaseSource")).Desc("The indexed sources. Populated once status is complete.")).
		P("enable_auto_refresh", enableAutoRefreshSchema()).
		P("last_refreshed_timestamp", oas.Int().Desc("Last refresh time, in milliseconds since epoch. Only present when auto refresh is enabled.").Example(1703413636133)).
		P("max_chunk_size", maxChunkSizeSchema()).
		P("min_chunk_size", minChunkSizeSchema()).
		Req("knowledge_base_id", "knowledge_base_name", "status").
		Build()
}

// createKnowledgeBaseRequestSchema marks only knowledge_base_name as required.
// The helpers below all return freshly built schemas, so re-describing them as
// optional here does not leak into the resource or add-sources schemas that
// share them.
func createKnowledgeBaseRequestSchema() oas.Object {
	name := knowledgeBaseNameSchema()
	name["description"] = "Name of the knowledge base. The only required field."

	urls := knowledgeBaseUrlsSchema().Desc("Not supported yet. URLs to scrape and index; supplying any is rejected until the url fetcher lands.")

	files := knowledgeBaseFilesSchema()
	files["description"] = "Not accepted here. Files are uploaded as multipart/form-data \"files\" parts to the sources endpoint rather than referenced by url; supplying file_url references is rejected."

	texts := knowledgeBaseTextsSchema().
		Desc("Optional. Raw text entries to index as part of the create. At most 25 per request. Each title must be unique within the knowledge base, since it becomes the record id in the vector store.").
		Build()
	texts["maxItems"] = maxTextsPerRequest

	return oas.Obj().
		Desc("Knowledge base configuration to create. Only knowledge_base_name is required; every other field is optional. Texts supplied here are chunked, embedded and indexed inside the request, so the knowledge base comes back at status complete with its sources attached; omit them to create an empty knowledge base and add content later. The create is all-or-nothing — if indexing fails the knowledge base is removed again and the same request can be retried.").
		Req("knowledge_base_name").
		P("knowledge_base_name", name).
		P("knowledge_base_texts", texts).
		P("knowledge_base_urls", urls).
		P("knowledge_base_files", files).
		P("enable_auto_refresh", enableAutoRefreshSchema().Desc("Optional. Refresh the knowledge base URL sources automatically every 12 hours. Only applies to url sources. Uses the default when omitted.")).
		P("max_chunk_size", maxChunkSizeSchema().Desc("Optional. Maximum number of characters per chunk. Larger chunks keep more context together; smaller chunks retrieve more precisely. Uses the default when omitted.")).
		P("min_chunk_size", minChunkSizeSchema().Desc("Optional. Minimum number of characters per chunk. Chunks smaller than this are merged into the next one. Uses the default when omitted.")).
		Build()
}

// updateKnowledgeBaseRequestSchema is a partial update: nothing is required, but
// at least one field must be present. Only the name and the indexing settings
// are settable — the sources and the status follow indexing, not the caller.
func updateKnowledgeBaseRequestSchema() oas.Object {
	name := knowledgeBaseNameSchema()
	name["description"] = "Optional. New name of the knowledge base. Must be unique among your knowledge bases."

	return oas.Obj().
		Desc("Fields to change on an existing knowledge base. Every field is optional, but at least one must be supplied; omitted fields keep their stored value. min_chunk_size is validated against the stored max_chunk_size (and vice versa), so supplying only one of them is safe.").
		P("knowledge_base_name", name).
		P("enable_auto_refresh", enableAutoRefreshSchema().Desc("Optional. Refresh the knowledge base URL sources automatically every 12 hours. Only applies to url sources.")).
		P("max_chunk_size", maxChunkSizeSchema().Desc("Optional. New maximum number of characters per chunk. Existing sources are not re-indexed.")).
		P("min_chunk_size", minChunkSizeSchema().Desc("Optional. New minimum number of characters per chunk. Existing sources are not re-indexed.")).
		Build()
}

// addKnowledgeBaseSourcesRequestSchema carries only the source fields; the
// chunk-size and auto-refresh settings are changed through PATCH instead.
//
// This is the JSON form of the request, which adds raw texts. Files are sent as
// multipart/form-data instead (see addKnowledgeBaseSourcesUploadSchema): they
// are uploaded to this API rather than fetched from a url, so the file_url form
// below stays a validation error. The url fields stay in the schema so the
// contract does not move when the scraper lands.
func addKnowledgeBaseSourcesRequestSchema() oas.Object {
	urls := knowledgeBaseUrlsSchema().Desc("Not supported yet. URLs to scrape and index; supplying any is rejected until the url fetcher lands.")

	files := knowledgeBaseFilesSchema()
	files["description"] = "Not accepted here. Files are uploaded as multipart/form-data \"files\" parts rather than referenced by url; supplying file_url references is rejected."

	texts := knowledgeBaseTextsSchema().
		Desc("Raw text entries to index. At least one is required; at most 25 per request. Each title must be unique within the knowledge base, since it becomes the record id in the vector store.").
		Build()
	texts["maxItems"] = maxTextsPerRequest

	return oas.Obj().
		Desc("Raw text sources to add to an existing knowledge base. knowledge_base_texts is required in this JSON form; upload documents with a multipart/form-data body instead.").
		Req("knowledge_base_texts").
		P("knowledge_base_texts", texts).
		P("knowledge_base_urls", urls).
		P("knowledge_base_files", files).
		Build()
}

// addKnowledgeBaseSourcesUploadSchema is the multipart form of the same
// request: the documents themselves, with optional titles alongside.
//
// The server extracts each upload's text — nothing is parsed client-side — so
// what the knowledge base indexes is what this server read out of the file.
func addKnowledgeBaseSourcesUploadSchema() oas.Object {
	return oas.Object{
		"type":        "object",
		"description": "Documents to extract and index. Repeat the \"files\" part once per document, and optionally repeat \"titles\" to name them: the nth title belongs to the nth file, and a blank or missing one falls back to the filename.",
		"required":    []any{models.KnowledgeBaseUploadFilesFormField},
		"properties": oas.Object{
			models.KnowledgeBaseUploadFilesFormField: oas.Object{
				"type": "array",
				"description": fmt.Sprintf(
					"The documents to index. Supported formats: %s. Each file is at most %d MB, at most %d files may be sent in one request, and a document may yield at most %d characters of text.",
					strings.Join(knowledgebases.SupportedFileExtensions(), ", "),
					models.KnowledgeBaseMaxFileBytes>>20,
					models.KnowledgeBaseMaxFilesPerRequest,
					models.KnowledgeBaseMaxSourceTextLength,
				),
				"maxItems": models.KnowledgeBaseMaxFilesPerRequest,
				"items": oas.Object{
					"type":   "string",
					"format": "binary",
				},
			},
			models.KnowledgeBaseUploadTitlesFormField: oas.Object{
				"type":        "array",
				"description": "Optional source titles, one per file in the same order. A title is used verbatim as the record id in the vector store, so it must be printable ASCII, must not contain '#', must be unique within the knowledge base, and is at most 200 characters.",
				"maxItems":    models.KnowledgeBaseMaxFilesPerRequest,
				"items": oas.Object{
					"type":      "string",
					"maxLength": maxSourceTitleLength,
					"example":   "Employee handbook",
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Response envelopes — same shape as the Agents module.
// ---------------------------------------------------------------------------

func resourceLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Resource links.").
		P("self", oas.Str().Desc("Canonical URL of this resource.").Example("/v1/knowledge-base/knowledge_base_a456426614174000"))
}

func listLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Pagination links as query URLs.").
		P("self", oas.Str().Example("/v1/knowledge-base?page=1&limit=20")).
		P("first", oas.Str().Example("/v1/knowledge-base?page=1&limit=20")).
		P("previous", oas.Str().Nullable().Desc("Previous page URL, or null on the first page.").Example(nil)).
		P("next", oas.Str().Nullable().Desc("Next page URL, or null on the last page.").Example(nil)).
		P("last", oas.Str().Example("/v1/knowledge-base?page=1&limit=20"))
}

func paginationMetaSchema() *oas.Schema {
	return oas.Obj().Desc("Collection metadata.").
		P("pagination", oas.Obj().
			P("page", oas.Int().Desc("Current page (1-based).").Example(1)).
			P("limit", oas.Int().Desc("Page size.").Example(20)).
			P("total_items", oas.Int().Desc("Total matching items.").Example(1)).
			P("total_pages", oas.Int().Desc("Total number of pages.").Example(1)).
			P("has_next_page", oas.Bool().Example(false)).
			P("has_previous_page", oas.Bool().Example(false)))
}

func createKnowledgeBaseResponseSchema() oas.Object {
	return knowledgeBaseEnvelopeSchema("Successful create response.", "Knowledge base created successfully")
}

func getKnowledgeBaseResponseSchema() oas.Object {
	return knowledgeBaseEnvelopeSchema("Successful single knowledge base response.", "Knowledge base retrieved successfully")
}

func updateKnowledgeBaseResponseSchema() oas.Object {
	return knowledgeBaseEnvelopeSchema("Successful update response.", "Knowledge base updated successfully")
}

// addKnowledgeBaseSourcesResponseSchema returns the updated knowledge base, so
// callers see the new sources and the refreshed status in one round trip.
func addKnowledgeBaseSourcesResponseSchema() oas.Object {
	return knowledgeBaseEnvelopeSchema("Successful add-sources response.", "Knowledge base sources added successfully")
}

func knowledgeBaseEnvelopeSchema(desc, message string) oas.Object {
	return oas.Obj().Desc(desc).Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example(message)).
		P("data", oas.Ref("KnowledgeBaseResource")).
		P("links", resourceLinksSchema()).
		Build()
}

func listKnowledgeBasesResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful paginated list response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Knowledge bases retrieved successfully")).
		P("data", oas.Arr(oas.Ref("KnowledgeBaseResource")).Desc("Page of knowledge base resources.")).
		P("meta", paginationMetaSchema()).
		P("links", listLinksSchema()).
		Build()
}

func deleteKnowledgeBaseResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful delete response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Knowledge base deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").
			P("id", oas.Str().Desc("Identifier of the deleted knowledge base.").Example("knowledge_base_a456426614174000")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func deleteKnowledgeBaseSourceResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful source delete response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Knowledge base source deleted successfully")).
		P("data", oas.Obj().Req("id", "knowledge_base_id", "deleted").
			P("id", oas.Str().Desc("Identifier of the deleted source.").Example("source_c656426614174002")).
			P("knowledge_base_id", oas.Str().Desc("Knowledge base the source belonged to.").Example("knowledge_base_a456426614174000")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func componentSchemas() oas.Object {
	return oas.Object{
		"KnowledgeBaseResource":             knowledgeBaseResourceSchema(),
		"KnowledgeBaseSource":               knowledgeBaseSourceSchema(),
		"KnowledgeBaseDocumentSource":       documentSourceSchema(),
		"KnowledgeBaseFileSource":           fileSourceSchema(),
		"KnowledgeBaseTextSource":           textSourceSchema(),
		"KnowledgeBaseUrlSource":            urlSourceSchema(),
		"KnowledgeBaseText":                 knowledgeBaseTextSchema(),
		"KnowledgeBaseFile":                 knowledgeBaseFileSchema(),
		"CreateKnowledgeBaseRequest":        createKnowledgeBaseRequestSchema(),
		"UpdateKnowledgeBaseRequest":        updateKnowledgeBaseRequestSchema(),
		"AddKnowledgeBaseSourcesRequest":    addKnowledgeBaseSourcesRequestSchema(),
		"AddKnowledgeBaseSourcesUpload":     addKnowledgeBaseSourcesUploadSchema(),
		"CreateKnowledgeBaseResponse":       createKnowledgeBaseResponseSchema(),
		"GetKnowledgeBaseResponse":          getKnowledgeBaseResponseSchema(),
		"UpdateKnowledgeBaseResponse":       updateKnowledgeBaseResponseSchema(),
		"ListKnowledgeBasesResponse":        listKnowledgeBasesResponseSchema(),
		"AddKnowledgeBaseSourcesResponse":   addKnowledgeBaseSourcesResponseSchema(),
		"DeleteKnowledgeBaseResponse":       deleteKnowledgeBaseResponseSchema(),
		"DeleteKnowledgeBaseSourceResponse": deleteKnowledgeBaseSourceResponseSchema(),
	}
}
