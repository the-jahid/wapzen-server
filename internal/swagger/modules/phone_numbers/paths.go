package phone_numbers

import "whatsapp-ai-caller-server/internal/swagger/oas"

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

func errJSON(desc string) oas.Object {
	return oas.Object{
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema": oas.Ref("APIResponse"),
			},
		},
	}
}

func requestBody(desc, schemaName string, example any) oas.Object {
	return requestBodyWithRequired(desc, schemaName, example, true)
}

func optionalRequestBody(desc, schemaName string, example any) oas.Object {
	return requestBodyWithRequired(desc, schemaName, example, false)
}

func requestBodyWithRequired(desc, schemaName string, example any, required bool) oas.Object {
	return oas.Object{
		"required":    required,
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema":  oas.Ref(schemaName),
				"example": example,
			},
		},
	}
}

func phoneNumberIDParam() oas.Object {
	return oas.Object{
		"name":        "phone_number_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the phone number.",
		"schema":      oas.Object{"type": "string", "example": "phone_number_12345"},
	}
}

// apiKeySecurity marks phone-number operations as authenticated by generated
// API keys in Swagger UI. The route middleware also accepts Clerk sessions for
// dashboard compatibility, but public docs should guide callers to API keys.
func apiKeySecurity() []any {
	return []any{oas.Object{"apiKeyBearer": []any{}}}
}

func loginPhoneNumberOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "loginPhoneNumber",
		"summary":     "Start WhatsApp QR login",
		"description": "Starts a WhatsApp Linked Devices QR login. With phone_number_id and force_repair, it replaces a connected companion while preserving the phone-number resource and agent assignment. With agent_id, the number the scan links is assigned to that agent as soon as it connects.",
		"security":    apiKeySecurity(),
		"requestBody": optionalRequestBody("Optional phone number metadata for the login.", "LoginPhoneNumberRequest", loginPhoneNumberRequestExample()),
		"responses": oas.Object{
			"200": okJSON("The pending phone number login resource.", "LoginPhoneNumberResponse", phoneNumberResponseExample("Phone number login started successfully")),
			"400": errJSON("Bad Request"),
			"401": errJSON("Unauthorized - missing or invalid API key bearer token."),
			"409": errJSON("Conflict - phone number already exists."),
			"500": errJSON("Internal Server Error"),
		},
	}
}

func logoutPhoneNumberOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "logoutPhoneNumber",
		"summary":     "Logout",
		"description": "Disconnects a WhatsApp phone number for the authenticated API key owner and deletes its local database record.",
		"security":    apiKeySecurity(),
		"requestBody": requestBody("The phone number to log out.", "LogoutPhoneNumberRequest", logoutPhoneNumberRequestExample()),
		"responses": oas.Object{
			"200": okJSON("The phone number was disconnected and deleted.", "LogoutPhoneNumberResponse", logoutPhoneNumberResponseExample()),
			"400": errJSON("Bad Request"),
			"401": errJSON("Unauthorized - missing or invalid API key bearer token."),
			"404": errJSON("Not Found"),
			"500": errJSON("Internal Server Error"),
		},
	}
}

func listPhoneNumbersOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listPhoneNumbers",
		"summary":     "Get phone numbers list",
		"description": "Returns the phone numbers owned by the authenticated API key owner.",
		"security":    apiKeySecurity(),
		"responses": oas.Object{
			"200": okJSON("The phone number list.", "ListPhoneNumbersResponse", listPhoneNumbersResponseExample()),
			"401": errJSON("Unauthorized - missing or invalid API key bearer token."),
			"500": errJSON("Internal Server Error"),
		},
	}
}

func getPhoneNumberOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "getPhoneNumber",
		"summary":     "Get one phone number",
		"description": "Returns a single phone number by id, scoped to the authenticated API key owner.",
		"security":    apiKeySecurity(),
		"parameters":  []any{phoneNumberIDParam()},
		"responses": oas.Object{
			"200": okJSON("The requested phone number resource.", "GetPhoneNumberResponse", phoneNumberResponseExample("Phone number retrieved successfully")),
			"400": errJSON("Invalid phone_number_id path parameter."),
			"401": errJSON("Unauthorized - missing or invalid API key bearer token."),
			"404": errJSON("Not Found"),
			"500": errJSON("Internal Server Error"),
		},
	}
}

func phoneNumberPaths() oas.Object {
	collection := oas.APIV1Prefix + "/phone-number"
	login := collection + "/login"
	logout := collection + "/logout"
	item := oas.APIV1Prefix + "/phone-number/{phone_number_id}"
	return oas.Object{
		collection: oas.Object{
			"get": listPhoneNumbersOperation(),
		},
		login: oas.Object{
			"post": loginPhoneNumberOperation(),
		},
		logout: oas.Object{
			"post": logoutPhoneNumberOperation(),
		},
		item: oas.Object{
			"get": getPhoneNumberOperation(),
		},
	}
}
