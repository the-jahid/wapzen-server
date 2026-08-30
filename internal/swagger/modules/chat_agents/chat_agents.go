// Package chat_agents registers the static OpenAPI contract for chat-agent
// CRUD. It changes documentation only; live routes are registered elsewhere.
package chat_agents

import "whatsapp-ai-caller-server/internal/swagger/oas"

const tagName = "Chat Agents"

// InjectStaticChatAgentDocs adds the chat-agent tag, paths, and schemas to the
// shared OpenAPI document served by Swagger UI.
func InjectStaticChatAgentDocs(document oas.OpenAPIObject) {
	tags, _ := document["tags"].([]any)
	document["tags"] = append(tags, oas.Object{
		"name":        tagName,
		"description": "Chat-agent create, read, update, and delete API contract.",
	})

	paths, ok := document["paths"].(oas.Object)
	if !ok {
		paths = oas.Object{}
		document["paths"] = paths
	}
	for path, item := range chatAgentPaths() {
		paths[path] = item
	}

	components, ok := document["components"].(oas.Object)
	if !ok {
		components = oas.Object{}
		document["components"] = components
	}
	schemas, ok := components["schemas"].(oas.Object)
	if !ok {
		schemas = oas.Object{}
		components["schemas"] = schemas
	}
	for name, schema := range chatAgentSchemas() {
		schemas[name] = schema
	}
}

func chatAgentPaths() oas.Object {
	return oas.Object{
		"/v1/chat-agents": oas.Object{
			"post": createChatAgentOperation(),
			"get":  listChatAgentsOperation(),
		},
		"/v1/chat-agents/{chat_agent_id}": oas.Object{
			"get":    getChatAgentOperation(),
			"patch":  updateChatAgentOperation(),
			"delete": deleteChatAgentOperation(),
		},
	}
}

func createChatAgentOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "createChatAgent",
		"summary":     "Create chat agent",
		"description": "Creates a WhatsApp chat agent for the authenticated user. Only agent.name is required. All other settings are optional. A phone number may be assigned to at most one chat agent, and a chat agent may have at most one phone number.",
		"security":    apiKeySecurity(),
		"requestBody": jsonRequest("CreateChatAgentRequest", true, createRequestExample()),
		"responses": oas.Object{
			"201": jsonResponse("Chat agent created.", "ChatAgentResponse", responseExample("Chat agent created successfully")),
			"400": errorResponse("Invalid request."),
			"401": errorResponse("Unauthorized."),
			"404": errorResponse("A referenced phone number, knowledge base, or tool was not found."),
			"409": errorResponse("The name or an assigned resource is already in use."),
		},
	}
}

func listChatAgentsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listChatAgents",
		"summary":     "List chat agents",
		"description": "Returns the authenticated user's chat agents, newest first.",
		"security":    apiKeySecurity(),
		"parameters": []any{
			queryParameter("page", "Page number, starting at 1.", oas.Int().Min(1).Default(1).Build()),
			queryParameter("limit", "Items per page.", oas.Int().Min(1).Max(100).Default(20).Build()),
		},
		"responses": oas.Object{
			"200": jsonResponse("Chat agents retrieved.", "ListChatAgentsResponse", listResponseExample()),
			"400": errorResponse("Invalid pagination parameters."),
			"401": errorResponse("Unauthorized."),
		},
	}
}

func getChatAgentOperation() oas.Object {
	return itemOperation("getChatAgent", "Get chat agent", "Returns one chat agent owned by the authenticated user.", oas.Object{
		"200": jsonResponse("Chat agent retrieved.", "ChatAgentResponse", responseExample("Chat agent retrieved successfully")),
		"401": errorResponse("Unauthorized."),
		"404": errorResponse("Chat agent not found."),
	})
}

