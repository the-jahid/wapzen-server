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
	"whatsapp-ai-caller-server/internal/swagger/modules/knowledge_base"
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
		},
		"components": oas.Object{
			// .addBearerAuth() analogue: every Agents operation references this
			// `bearer` scheme via `security: [{ bearer: [] }]`.
			"securitySchemes": oas.Object{
				"bearer": oas.Object{
					"type":        "http",
					"scheme":      "bearer",
					"description": "Bearer authentication. Send a Clerk session token as `Authorization: Bearer <token>`.",
				},
			},
			"schemas": oas.Object{
				"APIResponse": oas.Obj().
					Desc("Standard JSON envelope returned by the foundation endpoints.").
					P("success", oas.Bool().Example(true)).
					P("message", oas.Str().Example("Server is running")).
					P("data", oas.Object{}).
					Build(),
			},
		},
	}

	// Apply the static-doc module mutators.
	agents.InjectStaticAgentDocs(doc)
	calls.InjectStaticCallDocs(doc)
	phone_numbers.InjectStaticPhoneNumberDocs(doc)
	knowledge_base.InjectStaticKnowledgeBaseDocs(doc)
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
// application record, resolved from the Clerk bearer token by the
// auth middleware.
func currentUserOperation() oas.Object {
	return oas.Object{
		"tags":        []any{"users"},
		"summary":     "Get current user",
		"description": "Returns the application user for the authenticated Clerk session, including the internal user id.",
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
