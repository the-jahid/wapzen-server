// Package chat_conversations registers the static OpenAPI contract for the
// saved WhatsApp text threads a chat agent handled. It changes documentation
// only; live routes are registered elsewhere.
//
// The collection is read-only apart from closing and deleting a thread: the
// messages are written by the message runtime as it answers, so there is no
// endpoint that creates one.
package chat_conversations

import (
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

const tagName = "Chat Conversations"

// InjectStaticChatConversationDocs adds the chat-conversation tag, paths, and
// schemas to the shared OpenAPI document served by Swagger UI.
func InjectStaticChatConversationDocs(document oas.OpenAPIObject) {
	tags, _ := document["tags"].([]any)
	document["tags"] = append(tags, oas.Object{
		"name":        tagName,
		"description": "Saved WhatsApp chat threads: the messages a chat agent exchanged with a contact.",
	})

	paths, ok := document["paths"].(oas.Object)
	if !ok {
		paths = oas.Object{}
		document["paths"] = paths
	}
	for path, item := range conversationPaths() {
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
	for name, schema := range conversationSchemas() {
		schemas[name] = schema
	}
}

func conversationPaths() oas.Object {
	return oas.Object{
		"/v1/chat-conversations": oas.Object{
			"get": listConversationsOperation(),
		},
		"/v1/chat-conversations/{conversation_id}": oas.Object{
			"get":    getConversationOperation(),
			"patch":  updateConversationOperation(),
			"delete": deleteConversationOperation(),
		},
		"/v1/chat-conversations/{conversation_id}/messages": oas.Object{
			"post": sendConversationMessageOperation(),
		},
		"/v1/chat-agents/{chat_agent_id}/conversations": oas.Object{
			"get": listAgentConversationsOperation(),
		},
	}
}

func listConversationsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listChatConversations",
		"summary":     "List Chat Conversations",
		"description": "Returns a paginated list of the authenticated user's saved WhatsApp threads, most recently active first. " +
			"A thread is one contact writing to one of the user's numbers; it is opened by the first message that arrives and " +
			"appended to on every exchange after that. Message transcripts are omitted here — read one thread to get its messages.",
		"security": apiKeySecurity(),
		"parameters": []any{
			pageParam(), limitParam(),
			queryParameter("chat_agent_id", "Only threads the given chat agent last answered.", oas.Str().Example("chat_agent_12345").Build()),
			queryParameter("phone_number_id", "Only threads that arrived on the given phone number.", oas.Str().Example("phone_number_12345").Build()),
			queryParameter("status", "Only threads in this state.", oas.Str().Enum(conversationStatuses()).Example(models.ChatConversationStatusOpen).Build()),
			queryParameter("search", "Case-insensitive match on the contact's JID, phone number, or WhatsApp name.", oas.Str().Example("+15557654321").Build()),
		},
		"responses": oas.Object{
			"200": jsonResponse("A page of conversations.", "ChatConversationListResponse", listResponseExample()),
			"400": errorResponse("Invalid query parameters."),
			"401": errorResponse("Unauthorized - missing or invalid API key bearer token."),
			"500": errorResponse("Internal Server Error"),
		},
	}
}

func listAgentConversationsOperation() oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": "listChatAgentConversations",
		"summary":     "List Chat Agent Conversations",
		"description": "The same listing narrowed to one chat agent, addressed by the agent instead of by a query parameter. " +
			"It is scoped to the authenticated user as well, so a chat agent id belonging to somebody else matches nothing " +
			"rather than exposing their threads.",
		"security": apiKeySecurity(),
		"parameters": []any{
			chatAgentIDParam(), pageParam(), limitParam(),
			queryParameter("status", "Only threads in this state.", oas.Str().Enum(conversationStatuses()).Example(models.ChatConversationStatusOpen).Build()),
			queryParameter("search", "Case-insensitive match on the contact's JID, phone number, or WhatsApp name.", oas.Str().Example("Alex").Build()),
		},
		"responses": oas.Object{
			"200": jsonResponse("A page of the agent's conversations.", "ChatConversationListResponse", listResponseExample()),
			"400": errorResponse("Invalid query parameters."),
			"401": errorResponse("Unauthorized - missing or invalid API key bearer token."),
			"500": errorResponse("Internal Server Error"),
		},
	}
}

func getConversationOperation() oas.Object {
	return itemOperation("getChatConversation", "Get Chat Conversation",
		"Returns one saved thread with its full transcript, ordered by turn.",
		oas.Object{
			"200": jsonResponse("The conversation, including its messages.", "ChatConversationResponse", conversationResponseExample()),
			"401": errorResponse("Unauthorized - missing or invalid API key bearer token."),
			"404": errorResponse("Conversation not found."),
			"500": errorResponse("Internal Server Error"),
		})
}