func updateChatAgentOperation() oas.Object {
	op := itemOperation("updateChatAgent", "Update chat agent", "Partially updates a chat agent. Omitted fields remain unchanged; supplied knowledge-base and tool id arrays replace their current attachment sets.", oas.Object{
		"200": jsonResponse("Chat agent updated.", "ChatAgentResponse", responseExample("Chat agent updated successfully")),
		"400": errorResponse("Invalid request."),
		"401": errorResponse("Unauthorized."),
		"404": errorResponse("The chat agent or a referenced resource was not found."),
		"409": errorResponse("The name or an assigned resource is already in use."),
	})
	op["requestBody"] = jsonRequest("UpdateChatAgentRequest", true, oas.Object{
		"agent": oas.Object{"status": "inactive"},
	})
	return op
}

func deleteChatAgentOperation() oas.Object {
	return itemOperation("deleteChatAgent", "Delete chat agent", "Deletes a chat agent owned by the authenticated user.", oas.Object{
		"200": jsonResponse("Chat agent deleted.", "DeleteChatAgentResponse", oas.Object{
			"success": true,
			"message": "Chat agent deleted successfully",
			"data":    oas.Object{"id": "chat_agent_12345", "deleted": true},
		}),
		"401": errorResponse("Unauthorized."),
		"404": errorResponse("Chat agent not found."),
	})
}

func itemOperation(operationID, summary, description string, responses oas.Object) oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": operationID,
		"summary":     summary,
		"description": description,
		"security":    apiKeySecurity(),
		"parameters": []any{oas.Object{
			"name":        "chat_agent_id",
			"in":          "path",
			"required":    true,
			"description": "Unique chat-agent identifier.",
			"schema":      oas.Str().Example("chat_agent_12345").Build(),
		}},
		"responses": responses,
	}
}

func queryParameter(name, description string, schema oas.Object) oas.Object {
	return oas.Object{
		"name": name, "in": "query", "required": false,
		"description": description, "schema": schema,
	}
}

func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
}

func jsonRequest(schemaName string, required bool, example any) oas.Object {
	return oas.Object{
		"required": required,
		"content": oas.Object{"application/json": oas.Object{
			"schema": oas.Ref(schemaName), "example": example,
		}},
	}
}

func jsonResponse(description, schemaName string, example any) oas.Object {
	return oas.Object{
		"description": description,
		"content": oas.Object{"application/json": oas.Object{
			"schema": oas.Ref(schemaName), "example": example,
		}},
	}
}

func errorResponse(description string) oas.Object {
	return oas.Object{
		"description": description,
		"content": oas.Object{"application/json": oas.Object{
			"schema": oas.Ref("ErrorResponse"),
		}},
	}
}

func chatAgentSchemas() oas.Object {
	return oas.Object{
		"CreateChatAgentRequest":  createChatAgentRequestSchema(),
		"UpdateChatAgentRequest":  updateChatAgentRequestSchema(),
		"ChatAgentResource":       chatAgentResourceSchema(),
		"ChatAgentResponse":       chatAgentResponseSchema(),
		"ListChatAgentsResponse":  listChatAgentsResponseSchema(),
		"DeleteChatAgentResponse": deleteChatAgentResponseSchema(),
	}
}

func agentSectionSchema(requireName bool) oas.Object {
	s := oas.Obj().Desc("General chat-agent settings.").
		P("name", oas.Str().Desc("Name unique within the authenticated user's chat agents.").Example("Support Chat"))
	if requireName {
		s.Req("name")
	}
	return s.
		P("phone_number_id", oas.Str().Nullable().Desc("Optional one-to-one WhatsApp phone-number assignment. A chat agent can have at most one phone number, and the same phone number cannot be assigned to another chat agent.").Example("phone_number_12345")).
		P("status", oas.Str().Enum([]string{"active", "inactive"}).Default("inactive").Example("inactive")).
		Build()
}

func modelSectionSchema() oas.Object {
	return oas.Obj().Desc("Conversational model settings.").
		P("provider", oas.Str().Enum([]string{"openai", "anthropic"}).Default("openai").Example("openai")).
		P("name", oas.Str().Default("gpt-4.1-mini").Example("gpt-4.1-mini")).
		P("temperature", oas.Num().Min(0.1).Max(1).Default(0.3).Example(0.3)).
		Build()
}

func promptSectionSchema() oas.Object {
	return oas.Obj().Desc("Chat system-prompt settings.").
		P("system_prompt", oas.Str().Example("You are a helpful WhatsApp support assistant.")).
		Build()
}

