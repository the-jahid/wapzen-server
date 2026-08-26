package tools

import "whatsapp-ai-caller-server/internal/swagger/oas"

func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
}

func toolIDParam() oas.Object {
	return oas.Object{
		"name":        "tool_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the tool.",
		"schema":      oas.Object{"type": "string"},
	}
}

// errJSON documents a failure with the shared ErrorResponse envelope and a
// worked example body, so the error cases render the same way the success ones
// do rather than as a bare sentence.
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
	return errJSON("Not found - no tool exists with the given id for the authenticated user.", notFoundErrorExample())
}

func resp409() oas.Object {
	return errJSON("A tool with this name already exists for this user.", conflictErrorExample())
}

func resp500() oas.Object {
	return errJSON("Internal server error.", internalErrorExample())
}

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
		"description": "Number of tools per page (1–200, default 50).",
		"schema":      oas.Int().Min(1).Max(200).Default(50).Build(),
	}
}

func typeParam() oas.Object {
	return oas.Object{
		"name":        "type",
		"in":          "query",
		"required":    false,
		"description": "Return only tools of this type.",
		"schema":      oas.Str().Enum(ToolTypes()).Build(),
	}
}

func listToolsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listTools",
		"summary":     "List Tools",
		"description": "Returns a paginated list of the tools owned by the authenticated API key owner, newest first.",
		"security":    apiKeySecurity(),
		"parameters":  []any{pageParam(), limitParam(), typeParam()},
		"responses": oas.Object{
			"200": okJSON("A page of tools.", "ToolListResponse", toolListResponseExample()),
			"400": errJSON("Invalid query parameters (page, limit or type).", listQueryValidationErrorExample()),
			"401": resp401(),
			"500": resp500(),
		},
	}
}

func createToolOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "createTool",
		"summary":     "Create Tool",
		"description": "Creates a tool the authenticated user's agents can call mid-call. The tool is inert " +
			"until it is attached to an agent: creating it only defines the action, it does not offer it on any " +
			"call. name and description are what the model reads when deciding whether to call it, so they carry " +
			"the same weight as prompt text.",
		"security":    apiKeySecurity(),
		"requestBody": requestBody("The tool to create.", "CreateToolRequest", createToolRequestExample()),
		"responses": oas.Object{
			"201": okJSON("The created tool.", "CreateToolResponse", createToolResponseExample()),
			"400": errJSON("Validation failed — type is not a known tool type, name is missing or not a valid "+
				"function name, or the configuration block required by type is missing or malformed.",
				createValidationErrorExample()),
			"401": resp401(),
			"409": resp409(),
			"500": resp500(),
		},
	}
}

func getToolOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "getTool",
		"summary":     "Get Tool",
		"description": "Returns a single tool owned by the authenticated user, including its configuration block.",
		"security":    apiKeySecurity(),
		"parameters":  []any{toolIDParam()},
		"responses": oas.Object{
			"200": okJSON("The tool.", "ToolResponse", toolResponseExample()),
			"401": resp401(),
			"404": resp404(),
			"500": resp500(),
		},
	}
}

func updateToolOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "updateTool",
		"summary":     "Update Tool",
		"description": "Applies a partial update to a tool; at least one field must be supplied. A configuration " +
			"block replaces the stored one wholesale. type is fixed at creation. Changes take effect on the next " +
			"call: a call already in progress keeps the tool definitions it started with.",
		"security":    apiKeySecurity(),
		"parameters":  []any{toolIDParam()},
		"requestBody": requestBody("The tool fields to change.", "UpdateToolRequest", updateToolRequestExample()),
		"responses": oas.Object{
			"200": okJSON("The updated tool.", "ToolResponse", updateToolResponseExample()),
			"400": errJSON("Validation failed, no updatable field was supplied, or the request tried to change type.",
				updateValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"409": resp409(),
			"500": resp500(),
		},
	}
}

func deleteToolOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "deleteTool",
		"summary":     "Delete Tool",
		"description": "Deletes a tool owned by the authenticated user. A tool belongs to at most one agent, so " +
			"the agent holding it — if any — simply loses it. Calls already in progress keep the tool for the " +
			"rest of the call.",
		"security":   apiKeySecurity(),
		"parameters": []any{toolIDParam()},
		"responses": oas.Object{
			"200": okJSON("The tool was deleted.", "ToolDeleteResponse", deleteToolResponseExample()),
			"401": resp401(),
			"404": resp404(),
			"500": resp500(),
		},
	}
}

func toolPaths() oas.Object {
	collection := oas.APIV1Prefix + "/tools"
	item := oas.APIV1Prefix + "/tools/{tool_id}"
	return oas.Object{
		collection: oas.Object{
			"get":  listToolsOperation(),
			"post": createToolOperation(),
		},
		item: oas.Object{
			"get":    getToolOperation(),
			"patch":  updateToolOperation(),
			"delete": deleteToolOperation(),
		},
	}
}