func updateConversationOperation() oas.Object {
	op := itemOperation("updateChatConversation", "Update Chat Conversation",
		"Closes or reopens a thread. Only status may be changed: the messages are what happened, so nothing else about a "+
			"saved thread is editable. Closing is bookkeeping for whoever reads the inbox — it does not stop the agent "+
			"answering, which is what pausing the agent is for.",
		oas.Object{
			"200": jsonResponse("The updated conversation.", "ChatConversationResponse", updatedConversationResponseExample()),
			"400": errorResponse("status is missing or not one of open, closed."),
			"401": errorResponse("Unauthorized - missing or invalid API key bearer token."),
			"404": errorResponse("Conversation not found."),
			"500": errorResponse("Internal Server Error"),
		})
	op["requestBody"] = jsonRequest("UpdateChatConversationRequest", true, oas.Object{
		"status": models.ChatConversationStatusClosed,
	})
	return op
}

func deleteConversationOperation() oas.Object {
	return itemOperation("deleteChatConversation", "Delete Chat Conversation",
		"Deletes a thread owned by the authenticated user. Its saved messages are removed with it.",
		oas.Object{
			"200": jsonResponse("The conversation was deleted.", "DeleteChatConversationResponse", oas.Object{
				"success": true,
				"message": "Conversation deleted successfully",
				"data":    oas.Object{"id": "c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3", "deleted": true},
			}),
			"401": errorResponse("Unauthorized - missing or invalid API key bearer token."),
			"404": errorResponse("Conversation not found."),
			"500": errorResponse("Internal Server Error"),
		})
}

func sendConversationMessageOperation() oas.Object {
	op := itemOperation("sendChatConversationMessage", "Send Chat Message",
		"Sends a WhatsApp message on this thread and appends it to the saved transcript — a person taking the "+
			"conversation over from the agent. It goes out on the number the thread runs on, so that number must still "+
			"be assigned to the account and connected. The message is stored with the \"assistant\" role, because to the "+
			"contact it is the same party speaking; it is also added to the agent's in-process history, so the agent's "+
			"next reply is written knowing what was already said.",
		oas.Object{
			"201": jsonResponse("The message was sent and saved.", "SendChatMessageResponse", sendMessageResponseExample()),
			"400": errorResponse("content is missing, blank, or longer than 4096 characters."),
			"401": errorResponse("Unauthorized - missing or invalid API key bearer token."),
			"404": errorResponse("Conversation not found."),
			"409": errorResponse("The thread's phone number is gone from the account or is not connected."),
			"500": errorResponse("Internal Server Error"),
			"502": errorResponse("WhatsApp refused the message."),
			"503": errorResponse("WhatsApp messaging is not available on this server."),
		})
	op["requestBody"] = jsonRequest("SendChatMessageRequest", true, oas.Object{
		"content": "We deliver Monday to Saturday, 9am–7pm.",
	})
	return op
}

func itemOperation(operationID, summary, description string, responses oas.Object) oas.Object {
	return oas.Object{
		"tags":        []any{tagName},
		"operationId": operationID,
		"summary":     summary,
		"description": description,
		"security":    apiKeySecurity(),
		"parameters":  []any{conversationIDParam()},
		"responses":   responses,
	}
}

func conversationIDParam() oas.Object {
	return oas.Object{
		"name":        "conversation_id",
		"in":          "path",
		"required":    true,
		"description": "Unique identifier of the conversation.",
		"schema":      oas.Str().Example("c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3").Build(),
	}
}

func chatAgentIDParam() oas.Object {
	return oas.Object{
		"name":        "chat_agent_id",
		"in":          "path",
		"required":    true,
		"description": "Unique chat-agent identifier.",
		"schema":      oas.Str().Example("chat_agent_12345").Build(),
	}
}

func pageParam() oas.Object {
	return queryParameter("page", "Page number (1-based).", oas.Int().Min(1).Default(1).Build())
}

