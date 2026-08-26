package agents

import (
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/examples"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// ---------------------------------------------------------------------------
// Response / request / parameter helpers.
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

func okJSONNamedExamples(desc, schemaName string, named oas.Object) oas.Object {
	return oas.Object{
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":   oas.Ref(schemaName),
				"examples": named,
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
	return errJSON("Unauthorized - missing or invalid API key bearer token.", examples.UnauthorizedErrorExample)
}

func resp404() oas.Object {
	return errJSON("Not found — no agent exists with the given id, or a referenced phone number, knowledge base or tool does not exist.", examples.NotFoundErrorExample)
}

func resp409() oas.Object {
	return errJSON("Conflict — the agent name, the phone-number direction assignment, or a knowledge base or tool "+
		"the request attaches is already in use by another agent.", examples.ConflictErrorExample)
}

func resp422() oas.Object {
	return errJSON("Unprocessable entity — the request is well-formed but semantically invalid (e.g. voice_id not available for the selected provider).", examples.UnprocessableErrorExample)
}

func resp500() oas.Object {
	return errJSON("Internal server error.", examples.InternalErrorExample)
}

// resp429 carries the Retry-After header documented for rate limiting.
func resp429() oas.Object {
	r := errJSON("Too many requests — the client is being rate limited.", examples.TooManyRequestsErrorExample)
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

func agentIDParam() oas.Object {
	return oas.Object{
		"name":        "agent_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the agent.",
		"schema":      oas.Object{"type": "string", "example": "agent_12345"},
	}
}

// apiKeySecurity marks an operation as requiring the API-key-only bearer
// security scheme registered in the document's components.
func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
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

func fieldsParam() oas.Object {
	return oas.Object{
		"name":        "fields",
		"in":          "query",
		"required":    false,
		"description": "Sparse fieldset. Comma-separated list of fields to include. Use dot-notation (e.g. `agent.language`) to select a nested property of a section. Omit this parameter to return the full resource.",
		"style":       "form",
		"explode":     false,
		"schema":      oas.Arr(oas.Str().Enum(constants.SelectableFieldsOptions)).Build(),
	}
}

// ---------------------------------------------------------------------------
// Operations.
// ---------------------------------------------------------------------------

func listAgentsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listAgents",
		"summary":     "Get All Agents",
		"description": "Returns a paginated list of the agents owned by the authenticated API key owner. Supports sparse fieldsets via the `fields` query parameter.",
		"security":    apiKeySecurity(),
		"parameters":  []any{pageParam(), limitParam(), fieldsParam()},
		"responses": oas.Object{
			"200": okJSONNamedExamples("A page of agents.", "ListAgentsResponse", oas.Object{
				"allFields": oas.Object{
					"summary": "Full resources (no fields filter)",
					"value":   examples.ListAgentsResponseExample,
				},
				"selectedFields": oas.Object{
					"summary": "Sparse fieldset: id, agent.status, agent.language, agent.call_direction",
					"value":   examples.ListAgentsSelectedFieldsExample,
				},
			}),
			"400": errJSON("Invalid query parameters (page, limit or fields).", examples.ListQueryValidationErrorExample),
			"401": resp401(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func createAgentOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "createAgent",
		"summary":     "Create Agent",
		"description": "Creates a new outbound agent owned by the authenticated API key owner from the supplied configuration. If `agent.phone_number_id` is set, the phone number must belong to the user and must not already be assigned to another agent. Every id in `knowledge_base.knowledge_base_ids` must reference a knowledge base owned by the same user, and every id in `tools.tool_ids` a tool owned by the same user.",
		"security":    apiKeySecurity(),
		"requestBody": requestBody("The agent configuration to create.", "CreateAgentRequest", examples.CreateAgentRequestExample),
		"responses": oas.Object{
			"201": okJSON("The created agent resource.", "CreateAgentResponse", examples.CreateAgentResponseExample),
			"400": errJSON("Validation failed for the request body.", examples.ValidationErrorExample),
			"401": resp401(),
			"404": resp404(),
			"409": resp409(),
			"422": resp422(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func getAgentOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "getAgent",
		"summary":     "Get Agent",
		"description": "Returns a single agent by id, scoped to the authenticated API key owner.",
		"security":    apiKeySecurity(),
		"parameters":  []any{agentIDParam()},
		"responses": oas.Object{
			"200": okJSON("The requested agent resource.", "GetAgentResponse", examples.GetAgentResponseExample),
			"400": errJSON("Invalid agent_id path parameter.", examples.AgentIDValidationErrorExample),
			"401": resp401(),
			"404": resp404(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func updateAgentOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "updateAgent",
		"summary":     "Update Agent",
		"description": "Applies a partial update to an existing outbound agent owned by the authenticated API key owner. Only the supplied fields are changed. Assigning `agent.phone_number_id` is rejected (409) when another agent already uses that phone number. Sending `knowledge_base.knowledge_base_ids` or `tools.tool_ids` replaces that attachment set wholesale; omitting a section leaves it untouched.",
		"security":    apiKeySecurity(),
		"parameters":  []any{agentIDParam()},
		"requestBody": requestBody("The fields to change.", "UpdateAgentRequest", examples.UpdateAgentRequestExample),
		"responses": oas.Object{
			"200": okJSON("The updated agent resource.", "GetAgentResponse", examples.UpdateAgentResponseExample),
			"400": errJSON("Validation failed for the request body.", examples.ValidationErrorExample),
			"401": resp401(),
			"404": resp404(),
			"409": resp409(),
			"422": resp422(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func deleteAgentOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "deleteAgent",
		"summary":     "Delete Agent",
		"description": "Deletes an agent by id, scoped to the authenticated API key owner. A knowledge base or " +
			"tool belongs to the agent that attached it, so this deletes them too — the knowledge bases with " +
			"their sources and every vector indexed under their namespaces, and the tools with their " +
			"configuration. Detach anything worth keeping first, by removing its id from the agent's " +
			"`knowledge_base.knowledge_base_ids` or `tools.tool_ids`. Any phone number assigned to the agent is " +
			"released rather than deleted.",
		"security":    apiKeySecurity(),
		"parameters":  []any{agentIDParam()},
		"responses": oas.Object{
			"200": okJSON("The agent was deleted.", "DeleteAgentResponse", examples.DeleteAgentResponseExample),
			"400": errJSON("Invalid agent_id path parameter.", examples.AgentIDValidationErrorExample),
			"401": resp401(),
			"404": resp404(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

// agentPaths returns the path-item map to merge into document.paths.
func agentPaths() oas.Object {
	collection := oas.APIV1Prefix + "/agents"
	item := oas.APIV1Prefix + "/agents/{agent_id}"
	return oas.Object{
		collection: oas.Object{
			"get":  listAgentsOperation(),
			"post": createAgentOperation(),
		},
		item: oas.Object{
			"get":    getAgentOperation(),
			"patch":  updateAgentOperation(),
			"delete": deleteAgentOperation(),
		},
	}
}
