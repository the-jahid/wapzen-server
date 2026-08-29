// Package swagger assembles the OpenAPI 3 document served to Swagger UI.
//
// The project's real endpoints are annotated for swaggo (which emits Swagger
// 2.0). The static "Agents" documentation, however, needs OpenAPI 3 features
// (a bearer security scheme, requestBody, components, multiple named examples,
// style/explode query params). To keep a single Swagger UI while honoring that
// contract, this package builds one OpenAPI 3.0.3 document — porting the
// existing /health and /api/webhooks/clerk endpoints so nothing regresses — and
// then applies the per-module document mutators. The result is served at
// /swagger/doc.json, overriding swaggo's served spec.
package swagger

import (
	"whatsapp-ai-caller-server/internal/swagger/modules/agents"
	"whatsapp-ai-caller-server/internal/swagger/modules/calls"
	"whatsapp-ai-caller-server/internal/swagger/modules/chat_agents"
	"whatsapp-ai-caller-server/internal/swagger/modules/knowledge_base"
	"whatsapp-ai-caller-server/internal/swagger/modules/outbound_campaigns"
	"whatsapp-ai-caller-server/internal/swagger/modules/phone_numbers"
	"whatsapp-ai-caller-server/internal/swagger/modules/tools"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// BuildDocument assembles the base document and applies every static-doc
// mutator. This is the analogue of NestJS `SwaggerModule.createDocument(...)`
// followed by `injectStaticAgentDocs(document)`.
func BuildDocument() oas.OpenAPIObject {
	doc := oas.OpenAPIObject{
		"openapi": "3.0.3",
		"info": oas.Object{
			"title":       "WhatsApp AI Caller API",
			"description": "Backend API foundation for the WhatsApp AI Voice Caller application.",
			"version":     "1.0",
		},
		"servers": []any{
			oas.Object{"url": "http://localhost:8080"},
		},
		"tags": []any{
			oas.Object{"name": "health", "description": "Service health checks."},
			oas.Object{"name": "webhooks", "description": "Inbound third-party webhooks."},
			oas.Object{"name": "users", "description": "The authenticated user's identity."},
			oas.Object{"name": "apiKeys", "description": "User-owned bearer API keys."},
			oas.Object{"name": voicesTagName, "description": "ElevenLabs voice catalogue used to pick an agent voice."},
		},
		"paths": oas.Object{
			"/health": oas.Object{
				"get": healthOperation(),
			},
			"/api/webhooks/clerk": oas.Object{
				"post": clerkWebhookOperation(),
			},
			"/v1/users/me": oas.Object{
				"get": currentUserOperation(),
			},
			"/v1/api-keys": oas.Object{
				"get":  listAPIKeysOperation(),
				"post": createAPIKeyOperation(),
			},
			"/v1/api-keys/{api_key_id}/default": oas.Object{
				"patch": setDefaultAPIKeyOperation(),
			},
			"/v1/api-keys/{api_key_id}": oas.Object{
				"delete": revokeAPIKeyOperation(),
			},
		},
		"components": oas.Object{
			// .addBearerAuth() analogue: every Agents operation references this
			// `bearer` scheme via `security: [{ bearer: [] }]`.
			"securitySchemes": oas.Object{
				"bearer": oas.Object{
					"type":        "http",
					"scheme":      "bearer",
					"description": "Bearer authentication. Send either a Clerk session token or a generated API key as `Authorization: Bearer <token>`.",
				},
				"apiKeyBearer": oas.Object{
					"type":        "http",
					"scheme":      "bearer",
					"description": "Generated API key authentication for live API routes. Send `Authorization: Bearer <wcai_...>`.",
				},
			},
			"schemas": oas.Object{
				"APIResponse": oas.Obj().
					Desc("Standard JSON envelope returned by the foundation endpoints.").
					P("success", oas.Bool().Example(true)).
					P("message", oas.Str().Example("Server is running")).
					P("data", oas.Object{}).
					Build(),
				"APIKey":                   apiKeySchema(),
				"CreatedAPIKey":            createdAPIKeySchema(),
				"CreateAPIKeyRequest":      createAPIKeyRequestSchema(),
				"ListAPIKeysResponse":      listAPIKeysResponseSchema(),
				"CreateAPIKeyResponse":     createAPIKeyResponseSchema(),
				"SetDefaultAPIKeyResponse": setDefaultAPIKeyResponseSchema(),
				"RevokeAPIKeyResponse":     revokeAPIKeyResponseSchema(),
			},
		},
	}

	// Voice discovery lives alongside the base endpoints (see voices.go).
	for path, item := range voicePaths() {
		doc["paths"].(oas.Object)[path] = item
	}
	for name, schema := range voiceComponentSchemas() {
		doc["components"].(oas.Object)["schemas"].(oas.Object)[name] = schema
	}

	// Apply the static-doc module mutators.
	agents.InjectStaticAgentDocs(doc)
	chat_agents.InjectStaticChatAgentDocs(doc)
	calls.InjectStaticCallDocs(doc)
	phone_numbers.InjectStaticPhoneNumberDocs(doc)
	knowledge_base.InjectStaticKnowledgeBaseDocs(doc)
	outbound_campaigns.InjectStaticOutboundCampaignDocs(doc)
	tools.InjectStaticToolDocs(doc)

	return doc
}

// healthOperation ports the swaggo-annotated GET /health endpoint to OpenAPI 3.
func healthOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"health"},
		"summary":     "Health check",
		"description": "Returns the current status of the server.",
		"responses": oas.Object{
			"200": jsonResponse("OK", "APIResponse"),
		},
	}
}