func limitParam() oas.Object {
	return queryParameter("limit", "Number of conversations per page (1–200, default 50).", oas.Int().Min(1).Max(200).Default(50).Build())
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

func conversationStatuses() []string {
	return []string{models.ChatConversationStatusOpen, models.ChatConversationStatusClosed}
}

func conversationSchemas() oas.Object {
	return oas.Object{
		"ChatConversation":               conversationSchema(),
		"ChatConversationMessage":        messageSchema(),
		"ChatConversationResponse":       conversationResponseSchema(),
		"ChatConversationListResponse":   conversationListResponseSchema(),
		"UpdateChatConversationRequest":  updateConversationRequestSchema(),
		"SendChatMessageRequest":         sendMessageRequestSchema(),
		"SendChatMessageResponse":        sendMessageResponseSchema(),
		"DeleteChatConversationResponse": deleteConversationResponseSchema(),
	}
}

func conversationSchema() oas.Object {
	return oas.Obj().Desc("One saved WhatsApp thread between a chat agent and a contact.").
		Req("id", "peer_jid", "status", "message_count", "created_at", "updated_at").
		P("id", oas.Str().Desc("Unique conversation identifier.").Example("c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3")).
		P("phone_number_id", oas.Str().Nullable().Desc("The user's number the thread runs on, if still present.").Example("phone_number_12345")).
		P("chat_agent_id", oas.Str().Nullable().Desc("Chat agent that last answered on this thread, if still present.").Example("chat_agent_12345")).
		P("peer_jid", oas.Str().Desc("The contact's WhatsApp JID.").Example("15557654321@s.whatsapp.net")).
		P("peer_phone", oas.Str().Nullable().Desc("The contact's number in E.164. A message often arrives carrying only "+
			"the sender's LID (an opaque WhatsApp identifier), in which case this is resolved at read time from the "+
			"LID→phone mapping and stays null only while that mapping is still unknown.").Example("+15557654321")).
		P("peer_name", oas.Str().Nullable().Desc("The contact's WhatsApp display name, as it arrived on the message.").Example("Alex Doe")).
		P("status", oas.Str().Desc("Whether the thread is still open in the inbox.").
			Enum(conversationStatuses()).Example(models.ChatConversationStatusOpen)).
		P("message_count", oas.Int().Desc("Number of saved messages on the thread.").Example(6)).
		P("last_message_role", oas.Str().Nullable().Desc("Who spoke last.").
			Enum([]string{models.ChatRoleUser, models.ChatRoleAssistant}).Example(models.ChatRoleAssistant)).
		P("last_message_at", oas.Str().Format("date-time").Nullable().Desc("When the last message was saved.").Example("2026-08-30T10:04:11Z")).
		P("created_at", oas.Str().Format("date-time").Desc("When the thread was opened.").Example("2026-08-30T10:03:52Z")).
		P("updated_at", oas.Str().Format("date-time").Desc("Last-update timestamp.").Example("2026-08-30T10:04:11Z")).
		P("messages", oas.Arr(oas.Ref("ChatConversationMessage")).
			Desc("Saved transcript, ordered by turn; present on the Get Chat Conversation response.")).
		Build()
}

func messageSchema() oas.Object {
	return oas.Obj().Desc("One turn of a saved chat thread.").
		Req("seq", "role", "content", "created_at").
		P("seq", oas.Int().Desc("Turn order within the thread, starting at 0.").Example(0)).
		P("role", oas.Str().Desc("Who sent this turn: the contact, or the agent.").
			Enum([]string{models.ChatRoleUser, models.ChatRoleAssistant}).Example(models.ChatRoleUser)).
		P("content", oas.Str().Desc("The message text.").Example("Do you deliver on Sundays?")).
		P("created_at", oas.Str().Format("date-time").Desc("When the turn was saved.").Example("2026-08-30T10:03:52Z")).
		Build()
}

func conversationResponseSchema() oas.Object {
	return oas.Obj().Desc("A single conversation resource.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Conversation retrieved successfully")).
		P("data", oas.Ref("ChatConversation")).
		P("links", oas.Obj().P("self", oas.Str().Example("/v1/chat-conversations/c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3"))).
		Build()
}

func conversationListResponseSchema() oas.Object {
	return oas.Obj().Desc("A page of conversations, most recently active first.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Conversations retrieved successfully")).
		P("data", oas.Arr(oas.Ref("ChatConversation"))).
		P("meta", oas.Obj().Desc("Collection metadata.").
			P("pagination", oas.Obj().
				P("page", oas.Int().Desc("Current page (1-based).").Example(1)).
				P("limit", oas.Int().Desc("Page size.").Example(50)).
				P("total_items", oas.Int().Desc("Total matching conversations.").Example(12)).
				P("total_pages", oas.Int().Desc("Total number of pages.").Example(1)).
				P("has_next_page", oas.Bool().Example(false)).
				P("has_previous_page", oas.Bool().Example(false)))).
		P("links", oas.Obj().Desc("Pagination links as query URLs.").
			P("self", oas.Str().Example("/v1/chat-conversations?page=1&limit=50")).
			P("first", oas.Str().Example("/v1/chat-conversations?page=1&limit=50")).
			P("previous", oas.Str().Nullable().Example(nil)).
			P("next", oas.Str().Nullable().Example(nil)).
			P("last", oas.Str().Example("/v1/chat-conversations?page=1&limit=50"))).
		Build()
}

