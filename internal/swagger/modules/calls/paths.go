package calls

import "whatsapp-ai-caller-server/internal/swagger/oas"

func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
}

func callIDParam() oas.Object {
	return oas.Object{
		"name":        "call_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the call.",
		"schema":      oas.Object{"type": "string"},
	}
}

func response(desc string) oas.Object {
	return oas.Object{"description": desc}
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
		"description": "Number of calls per page (1–200, default 50).",
		"schema":      oas.Int().Min(1).Max(200).Default(50).Build(),
	}
}

func listCallsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listCalls",
		"summary":     "List Calls",
		"description": "Returns a paginated list of the calls handled for the authenticated API key owner, newest first.",
		"security":    apiKeySecurity(),
		"parameters":  []any{pageParam(), limitParam()},
		"responses": oas.Object{
			"200": okJSON("A page of calls.", "CallListResponse", callListResponseExample()),
			"400": response("Invalid query parameters (page or limit)."),
			"401": response("Unauthorized - missing or invalid API key bearer token."),
			"500": response("Internal Server Error"),
		},
	}
}

func createCallOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "createCall",
		"summary":     "Create Call",
		"description": "Places a new outbound WhatsApp call from one of the authenticated API key owner's connected " +
			"phone numbers. The agent that speaks is the one assigned to that number, so only the number and the " +
			"destination are required. It returns as soon as the callee's phone is ringing: the call comes back in " +
			"the \"received\" status and advances to \"answered\" when they pick up — or \"declined\" if they cut it " +
			"while it rings — so poll Get Call (or read the transcript once it has ended) to follow it.",
		"security":    apiKeySecurity(),
		"requestBody": requestBody("The call to place.", "CreateCallRequest", createCallRequestExample()),
		"responses": oas.Object{
			"201": okJSON("The call was placed and is ringing.", "CreateCallResponse", createCallResponseExample()),
			"400": response("Invalid request body — phone_number_id or to is missing or malformed, or agent_id names an agent that is not assigned to this phone number."),
			"401": response("Unauthorized - missing or invalid API key bearer token."),
			"404": response("No such phone number for this user."),
			"409": response("The phone number is not connected, has no call handler attached, or has no active outbound-capable agent assigned."),
			"500": response("Internal Server Error"),
			"502": response("WhatsApp refused the call — for example the destination is unreachable or not on WhatsApp."),
			"503": response("Voice calling is not configured on this server, or the agent's selected providers have no credentials."),
		},
	}
}

func getCallOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "getCall",
		"summary":     "Get Call",
		"description": "Returns a single call owned by the authenticated user, including its saved AI/user conversation transcript.",
		"security":    apiKeySecurity(),
		"parameters":  []any{callIDParam()},
		"responses": oas.Object{
			"200": okJSON("The call, including its transcript.", "CallResponse", callResponseExample()),
			"401": response("Unauthorized - missing or invalid API key bearer token."),
			"404": response("Not Found"),
			"500": response("Internal Server Error"),
		},
	}
}

func updateCallOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "updateCall",
		"summary":     "Update Call",
		"description": "Applies a partial update to a call. Only status and end_reason may be changed; at least one must be supplied.",
		"security":    apiKeySecurity(),
		"parameters":  []any{callIDParam()},
		"requestBody": requestBody("The call fields to change.", "UpdateCallRequest", updateCallRequestExample()),
		"responses": oas.Object{
			"200": okJSON("The updated call.", "CallResponse", updateCallResponseExample()),
			"400": response("Bad Request"),
			"401": response("Unauthorized - missing or invalid API key bearer token."),
			"404": response("Not Found"),
			"500": response("Internal Server Error"),
		},
	}
}

func deleteCallOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "deleteCall",
		"summary":     "Delete Call",
		"description": "Deletes a call owned by the authenticated user. Its saved transcript is removed with it.",
		"security":    apiKeySecurity(),
		"parameters":  []any{callIDParam()},
		"responses": oas.Object{
			"200": okJSON("The call was deleted.", "CallDeleteResponse", deleteCallResponseExample()),
			"401": response("Unauthorized - missing or invalid API key bearer token."),
			"404": response("Not Found"),
			"500": response("Internal Server Error"),
		},
	}
}

func callPaths() oas.Object {
	collection := oas.APIV1Prefix + "/calls"
	item := oas.APIV1Prefix + "/calls/{call_id}"
	return oas.Object{
		collection: oas.Object{
			"get":  listCallsOperation(),
			"post": createCallOperation(),
		},
		item: oas.Object{
			"get":    getCallOperation(),
			"delete": deleteCallOperation(),
			"patch":  updateCallOperation(),
		},
	}
}
