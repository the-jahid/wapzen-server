package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

// ---------------------------------------------------------------------------
// Response / request / parameter helpers — mirrors the Knowledge Base module.
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

// okJSONNamedExamples is okJSON for a response that has more than one shape
// worth showing — the same 201 whose call either happened or did not.
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
	return errJSON("Unauthorized - missing or invalid API key bearer token.", unauthorizedErrorExample())
}

func resp404() oas.Object {
	return errJSON("Not found — no campaign exists with the given id.", notFoundErrorExample())
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

func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
}

func campaignIDParam() oas.Object {
	return oas.Object{
		"name":        "campaign_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the outbound campaign.",
		"schema":      oas.Object{"type": "string", "example": "campaign_a456426614174000"},
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

// statusParam narrows a listing to one lifecycle state, which is how a
// dashboard shows only the campaigns currently on the air.
func statusParam() oas.Object {
	return oas.Object{
		"name":        "status",
		"in":          "query",
		"required":    false,
		"description": "Return only campaigns in this status. Omit for all statuses.",
		"schema":      oas.Str().Enum(campaignStatusOptions).Example("running").Build(),
	}
}

// ---------------------------------------------------------------------------
// Operations.
// ---------------------------------------------------------------------------

func listOutboundCampaignsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listOutboundCampaigns",
		"summary":     "List Outbound Campaigns",
		"description": "Returns a paginated list of the outbound campaigns owned by the authenticated API key owner, newest first, each with its current counters.",
		"security":    apiKeySecurity(),
		"parameters":  []any{pageParam(), limitParam(), statusParam()},
		"responses": oas.Object{
			"200": okJSON("A page of outbound campaigns.", "ListOutboundCampaignsResponse", listOutboundCampaignsResponseExample()),
			"400": errJSON("Invalid query parameters (page, limit or status).", listQueryValidationErrorExample()),
			"401": resp401(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func createOutboundCampaignOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "createOutboundCampaign",
		"summary":     "Create Outbound Campaign",
		"description": "Creates an outbound campaign owned by the authenticated API key owner. `campaign_name` is the only required field and must be unique among the owner's campaigns. The campaign is created at status `draft` with every counter at zero and no leads; omitting `budget_usd` leaves it uncapped. `agent_id` is optional here and may be added later, but a campaign cannot be started without one — it must name one of the owner's agents that already has a phone number assigned, and the campaign dials from that agent's number rather than from a number of its own. Nothing is dialled by this call.",
		"security":    apiKeySecurity(),
		"requestBody": requestBodyNamedExamples("The campaign to create. Only `campaign_name` is required.", "CreateOutboundCampaignRequest", oas.Object{
			"minimal": oas.Object{
				"summary": "Minimal: only the required campaign_name",
				"value":   createCampaignMinimalRequestExample(),
			},
			"withBudget": oas.Object{
				"summary": "With an agent and a spend cap",
				"value":   createCampaignRequestExample(),
			},
		}),
		"responses": oas.Object{
			"201": okJSON("The created campaign resource.", "CreateOutboundCampaignResponse", createOutboundCampaignResponseExample()),
			"400": errJSON("Validation failed for the request body.", validationErrorExample()),
			"401": resp401(),
			"409": errJSON("Conflict — a campaign with that name already exists.", nameConflictErrorExample()),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func getOutboundCampaignOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "getOutboundCampaign",
		"summary":     "Get Outbound Campaign",
		"description": "Returns a single outbound campaign by id, scoped to the authenticated API key owner, with its counters and the `pickup_rate` and `success_rate` derived from them. Both rates are 0 on a campaign that has placed no calls rather than undefined.",
		"security":    apiKeySecurity(),
		"parameters":  []any{campaignIDParam()},
		"responses": oas.Object{
			"200": okJSON("The requested campaign resource.", "GetOutboundCampaignResponse", getOutboundCampaignResponseExample()),
			"400": errJSON("Invalid campaign_id path parameter.", campaignIDValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func updateOutboundCampaignOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "updateOutboundCampaign",
		"summary":     "Update Outbound Campaign",
		"description": "Applies a partial update to a campaign owned by the authenticated API key owner. Only `campaign_name`, `status`, `budget_usd` and `agent_id` may be changed; at least one field must be supplied and omitted fields keep their stored value. Send `budget_usd: null` to remove a spend cap, or `agent_id: null` to detach the agent — omitting either field keeps the stored value instead. `agent_id` must name one of the owner's agents that has a phone number assigned; `phone_number_id` and `phone_number` are read back from that agent and are rejected if supplied. Status is how a campaign is started, paused and resumed: `draft` and `paused` may move to `running`, `running` may move to `paused` or `completed`, and the terminal statuses `completed` and `failed` may not move at all. A `running` campaign must have an agent, so starting one without an agent — or detaching the agent of a running one — is refused with 409. The counters are maintained by the server and are rejected if supplied.",
		"security":    apiKeySecurity(),
		"parameters":  []any{campaignIDParam()},
		"requestBody": requestBodyNamedExamples("The fields to change. At least one is required.", "UpdateOutboundCampaignRequest", oas.Object{
			"assignAgent": oas.Object{
				"summary": "Choose the agent that dials (its number comes with it)",
				"value":   assignCampaignAgentRequestExample(),
			},
			"start": oas.Object{
				"summary": "Put the campaign on the air",
				"value":   startCampaignRequestExample(),
			},
			"raiseBudget": oas.Object{
				"summary": "Raise the spend cap",
				"value":   updateCampaignBudgetRequestExample(),
			},
			"clearBudget": oas.Object{
				"summary": "Remove the spend cap (uncapped)",
				"value":   clearCampaignBudgetRequestExample(),
			},
		}),
		"responses": oas.Object{
			"200": okJSON("The updated campaign resource.", "UpdateOutboundCampaignResponse", updateOutboundCampaignResponseExample()),
			"400": errJSON("Validation failed for the request body.", updateValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"409": errJSON("Conflict — the campaign name is taken, the status transition is not allowed, or the campaign would be left running without an agent.", statusConflictErrorExample()),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

func deleteOutboundCampaignOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "deleteOutboundCampaign",
		"summary":     "Delete Outbound Campaign",
		"description": "Deletes a campaign by id, scoped to the authenticated API key owner, together with its counters. A running campaign is refused rather than deleted mid-flight: pause it first. Calls the campaign has already placed are not deleted — they remain in the call history.",
		"security":    apiKeySecurity(),
		"parameters":  []any{campaignIDParam()},
		"responses": oas.Object{
			"200": okJSON("The campaign was deleted.", "DeleteOutboundCampaignResponse", deleteOutboundCampaignResponseExample()),
			"400": errJSON("Invalid campaign_id path parameter.", campaignIDValidationErrorExample()),
			"401": resp401(),
			"404": resp404(),
			"409": errJSON("Conflict — the campaign is running and cannot be deleted.", deleteRunningConflictErrorExample()),
			"429": resp429(),
			"500": resp500(),
		},
	}
}

// outboundCampaignPaths returns the path-item map to merge into document.paths.
func outboundCampaignPaths() oas.Object {
	collection := oas.APIV1Prefix + "/outbound-campaigns"
	item := collection + "/{campaign_id}"
	return oas.Object{
		collection: oas.Object{
			"get":  listOutboundCampaignsOperation(),
			"post": createOutboundCampaignOperation(),
		},
		item: oas.Object{
			"get":    getOutboundCampaignOperation(),
			"patch":  updateOutboundCampaignOperation(),
			"delete": deleteOutboundCampaignOperation(),
		},
	}
}
