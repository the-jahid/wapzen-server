package knowledge_base

import (
	"fmt"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// ---------------------------------------------------------------------------
// Resource examples.
// ---------------------------------------------------------------------------

func documentSourceExample() oas.Object {
	return oas.Object{
		"type":      "document",
		"source_id": "source_a456426614174000",
		"filename":  "test_data.txt",
		"file_url":  "https://storage.example.com/kb/test_data.txt",
		"file_size": 24576,
	}
}

func textSourceExample() oas.Object {
	return oas.Object{
		"type":        "text",
		"source_id":   "source_b556426614174001",
		"title":       "Sample Question",
		"chunk_count": 3,
	}
}

func urlSourceExample() oas.Object {
	return oas.Object{
		"type":      "url",
		"source_id": "source_c656426614174002",
		"url":       "https://www.retellai.com",
	}
}

// knowledgeBaseExample is a freshly created knowledge base: the namespace is
// already assigned, but indexing is still running, so knowledge_base_sources is
// not populated yet, and no agent has attached it yet, so agent_id is null.
func knowledgeBaseExample() oas.Object {
	return oas.Object{
		"knowledge_base_id":        "knowledge_base_a456426614174000",
		"knowledge_base_name":      "Sample KB",
		"status":                   "in_progress",
		"namespace_id":             "kb_a456426614174000",
		"agent_id":                 nil,
		"max_chunk_size":           2000,
		"min_chunk_size":           400,
		"enable_auto_refresh":      true,
		"last_refreshed_timestamp": 1703413636133,
	}
}

// completeKnowledgeBaseExample is the same knowledge base after indexing, with
// one source of each variant.
func completeKnowledgeBaseExample() oas.Object {
	kb := knowledgeBaseExample()
	kb["status"] = "complete"
	kb["knowledge_base_sources"] = []any{
		documentSourceExample(),
		textSourceExample(),
		urlSourceExample(),
	}
	return kb
}

func selfLink() oas.Object {
	return oas.Object{"self": "/v1/knowledge-base/knowledge_base_a456426614174000"}
}

// ---------------------------------------------------------------------------
// Request examples.
// ---------------------------------------------------------------------------

// createKnowledgeBaseMinimalRequestExample is the smallest accepted body: the
// name alone. Sources may be added later, and the chunk sizes and auto-refresh
// flag fall back to their defaults.
func createKnowledgeBaseMinimalRequestExample() oas.Object {
	return oas.Object{
		"knowledge_base_name": "Sample KB",
	}
}

func createKnowledgeBaseRequestExample() oas.Object {
	return oas.Object{
		"knowledge_base_name": "Sample KB",
		"knowledge_base_texts": []any{
			oas.Object{"title": "Sample Question", "text": "Hello, how are you?"},
		},
		"knowledge_base_urls": []any{"https://www.retellai.com", "https://docs.retellai.com"},
		"knowledge_base_files": []any{
			oas.Object{"filename": "test_data.txt", "file_url": "https://storage.example.com/kb/test_data.txt"},
		},
		"enable_auto_refresh": true,
		"max_chunk_size":      2000,
		"min_chunk_size":      400,
	}
}

// updateKnowledgeBaseSettingsRequestExample retunes the chunking without
// touching the name.
func updateKnowledgeBaseSettingsRequestExample() oas.Object {
	return oas.Object{
		"max_chunk_size":      3000,
		"min_chunk_size":      600,
		"enable_auto_refresh": true,
	}
}

// updateKnowledgeBaseRenameRequestExample is the smallest useful update: one
// field.
func updateKnowledgeBaseRenameRequestExample() oas.Object {
	return oas.Object{
		"knowledge_base_name": "Support handbook",
	}
}

func addKnowledgeBaseSourcesRequestExample() oas.Object {
	return oas.Object{
		"knowledge_base_texts": []any{
			oas.Object{"title": "Refund policy", "text": "Refunds are processed within 14 days."},
			oas.Object{"title": "Shipping times", "text": "Orders ship within 2 business days and arrive in 3 to 5."},
		},
	}
}

// ---------------------------------------------------------------------------
// Success response examples.
// ---------------------------------------------------------------------------

func createKnowledgeBaseResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Knowledge base created successfully",
		"data":    knowledgeBaseExample(),
		"links":   selfLink(),
	}
}

func getKnowledgeBaseResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Knowledge base retrieved successfully",
		"data":    completeKnowledgeBaseExample(),
		"links":   selfLink(),
	}
}

// updateKnowledgeBaseResponseExample echoes the stored row after the settings
// change, so the caller can confirm what was written.
func updateKnowledgeBaseResponseExample() oas.Object {
	kb := completeKnowledgeBaseExample()
	kb["max_chunk_size"] = 3000
	kb["min_chunk_size"] = 600
	return oas.Object{
		"success": true,
		"message": "Knowledge base updated successfully",
		"data":    kb,
		"links":   selfLink(),
	}
}

func listKnowledgeBasesResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Knowledge bases retrieved successfully",
		"data":    []any{completeKnowledgeBaseExample()},
		"meta": oas.Object{
			"pagination": oas.Object{
				"page":              1,
				"limit":             20,
				"total_items":       1,
				"total_pages":       1,
				"has_next_page":     false,
				"has_previous_page": false,
			},
		},
		"links": oas.Object{
			"self":     "/v1/knowledge-base?page=1&limit=20",
			"first":    "/v1/knowledge-base?page=1&limit=20",
			"previous": nil,
			"next":     nil,
			"last":     "/v1/knowledge-base?page=1&limit=20",
		},
	}
}

