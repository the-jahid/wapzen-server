package knowledge_base

import (
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// ---------------------------------------------------------------------------
// Response / request / parameter helpers — mirrors the Agents module.
// ---------------------------------------------------------------------------

func okJSON(desc, schemaName string, example any) oas.Object {
	return oas.Object{
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":  oas.Ref(schemaName),
				"example": example,
			},
		},
	}
}

func errJSON(desc string, example any) oas.Object {
	return oas.Object{
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":  oas.Ref("ErrorResponse"),
				"example": example,
			},
		},
	}
}

func resp401() oas.Object {
	return errJSON("Unauthorized - missing or invalid API key bearer token.", unauthorizedErrorExample())
}

func resp404() oas.Object {
	return errJSON("Not found — no knowledge base exists with the given id.", notFoundErrorExample())
}

func resp409() oas.Object {
	return errJSON("Conflict — a knowledge base with that name already exists.", conflictErrorExample())
}

func resp422() oas.Object {
	return errJSON("Unprocessable entity — the request is well-formed but a source could not be fetched or indexed.", unprocessableErrorExample())
}

func resp500() oas.Object {
	return errJSON("Internal server error.", internalErrorExample())
}

// resp429 carries the Retry-After header documented for rate limiting.
func resp429() oas.Object {
	r := errJSON("Too many requests — the client is being rate limited.", tooManyRequestsErrorExample())
	r["headers"] = oas.Object{
		"Retry-After": oas.Object{
			"description": "Number of seconds to wait before retrying.",
			"schema":      oas.Object{"type": "integer"},
			"example":     30,
		},
	}
	return r
}

func requestBody(desc, schemaName string, example any) oas.Object {
	return oas.Object{
		"required":    true,
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":  oas.Ref(schemaName),
				"example": example,
			},
		},
	}
}

// requestBodyNamedExamples renders a picker in Swagger UI instead of a single
// example, so the minimal body is visible next to the fully populated one.
func requestBodyNamedExamples(desc, schemaName string, named oas.Object) oas.Object {
	return oas.Object{
		"required":    true,
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":   oas.Ref(schemaName),
				"examples": named,
			},
		},
	}
}

// addKnowledgeBaseSourcesRequestBody documents both ways sources are supplied.
// They are two content types of one operation rather than two endpoints because
// they do the same thing — a file is a way to deliver a source's text, not a
// different kind of source — and both come back with the same knowledge base.
func addKnowledgeBaseSourcesRequestBody() oas.Object {
	return oas.Object{
		"required":    true,
		"description": "The sources to add: raw texts as JSON, or documents as a multipart upload.",
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":  oas.Ref("AddKnowledgeBaseSourcesRequest"),
				"example": addKnowledgeBaseSourcesRequestExample(),
			},
			"multipart/form-data": oas.Object{
				"schema": oas.Ref("AddKnowledgeBaseSourcesUpload"),
				"encoding": oas.Object{
					models.KnowledgeBaseUploadFilesFormField: oas.Object{
						// Repeated parts rather than one part holding an array: it is
						// what a browser's FormData sends and what the server reads.
						"style":   "form",
						"explode": true,
					},
					models.KnowledgeBaseUploadTitlesFormField: oas.Object{
						"style":       "form",
						"explode":     true,
						"contentType": "text/plain",
					},
				},
			},
		},
	}
}

func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
}

func knowledgeBaseIDParam() oas.Object {
	return oas.Object{
		"name":        "knowledge_base_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the knowledge base.",
		"schema":      oas.Object{"type": "string", "example": "knowledge_base_a456426614174000"},
	}
}

func sourceIDParam() oas.Object {
	return oas.Object{
		"name":        "source_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the knowledge base source.",
		"schema":      oas.Object{"type": "string", "example": "source_c656426614174002"},
	}
}

func pageParam() oas.Object {
	return oas.Object{
		"name":        "page",
		"in":          "query",
		"required":    false,
		"description": "Page number (1-based).",
		"schema":      oas.Int().Min(1).Default(1).Build(),
	}
}

func limitParam() oas.Object {
	return oas.Object{
		"name":        "limit",
		"in":          "query",
		"required":    false,
		"description": "Number of items per page.",
		"schema":      oas.Int().Min(1).Max(100).Default(20).Build(),
	}
}

// ---------------------------------------------------------------------------
// Operations.
// ---------------------------------------------------------------------------

func listKnowledgeBasesOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listKnowledgeBases",
		"summary":     "List Knowledge Bases",
		"description": "Returns a paginated list of the knowledge bases owned by the authenticated API key owner, newest first, each with its indexed sources. A knowledge base that has had no sources added carries no `knowledge_base_sources`.",
		"security":    apiKeySecurity(),
		"parameters":  []any{pageParam(), limitParam()},
		"responses": oas.Object{
			"200": okJSON("A page of knowledge bases.", "ListKnowledgeBasesResponse", listKnowledgeBasesResponseExample()),
			"400": errJSON("Invalid query parameters (page or limit).", listQueryValidationErrorExample()),
			"401": resp401(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func createKnowledgeBaseOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "createKnowledgeBase",
		"summary":     "Create Knowledge Base",
		"description": "Creates a knowledge base owned by the authenticated API key owner. `knowledge_base_name` is the only required field — every other field is optional. Omitted `max_chunk_size`, `min_chunk_size` and `enable_auto_refresh` fall back to their defaults. The vector `namespace_id` is assigned here and is stable for the knowledge base's lifetime. Note: sources supplied to this endpoint are validated but not indexed — a newly created knowledge base is always empty. Add its content with POST /v1/knowledge-base/{knowledge_base_id}/sources, which does index what it is given.",
		"security":    apiKeySecurity(),
		"requestBody": requestBodyNamedExamples("The knowledge base configuration to create. Only `knowledge_base_name` is required.", "CreateKnowledgeBaseRequest", oas.Object{
			"minimal": oas.Object{
				"summary": "Minimal: only the required knowledge_base_name",
				"value":   createKnowledgeBaseMinimalRequestExample(),
			},
			"allFields": oas.Object{
				"summary": "All fields: name plus optional sources and settings",
				"value":   createKnowledgeBaseRequestExample(),
			},
		}),
		"responses": oas.Object{
			"201": okJSON("The created knowledge base resource.", "CreateKnowledgeBaseResponse", createKnowledgeBaseResponseExample()),
			"400": errJSON("Validation failed for the request body.", validationErrorExample()),
			"401": resp401(),
			"409": resp409(),
			"422": resp422(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func getKnowledgeBaseOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "getKnowledgeBase",
		"summary":     "Get Knowledge Base",
		"description": "Returns a single knowledge base by id, including its indexed sources, scoped to the authenticated API key owner. `knowledge_base_sources` is omitted when none have been added.",
		"security":    apiKeySecurity(),
		"parameters":  []any{knowledgeBaseIDParam()},
		"responses": oas.Object{
			"200": okJSON("The requested knowledge base resource.", "GetKnowledgeBaseResponse", getKnowledgeBaseResponseExample()),
			"400": errJSON("Invalid knowledge_base_id path parameter.", knowledgeBaseIDValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func updateKnowledgeBaseOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "updateKnowledgeBase",
		"summary":     "Update Knowledge Base",
		"description": "Applies a partial update to a knowledge base owned by the authenticated API key owner. Only `knowledge_base_name` and the indexing settings may be changed; at least one field must be supplied and omitted fields keep their stored value. Chunk sizes are checked against the stored row, so supplying only one of them can never leave `min_chunk_size` above `max_chunk_size`. Changing the chunking does not re-index sources that are already indexed.",
		"security":    apiKeySecurity(),
		"parameters":  []any{knowledgeBaseIDParam()},
		"requestBody": requestBodyNamedExamples("The fields to change. At least one is required.", "UpdateKnowledgeBaseRequest", oas.Object{
			"settings": oas.Object{
				"summary": "Retune the chunking and auto refresh",
				"value":   updateKnowledgeBaseSettingsRequestExample(),
			},
			"rename": oas.Object{
				"summary": "Rename only",
				"value":   updateKnowledgeBaseRenameRequestExample(),
			},
		}),
		"responses": oas.Object{
			"200": okJSON("The updated knowledge base resource.", "UpdateKnowledgeBaseResponse", updateKnowledgeBaseResponseExample()),
			"400": errJSON("Validation failed for the request body.", updateValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"409": resp409(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func deleteKnowledgeBaseOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "deleteKnowledgeBase",
		"summary":     "Delete Knowledge Base",
		"description": "Deletes a knowledge base by id, scoped to the authenticated API key owner, together with all of its indexed sources and every vector written under its namespace. A knowledge base belongs to at most one agent, so the agent holding it — if any — simply stops answering from it.",
		"security":    apiKeySecurity(),
		"parameters":  []any{knowledgeBaseIDParam()},
		"responses": oas.Object{
			"200": okJSON("The knowledge base was deleted.", "DeleteKnowledgeBaseResponse", deleteKnowledgeBaseResponseExample()),
			"400": errJSON("Invalid knowledge_base_id path parameter.", knowledgeBaseIDValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func addKnowledgeBaseSourcesOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "addKnowledgeBaseSources",
		"summary":     "Add Knowledge Base Sources",
		"description": "Adds sources to an existing knowledge base and indexes them into its vector namespace. Sources arrive one of two ways: a JSON body carries raw texts in `knowledge_base_texts`, and a `multipart/form-data` body uploads documents in repeated `files` parts. An upload is extracted **server-side** — nothing is parsed by the client — so the indexed text is the text this API read out of the document; name the sources with repeated `titles` parts, where the nth title belongs to the nth file and a blank or missing one falls back to the filename. Either way the resulting text is chunked using the knowledge base's own `max_chunk_size` and `min_chunk_size`, embedded, and written under its `namespace_id` with the source `title` as the record id — so records are listed in the vector store under the names they were given. A source long enough to split stores its first chunk under the title and the rest under `<title>#2`, `<title>#3` and so on. Titles must therefore be printable ASCII, must not contain `#`, and must be unique within the knowledge base; a duplicate returns 409. Indexing runs inside the request, so the knowledge base comes back already at status complete with the new sources and their chunk counts attached — there is nothing to poll. The call is all-or-nothing: one unreadable file fails the whole upload, and if indexing fails the sources are removed again and the knowledge base is left exactly as it was, at status error. `knowledge_base_urls` is still rejected until the scraper lands, and so is the JSON `knowledge_base_files` (`file_url`) form — a file is uploaded here, not fetched from elsewhere.",
		"security":    apiKeySecurity(),
		"parameters":  []any{knowledgeBaseIDParam()},
		"requestBody": addKnowledgeBaseSourcesRequestBody(),
		"responses": oas.Object{
			"201": okJSON("The updated knowledge base resource, with the new sources indexed.", "AddKnowledgeBaseSourcesResponse", addKnowledgeBaseSourcesResponseExample()),
			"400": errJSON("Validation failed for the request body, or a file could not be read: an unsupported format, an empty document, a scan with no text layer, or text past the per-source limit.", sourcesValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"409": errJSON("Conflict — the knowledge base already has a source with one of these titles.", sourceTitleConflictErrorExample()),
			"413": errJSON("The upload is larger than the request limit.", uploadTooLargeErrorExample()),
			"422": errJSON("Unprocessable entity — the sources are well-formed but could not be embedded or written to the vector store. Nothing was added.", unprocessableErrorExample()),
			"429": resp429(),
			"500": resp500(),
			"503": errJSON("Indexing is not configured on this server, so no source can be added.", indexingUnavailableErrorExample()),
		},
	}
}

func deleteKnowledgeBaseSourceOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "deleteKnowledgeBaseSource",
		"summary":     "Delete Knowledge Base Source",
		"description": "Removes a single source from a knowledge base, together with every vector it wrote into the knowledge base's namespace — the chunks stored under its `title` and under `<title>#2`, `<title>#3` and so on. The knowledge base itself, its other sources and its chunking configuration are left untouched, and nothing is re-indexed. The vectors are deleted before the source row: if the vector store cannot be reached the source stays in place, to be retried, rather than leaving behind chunks that agents would go on retrieving from a source that has supposedly been deleted.",
		"security":    apiKeySecurity(),
		"parameters":  []any{knowledgeBaseIDParam(), sourceIDParam()},
		"responses": oas.Object{
			"200": okJSON("The source was deleted.", "DeleteKnowledgeBaseSourceResponse", deleteKnowledgeBaseSourceResponseExample()),
			"400": errJSON("Invalid knowledge_base_id or source_id path parameter.", sourceIDValidationErrorExample()),
			"401": resp401(),
			"404": errJSON("Not found — no such knowledge base, or no such source in it.", sourceNotFoundErrorExample()),
			"429": resp429(),
			"500": resp500(),
			"502": errJSON("The vector store could not be reached, so the source's chunks are still indexed. Nothing was deleted.", vectorDeleteFailedErrorExample()),
			"503": errJSON("Indexing is not configured on this server, so an indexed source's vectors cannot be removed.", indexingUnavailableErrorExample()),
		},
	}
}

// knowledgeBasePaths returns the path-item map to merge into document.paths.
func knowledgeBasePaths() oas.Object {
	collection := oas.APIV1Prefix + "/knowledge-base"
	item := collection + "/{knowledge_base_id}"
	sources := item + "/sources"
	source := sources + "/{source_id}"
	return oas.Object{
		collection: oas.Object{
			"get":  listKnowledgeBasesOperation(),
			"post": createKnowledgeBaseOperation(),
		},
		item: oas.Object{
			"get":    getKnowledgeBaseOperation(),
			"patch":  updateKnowledgeBaseOperation(),
			"delete": deleteKnowledgeBaseOperation(),
		},
		sources: oas.Object{
			"post": addKnowledgeBaseSourcesOperation(),
		},
		source: oas.Object{
			"delete": deleteKnowledgeBaseSourceOperation(),
		},
	}
}