func knowledgeBaseSectionSchema() oas.Object {
	return oas.Obj().Desc("Knowledge bases available to this chat agent.").
		P("knowledge_base_ids", oas.Arr(oas.Str().Example("knowledge_base_12345")).Default([]string{})).
		Build()
}

func toolsSectionSchema() oas.Object {
	return oas.Obj().Desc("Tools available to this chat agent. Only api_request and send_text tools apply to a chat; "+
		"end_call and transfer_call exist to release a phone call and are ignored here.").
		P("tool_ids", oas.Arr(oas.Str().Example("tool_12345")).Default([]string{})).
		Build()
}

func createChatAgentRequestSchema() oas.Object {
	return oas.Obj().Desc("Configuration for a new chat agent. Only agent.name is required; every other field and section is optional.").Req("agent").
		P("agent", agentSectionSchema(true)).
		P("model", modelSectionSchema()).
		P("prompt", promptSectionSchema()).
		P("knowledge_base", knowledgeBaseSectionSchema()).
		P("tools", toolsSectionSchema()).
		Build()
}

func updateChatAgentRequestSchema() oas.Object {
	return oas.Obj().Desc("Partial chat-agent configuration. Send only fields that should change.").
		P("agent", agentSectionSchema(false)).
		P("model", modelSectionSchema()).
		P("prompt", promptSectionSchema()).
		P("knowledge_base", knowledgeBaseSectionSchema()).
		P("tools", toolsSectionSchema()).
		Build()
}

func chatAgentResourceSchema() oas.Object {
	return oas.Obj().Desc("Persisted chat-agent resource.").Req("id", "created_at", "updated_at", "agent").
		P("id", oas.Str().Example("chat_agent_12345")).
		P("created_at", oas.Str().Format("date-time").Example("2026-08-29T10:00:00Z")).
		P("updated_at", oas.Str().Format("date-time").Example("2026-08-29T10:00:00Z")).
		P("agent", agentSectionSchema(true)).
		P("model", modelSectionSchema()).
		P("prompt", promptSectionSchema()).
		P("knowledge_base", knowledgeBaseSectionSchema()).
		P("tools", toolsSectionSchema()).
		Build()
}

func chatAgentResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Chat agent retrieved successfully")).
		P("data", oas.Ref("ChatAgentResource")).
		Build()
}

func listChatAgentsResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Chat agents retrieved successfully")).
		P("data", oas.Arr(oas.Ref("ChatAgentResource"))).
		P("meta", oas.Obj().P("page", oas.Int().Example(1)).P("limit", oas.Int().Example(20)).P("total_items", oas.Int().Example(1))).
		Build()
}

func deleteChatAgentResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Chat agent deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").P("id", oas.Str().Example("chat_agent_12345")).P("deleted", oas.Bool().Example(true))).
		Build()
}

func createRequestExample() oas.Object {
	return oas.Object{
		"agent": oas.Object{"name": "Support Chat"},
	}
}

func resourceExample() oas.Object {
	return oas.Object{
		"id":         "chat_agent_12345",
		"created_at": "2026-08-29T10:00:00Z",
		"updated_at": "2026-08-29T10:00:00Z",
		"agent": oas.Object{
			"name":            "Support Chat",
			"phone_number_id": "phone_number_12345",
			"status":          "inactive",
		},
		"model": oas.Object{"provider": "openai", "name": "gpt-4.1-mini", "temperature": 0.3},
		"prompt": oas.Object{
			"system_prompt": "You are a helpful WhatsApp support assistant.",
		},
		"knowledge_base": oas.Object{"knowledge_base_ids": []any{"knowledge_base_12345"}},
		"tools":          oas.Object{"tool_ids": []any{"tool_12345"}},
	}
}

func responseExample(message string) oas.Object {
	return oas.Object{"success": true, "message": message, "data": resourceExample()}
}

func listResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Chat agents retrieved successfully",
		"data":    []any{resourceExample()},
		"meta":    oas.Object{"page": 1, "limit": 20, "total_items": 1},
	}
}