func updateConversationRequestSchema() oas.Object {
	return oas.Obj().Desc("Change to a saved conversation. Only status may be set.").
		Req("status").
		P("status", oas.Str().Desc("New thread state.").
			Enum(conversationStatuses()).Example(models.ChatConversationStatusClosed)).
		Build()
}

func sendMessageRequestSchema() oas.Object {
	return oas.Obj().Desc("A message to send on this thread.").
		Req("content").
		P("content", oas.Str().Desc("The message text, 1–4096 characters.").
			Example("We deliver Monday to Saturday, 9am–7pm.")).
		Build()
}

func sendMessageResponseSchema() oas.Object {
	return oas.Obj().Desc("The message that was sent, as it now reads in the transcript.").
		Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Message sent successfully")).
		P("data", oas.Ref("ChatConversationMessage")).
		P("links", oas.Obj().P("self", oas.Str().Example("/v1/chat-conversations/c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3"))).
		Build()
}

func deleteConversationResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful conversation deletion response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Conversation deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").
			P("id", oas.Str().Example("c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

// conversationExample is a thread as the listing returns it: no transcript, the
// summary columns filled in by the last message that arrived.
func conversationExample() oas.Object {
	return oas.Object{
		"id":                "c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3",
		"phone_number_id":   "phone_number_12345",
		"chat_agent_id":     "chat_agent_12345",
		"peer_jid":          "15557654321@s.whatsapp.net",
		"peer_phone":        "+15557654321",
		"peer_name":         "Alex Doe",
		"status":            models.ChatConversationStatusOpen,
		"message_count":     4,
		"last_message_role": models.ChatRoleAssistant,
		"last_message_at":   "2026-08-30T10:04:11Z",
		"created_at":        "2026-08-30T10:03:52Z",
		"updated_at":        "2026-08-30T10:04:11Z",
	}
}

func listResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Conversations retrieved successfully",
		"data":    []any{conversationExample()},
		"meta": oas.Object{"pagination": oas.Object{
			"page": 1, "limit": 50, "total_items": 1, "total_pages": 1,
			"has_next_page": false, "has_previous_page": false,
		}},
		"links": oas.Object{
			"self":     "/v1/chat-conversations?page=1&limit=50",
			"first":    "/v1/chat-conversations?page=1&limit=50",
			"previous": nil,
			"next":     nil,
			"last":     "/v1/chat-conversations?page=1&limit=50",
		},
	}
}

func conversationResponseExample() oas.Object {
	conversation := conversationExample()
	conversation["messages"] = []any{
		oas.Object{"seq": 0, "role": models.ChatRoleUser, "content": "Do you deliver on Sundays?", "created_at": "2026-08-30T10:03:52Z"},
		oas.Object{"seq": 1, "role": models.ChatRoleAssistant, "content": "We deliver Monday to Saturday, 9am–7pm.", "created_at": "2026-08-30T10:03:54Z"},
		oas.Object{"seq": 2, "role": models.ChatRoleUser, "content": "Great — book me for Saturday please.", "created_at": "2026-08-30T10:04:09Z"},
		oas.Object{"seq": 3, "role": models.ChatRoleAssistant, "content": "Booked for Saturday. Anything else?", "created_at": "2026-08-30T10:04:11Z"},
	}
	return oas.Object{
		"success": true,
		"message": "Conversation retrieved successfully",
		"data":    conversation,
		"links":   oas.Object{"self": "/v1/chat-conversations/c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3"},
	}
}

func sendMessageResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Message sent successfully",
		"data": oas.Object{
			"seq":        4,
			"role":       models.ChatRoleAssistant,
			"content":    "We deliver Monday to Saturday, 9am–7pm.",
			"created_at": "2026-08-30T10:06:02Z",
		},
		"links": oas.Object{"self": "/v1/chat-conversations/c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3"},
	}
}

func updatedConversationResponseExample() oas.Object {
	conversation := conversationExample()
	conversation["status"] = models.ChatConversationStatusClosed
	return oas.Object{
		"success": true,
		"message": "Conversation updated successfully",
		"data":    conversation,
		"links":   oas.Object{"self": "/v1/chat-conversations/c1f0a6de-6f3f-4a52-9c1c-2c0f2ef7a1b3"},
	}
}