// currentUserOperation documents GET /v1/users/me: the authenticated user's
// application record, resolved from the Clerk bearer token or API key by the
// auth middleware.
func currentUserOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"users"},
		"summary":     "Get current user",
		"description": "Returns the application user for the authenticated Clerk session or API key, including the internal user id.",
		"security":    []any{oas.Object{"bearer": []any{}}},
		"responses": oas.Object{
			"200": oas.Object{
				"description": "The authenticated user.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema": oas.Ref("APIResponse"),
						"example": oas.Object{
							"success": true,
							"message": "User retrieved successfully",
							"data": oas.Object{
								"id":        "9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4",
								"email":     "user@example.com",
								"oauthId":   "user_2abc123",
								"username":  "janedoe",
								"createdAt": "2026-06-30T10:00:00Z",
								"updatedAt": "2026-06-30T10:00:00Z",
							},
						},
					},
				},
			},
			"401": jsonResponse("Unauthorized — missing or invalid bearer token.", "APIResponse"),
		},
	}
}

func listAPIKeysOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"apiKeys"},
		"operationId": "listAPIKeys",
		"summary":     "List API keys",
		"description": "Returns metadata for the authenticated user's active API keys. Secrets are never returned after creation.",
		"security":    oas.BearerSecurity(),
		"responses": oas.Object{
			"200": oas.Object{
				"description": "Active API keys.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema":  oas.Ref("ListAPIKeysResponse"),
						"example": listAPIKeysExample(),
					},
				},
			},
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"500": jsonResponse("Internal Server Error", "APIResponse"),
		},
	}
}

func createAPIKeyOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"apiKeys"},
		"operationId": "createAPIKey",
		"summary":     "Create API key",
		"description": "Creates a new API key for the authenticated user and returns the bearer secret once. New keys are not default until selected. Store the key securely; future list responses only include metadata.",
		"security":    oas.BearerSecurity(),
		"requestBody": oas.Object{
			"required": false,
			"content": oas.Object{
				"application/json": oas.Object{
					"schema":  oas.Ref("CreateAPIKeyRequest"),
					"example": oas.Object{"name": "Production"},
				},
			},
		},
		"responses": oas.Object{
			"201": oas.Object{
				"description": "The created API key. The key field is returned only once.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema":  oas.Ref("CreateAPIKeyResponse"),
						"example": createAPIKeyExample(),
					},
				},
			},
			"400": jsonResponse("Bad Request", "APIResponse"),
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"500": jsonResponse("Internal Server Error", "APIResponse"),
		},
	}
}

func setDefaultAPIKeyOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"apiKeys"},
		"operationId": "setDefaultAPIKey",
		"summary":     "Set default API key",
		"description": "Marks an active API key owned by the authenticated user as the default API key. Exactly one active API key is default per user.",
		"security":    oas.BearerSecurity(),
		"parameters": []any{
			oas.Object{
				"name":        "api_key_id",
				"in":          "path",
				"required":    true,
				"description": "Unique identifier of the API key to make default.",
				"schema":      oas.Str().Example("8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f").Build(),
			},
		},
		"responses": oas.Object{
			"200": oas.Object{
				"description": "The selected API key is now default.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema":  oas.Ref("SetDefaultAPIKeyResponse"),
						"example": setDefaultAPIKeyExample(),
					},
				},
			},
			"400": jsonResponse("Bad Request", "APIResponse"),
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"404": jsonResponse("Not Found", "APIResponse"),
			"500": jsonResponse("Internal Server Error", "APIResponse"),
		},
	}
}

func revokeAPIKeyOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"apiKeys"},
		"operationId": "revokeAPIKey",
		"summary":     "Delete API key",
		"description": "Deletes an API key owned by the authenticated user. Deleted keys can no longer be used for bearer authentication. A user cannot delete their only remaining active API key; if the deleted key was default, another active key becomes default.",
		"security":    oas.BearerSecurity(),
		"parameters": []any{
			oas.Object{
				"name":        "api_key_id",
				"in":          "path",
				"required":    true,
				"description": "Unique identifier of the API key.",
				"schema":      oas.Str().Example("8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f").Build(),
			},
		},
		"responses": oas.Object{
			"200": oas.Object{
				"description": "The API key was deleted.",
				"content": oas.Object{
					"application/json": oas.Object{
						"schema":  oas.Ref("RevokeAPIKeyResponse"),
						"example": revokeAPIKeyExample(),
					},
				},
			},
			"400": jsonResponse("Bad Request", "APIResponse"),
			"401": jsonResponse("Unauthorized - missing or invalid bearer token.", "APIResponse"),
			"404": jsonResponse("Not Found", "APIResponse"),
			"409": jsonResponse("Conflict - cannot delete the last active API key.", "APIResponse"),
			"500": jsonResponse("Internal Server Error", "APIResponse"),
		},
	}
}

// clerkWebhookOperation ports the swaggo-annotated POST /api/webhooks/clerk
// endpoint to OpenAPI 3.
func clerkWebhookOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"webhooks"},
		"summary":     "Clerk webhook",
		"description": "Verifies Svix headers and syncs Clerk user events into Postgres.",
		"responses": oas.Object{
			"200": jsonResponse("OK", "APIResponse"),
			"400": jsonResponse("Bad Request", "APIResponse"),
			"500": jsonResponse("Internal Server Error", "APIResponse"),
		},
	}
}

func jsonResponse(desc, schemaName string) oas.Object {
	return oas.Object{
		"description": desc,
		"content": oas.Object{
			"application/json": oas.Object{
				"schema": oas.Ref(schemaName),
			},
		},
	}
}

func apiKeySchema() oas.Object {
	return oas.Obj().
		Desc("Public metadata for a user-owned API key. The secret value is not stored or returned after creation.").
		P("id", oas.Str().Example("8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f")).
		P("name", oas.Str().Example("Production")).
		P("isDefault", oas.Bool().Example(true)).
		P("keyPrefix", oas.Str().Example("wcai_Bv6f")).
		P("last4", oas.Str().Example("9xQ2")).
		P("lastUsedAt", oas.Str().Format("date-time").Nullable().Example("2026-07-02T10:00:00Z")).
		P("createdAt", oas.Str().Format("date-time").Example("2026-07-02T10:00:00Z")).
		P("updatedAt", oas.Str().Format("date-time").Example("2026-07-02T10:00:00Z")).
		Req("id", "name", "isDefault", "keyPrefix", "last4", "createdAt", "updatedAt").
		Build()
}