// addKnowledgeBaseSourcesResponseExample is the knowledge base after indexing
// finished, since the request does not return until it has: the new sources are
// attached and the status has moved to complete.
func addKnowledgeBaseSourcesResponseExample() oas.Object {
	kb := completeKnowledgeBaseExample()
	kb["knowledge_base_sources"] = []any{textSourceExample()}
	return oas.Object{
		"success": true,
		"message": "Knowledge base sources added successfully",
		"data":    kb,
		"links":   selfLink(),
	}
}

func deleteKnowledgeBaseResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Knowledge base deleted successfully",
		"data": oas.Object{
			"id":      "knowledge_base_a456426614174000",
			"deleted": true,
		},
	}
}

func deleteKnowledgeBaseSourceResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Knowledge base source deleted successfully",
		"data": oas.Object{
			"id":                "source_c656426614174002",
			"knowledge_base_id": "knowledge_base_a456426614174000",
			"deleted":           true,
		},
	}
}

// ---------------------------------------------------------------------------
// Error response examples — the shared ErrorResponse envelope.
// ---------------------------------------------------------------------------

func errorExample(message string, fieldErrors ...oas.Object) oas.Object {
	errs := make([]any, 0, len(fieldErrors))
	for _, e := range fieldErrors {
		errs = append(errs, e)
	}
	return oas.Object{
		"success": false,
		"message": message,
		"errors":  errs,
	}
}

func fieldError(field, message string) oas.Object {
	return oas.Object{"field": field, "message": message}
}

func validationErrorExample() oas.Object {
	return errorExample("Validation failed",
		fieldError("knowledge_base_name", "knowledge_base_name is required and must be at most 40 characters"),
		fieldError("max_chunk_size", "max_chunk_size must be between 600 and 6000"),
	)
}

func sourcesValidationErrorExample() oas.Object {
	return errorExample("Invalid request body",
		fieldError("knowledge_base_texts", "supply at least one text entry"),
		fieldError("knowledge_base_urls", "url sources are not supported yet; supply knowledge_base_texts"),
		fieldError("files[1]", "scan.pdf: no text could be extracted from this file"),
	)
}

// uploadTooLargeErrorExample is the plain envelope: the body never made it far
// enough to be validated field by field.
func uploadTooLargeErrorExample() oas.Object {
	return oas.Object{
		"success": false,
		"message": fmt.Sprintf("the upload is too large; at most %d MB may be sent in one request", models.KnowledgeBaseMaxUploadTotalBytes>>20),
	}
}

func updateValidationErrorExample() oas.Object {
	return errorExample("Invalid request body",
		fieldError("min_chunk_size", "min_chunk_size must not be greater than max_chunk_size"),
	)
}

func listQueryValidationErrorExample() oas.Object {
	return errorExample("Invalid query parameters",
		fieldError("limit", "limit must be between 1 and 100"),
	)
}

func knowledgeBaseIDValidationErrorExample() oas.Object {
	return errorExample("Invalid path parameter",
		fieldError("knowledge_base_id", "knowledge_base_id is not a valid identifier"),
	)
}

func sourceIDValidationErrorExample() oas.Object {
	return errorExample("Invalid path parameter",
		fieldError("source_id", "source_id is not a valid identifier"),
	)
}

func unauthorizedErrorExample() oas.Object {
	return errorExample("Authentication required",
		fieldError("authorization", "Missing or invalid bearer token"),
	)
}

func notFoundErrorExample() oas.Object {
	return errorExample("Knowledge base not found",
		fieldError("knowledge_base_id", "No knowledge base exists with the given id"),
	)
}

func sourceNotFoundErrorExample() oas.Object {
	return errorExample("Knowledge base source not found",
		fieldError("source_id", "No source with the given id exists in this knowledge base"),
	)
}

func conflictErrorExample() oas.Object {
	return errorExample("Knowledge base name already in use",
		fieldError("knowledge_base_name", "A knowledge base with this name already exists"),
	)
}

func unprocessableErrorExample() oas.Object {
	return errorExample("Unprocessable entity",
		fieldError("knowledge_base_urls", "The URL could not be fetched or contained no indexable text"),
	)
}

func sourceTitleConflictErrorExample() oas.Object {
	return errorExample("Knowledge base source title already in use",
		fieldError("knowledge_base_texts[0].title", "This knowledge base already has a source titled \"Refund policy\""),
	)
}

func indexingUnavailableErrorExample() oas.Object {
	return errorExample("Knowledge base indexing is not configured",
		fieldError("server", "The embedding or vector store credentials are missing on this server"),
	)
}

func vectorDeleteFailedErrorExample() oas.Object {
	return errorExample("Failed to remove the source's vectors; the source was left in place",
		fieldError("source_id", "The vector store rejected the delete. The source is unchanged and the request can be retried"),
	)
}

func tooManyRequestsErrorExample() oas.Object {
	return errorExample("Too many requests",
		fieldError("rate_limit", "Request quota exceeded. Retry after 30 seconds"),
	)
}

func internalErrorExample() oas.Object {
	return errorExample("Internal server error",
		fieldError("server", "An unexpected error occurred while processing the request"),
	)
}
