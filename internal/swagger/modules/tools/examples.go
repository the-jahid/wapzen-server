package tools

import "whatsapp-ai-caller-server/internal/swagger/oas"

// createToolRequestExample is the payload Swagger UI prefills into "Try it out".
// It is an api_request tool because that is the only type with enough shape to
// show how the configuration block, headers and parameters fit together.
func createToolRequestExample() oas.Object {
	return oas.Object{
		"type":        ToolTypeAPIRequest,
		"name":        "check_availability",
		"description": "Look up open appointment slots for a given day so the agent can offer times.",
		"api_request": oas.Object{
			"method":          "GET",
			"url":             "https://api.acme-health.example/v1/availability",
			"timeout_seconds": 20,
			"async":           false,
			"headers": []any{
				oas.Object{"key": "Authorization", "value": "Bearer sk-live-2f9c1e..."},
			},
			"parameters": []any{
				oas.Object{
					"name":        "date",
					"type":        "string",
					"description": "Requested day in YYYY-MM-DD form.",
					"required":    true,
				},
				oas.Object{
					"name":        "clinician",
					"type":        "string",
					"description": "Preferred clinician, if the caller names one.",
					"required":    false,
				},
			},
		},
	}
}

// apiRequestToolExample is the stored resource the create example produces. The
// attachment lists are empty: creating a tool only defines it, and an agent
// attaches it afterwards by sending its tools.tool_ids.
func apiRequestToolExample() oas.Object {
	tool := createToolRequestExample()
	tool["id"] = "tool_12345"
	tool["agent_ids"] = []any{}
	tool["chat_agent_ids"] = []any{}
	tool["created_at"] = "2026-08-19T10:00:00Z"
	tool["updated_at"] = "2026-08-19T10:00:00Z"
	return tool
}

// transferCallToolExample and endCallToolExample round out the list example, so
// the collection shows what a tool with a different configuration block — and
// one with none at all — looks like. This one is attached to two agents at once,
// which is what sharing a single definition looks like in the list.
func transferCallToolExample() oas.Object {
	return oas.Object{
		"id":             "tool_23456",
		"type":           ToolTypeTransferCall,
		"name":           "transfer_to_human",
		"description":    "Transfer the caller to the support desk when they ask for a person.",
		"agent_ids":      []any{"agent_12345", "agent_67890"},
		"chat_agent_ids": []any{},
		"transfer_call": oas.Object{
			"destination": "+8801639726992",
			"message":     "Connecting you to a teammate now, one moment.",
		},
		"created_at": "2026-08-19T10:05:00Z",
		"updated_at": "2026-08-19T10:05:00Z",
	}
}

func endCallToolExample() oas.Object {
	return oas.Object{
		"id":          "tool_34567",
		"type":        ToolTypeEndCall,
		"name":        "end_call",
		"description": "Hang up politely once the caller says they are done.",
		// Attached to nothing, and a chat agent could not use it anyway: ending a
		// call is meaningless in a conversation that has none.
		"agent_ids":      []any{},
		"chat_agent_ids": []any{},
		"created_at":     "2026-08-19T10:10:00Z",
		"updated_at":     "2026-08-19T10:10:00Z",
	}
}

func createToolResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Tool created successfully",
		"data":    apiRequestToolExample(),
		"links":   oas.Object{"self": "/v1/tools/tool_12345"},
	}
}

func toolResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Tool retrieved successfully",
		"data":    apiRequestToolExample(),
		"links":   oas.Object{"self": "/v1/tools/tool_12345"},
	}
}

func toolListResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Tools retrieved successfully",
		"data": []any{
			apiRequestToolExample(),
			transferCallToolExample(),
			endCallToolExample(),
		},
		"meta": oas.Object{
			"pagination": oas.Object{
				"page":              1,
				"limit":             50,
				"total_items":       3,
				"total_pages":       1,
				"has_next_page":     false,
				"has_previous_page": false,
			},
		},
		"links": oas.Object{
			"self":     "/v1/tools?page=1&limit=50",
			"first":    "/v1/tools?page=1&limit=50",
			"previous": nil,
			"next":     nil,
			"last":     "/v1/tools?page=1&limit=50",
		},
	}
}

// updateToolRequestExample changes only the description, which is the edit that
// actually moves the needle on whether the model calls a tool at the right time.
func updateToolRequestExample() oas.Object {
	return oas.Object{
		"description": "Look up open appointment slots for a given day. Call it before offering any time.",
	}
}

func updateToolResponseExample() oas.Object {
	tool := apiRequestToolExample()
	tool["description"] = "Look up open appointment slots for a given day. Call it before offering any time."
	tool["updated_at"] = "2026-08-19T11:30:00Z"
	return oas.Object{
		"success": true,
		"message": "Tool updated successfully",
		"data":    tool,
		"links":   oas.Object{"self": "/v1/tools/tool_12345"},
	}
}

func deleteToolResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Tool deleted successfully",
		"data": oas.Object{
			"id":      "tool_12345",
			"deleted": true,
		},
	}
}

// ---------------------------------------------------------------------------
// Error response examples — the shared ErrorResponse envelope, matching the
// convention the Agents, Knowledge Base and Outbound Campaigns modules use.
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

// createValidationErrorExample shows the two mistakes that actually get made on
// create: a name that is not a valid function name, and a configuration block
// that does not match the declared type.
func createValidationErrorExample() oas.Object {
	return errorExample("Validation failed",
		fieldError("name", "name must contain only lowercase letters, digits and underscores"),
		fieldError("api_request.url", "url is required when type is api_request"),
	)
}

// updateValidationErrorExample covers the empty-patch case and the attempt to
// change type, which is the update error most callers hit first.
func updateValidationErrorExample() oas.Object {
	return errorExample("Invalid request body",
		fieldError("type", "type cannot be changed after creation"),
		fieldError("body", "supply at least one field to update"),
	)
}

func listQueryValidationErrorExample() oas.Object {
	return errorExample("Invalid query parameters",
		fieldError("limit", "limit must be between 1 and 200"),
		fieldError("type", "type must be one of api_request, transfer_call, end_call, send_text"),
	)
}

func unauthorizedErrorExample() oas.Object {
	return errorExample("Authentication required",
		fieldError("authorization", "Missing or invalid bearer token"),
	)
}

func notFoundErrorExample() oas.Object {
	return errorExample("Tool not found",
		fieldError("tool_id", "no tool exists with this id for the authenticated user"),
	)
}

func conflictErrorExample() oas.Object {
	return errorExample("Tool name already in use",
		fieldError("name", "a tool named check_availability already exists for this user"),
	)
}

func internalErrorExample() oas.Object {
	return errorExample("Internal server error",
		fieldError("server", "an unexpected error occurred; retry the request"),
	)
}