func createdAPIKeySchema() oas.Object {
	return oas.Obj().
		Desc("API key metadata plus the one-time bearer secret. The key field is never returned again.").
		P("id", oas.Str().Example("8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f")).
		P("name", oas.Str().Example("Production")).
		P("isDefault", oas.Bool().Example(false)).
		P("keyPrefix", oas.Str().Example("wcai_Bv6f")).
		P("last4", oas.Str().Example("9xQ2")).
		P("lastUsedAt", oas.Str().Format("date-time").Nullable()).
		P("createdAt", oas.Str().Format("date-time").Example("2026-07-02T10:00:00Z")).
		P("updatedAt", oas.Str().Format("date-time").Example("2026-07-02T10:00:00Z")).
		P("key", oas.Str().Example("wcai_Bv6fLwL8y6bpiGvM_rJX8oBsZw8QSg6DSxQ7VhA_9xQ2")).
		Req("id", "name", "isDefault", "keyPrefix", "last4", "createdAt", "updatedAt", "key").
		Build()
}

func createAPIKeyRequestSchema() oas.Object {
	return oas.Obj().
		Desc("Optional API key creation request. If name is omitted, the server uses Default.").
		P("name", oas.Object{
			"type":        "string",
			"description": "Human-readable key label.",
			"maxLength":   80,
			"example":     "Production",
		}).
		Build()
}

func listAPIKeysResponseSchema() oas.Object {
	return oas.Obj().
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("API keys retrieved successfully")).
		P("data", oas.Arr(oas.Ref("APIKey"))).
		Req("success", "message", "data").
		Build()
}

func createAPIKeyResponseSchema() oas.Object {
	return oas.Obj().
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("API key created successfully")).
		P("data", oas.Ref("CreatedAPIKey")).
		Req("success", "message", "data").
		Build()
}

func setDefaultAPIKeyResponseSchema() oas.Object {
	return oas.Obj().
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Default API key updated successfully")).
		P("data", oas.Ref("APIKey")).
		Req("success", "message", "data").
		Build()
}

func revokeAPIKeyResponseSchema() oas.Object {
	return oas.Obj().
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("API key deleted successfully")).
		P("data", oas.Obj().
			P("id", oas.Str().Example("8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f")).
			P("revoked", oas.Bool().Example(true)).
			Req("id", "revoked")).
		Req("success", "message", "data").
		Build()
}

func apiKeyExample() oas.Object {
	return apiKeyExampleWithDefault(true)
}

func apiKeyExampleWithDefault(isDefault bool) oas.Object {
	return oas.Object{
		"id":         "8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f",
		"name":       "Production",
		"isDefault":  isDefault,
		"keyPrefix":  "wcai_Bv6f",
		"last4":      "9xQ2",
		"lastUsedAt": nil,
		"createdAt":  "2026-07-02T10:00:00Z",
		"updatedAt":  "2026-07-02T10:00:00Z",
	}
}

func listAPIKeysExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "API keys retrieved successfully",
		"data":    []any{apiKeyExample()},
	}
}

func createAPIKeyExample() oas.Object {
	key := apiKeyExampleWithDefault(false)
	key["key"] = "wcai_Bv6fLwL8y6bpiGvM_rJX8oBsZw8QSg6DSxQ7VhA_9xQ2"
	return oas.Object{
		"success": true,
		"message": "API key created successfully",
		"data":    key,
	}
}

func setDefaultAPIKeyExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Default API key updated successfully",
		"data":    apiKeyExampleWithDefault(true),
	}
}

func revokeAPIKeyExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "API key deleted successfully",
		"data": oas.Object{
			"id":      "8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f",
			"revoked": true,
		},
	}
}
