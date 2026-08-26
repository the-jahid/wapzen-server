package calls

import (
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/oas"
)

// createCallRequestSchema is the body for placing an outbound call. The agent
// that speaks is derived from phone_number_id, which is why only the number and
// the destination are required.
func createCallRequestSchema() oas.Object {
	return oas.Obj().Desc("Request payload for placing a new outbound call.").
		Req("phone_number_id", "to").
		P("phone_number_id", oas.Str().Desc("Identifier of the user's connected phone number to place the call from. "+
			"It must be connected, and the agent assigned to it is the one that speaks on the call.").
			Example("phone_number_12345")).
		P("to", oas.Str().Desc("Destination phone number in E.164 format.").
			Format("phone").Example("+15557654321")).
		P("agent_id", oas.Str().Desc("Optional assertion of which agent handles the call. "+
			"The agent is always the one assigned to phone_number_id; supplying a different id is rejected "+
			"rather than overriding it.").
			Example("agent_12345")).
		Example(createCallRequestExample()).
		Build()
}

// createCallResponseSchema returns the same call resource the read endpoints
// serve, so a placed call and a fetched one are one shape. The call is still
// ringing when this is returned: its status is "received" and becomes
// "answered" once the callee picks up.
func createCallResponseSchema() oas.Object {
	return oas.Obj().Desc("The call that was placed. It is ringing, not yet answered.").
		Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Call created successfully")).
		P("data", oas.Ref("Call")).
		P("links", oas.Obj().P("self", oas.Str().Example("/v1/calls/9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"))).
		Build()
}

// callMessageSchema is one turn of a call's saved conversation transcript.
func callMessageSchema() oas.Object {
	return oas.Obj().Desc("One turn of a call's saved conversation transcript.").
		Req("role", "content").
		P("role", oas.Str().Desc("Who spoke this turn.").
			Enum([]string{"assistant", "user"}).Example("assistant")).
		P("content", oas.Str().Desc("The spoken text of the turn.").
			Example("Hello! How can I help you today?")).
		Build()
}

// callSchema is the real call resource returned by the read/update endpoints,
// matching the calls table. Nullable fields stay null until the relevant point
// in the call's lifecycle; messages is present only on the Get response.
func callSchema() oas.Object {
	return oas.Obj().Desc("A voice call handled for the authenticated user.").
		Req("id", "call_id", "peer", "call_type", "status", "created_at", "updated_at").
		P("id", oas.Str().Desc("Unique call identifier.").Example("9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4")).
		P("call_id", oas.Str().Desc("Underlying WhatsApp call id.").Example("C7A1B2C3D4E5F6")).
		P("phone_number_id", oas.Str().Nullable().Desc("Phone number the call used, if still present.").Example("phone_number_12345")).
		P("agent_id", oas.Str().Nullable().Desc("Agent that handled the call, if still present.").Example("agent_12345")).
		P("campaign_id", oas.Str().Nullable().Desc("Outbound campaign that placed the call; null on every call no campaign placed.").Example(nil)).
		P("lead_id", oas.Str().Nullable().Desc("Campaign lead the call was placed to; null on every call no campaign placed.").Example(nil)).
		P("peer", oas.Str().Desc("The remote party (caller/callee) JID.").Example("15557654321@s.whatsapp.net")).
		P("call_type", oas.Str().Desc("Call direction.").
			Enum([]string{models.CallTypeInbound, models.CallTypeOutbound}).Example(models.CallTypeInbound)).
		P("status", oas.Str().Desc("Call lifecycle status.").
			Enum(models.CallStatuses()).Example(models.CallStatusEnded)).
		P("end_reason", oas.Str().Nullable().Desc("Why the call ended.").Example("caller hung up")).
		P("duration_seconds", oas.Int().Nullable().Desc("Connected duration in whole seconds; null until answered.").Example(42)).
		P("answered_at", oas.Str().Format("date-time").Nullable().Desc("When the call was answered.").Example("2026-07-24T10:00:05Z")).
		P("ended_at", oas.Str().Format("date-time").Nullable().Desc("When the call ended.").Example("2026-07-24T10:00:47Z")).
		P("created_at", oas.Str().Format("date-time").Desc("When the call was first recorded.").Example("2026-07-24T10:00:00Z")).
		P("updated_at", oas.Str().Format("date-time").Desc("Last-update timestamp.").Example("2026-07-24T10:00:47Z")).
		P("messages", oas.Arr(oas.Ref("CallMessage")).Desc("Saved conversation transcript; present on the Get Call response.")).
		Build()
}

func callResponseSchema() oas.Object {
	return oas.Obj().Desc("A single call resource.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Call retrieved successfully")).
		P("data", oas.Ref("Call")).
		P("links", oas.Obj().P("self", oas.Str().Example("/v1/calls/9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"))).
		Build()
}

// paginationMetaSchema and listLinksSchema mirror the agents collection so both
// paginated endpoints report progress through a page the same way.
func paginationMetaSchema() *oas.Schema {
	return oas.Obj().Desc("Collection metadata.").
		P("pagination", oas.Obj().
			P("page", oas.Int().Desc("Current page (1-based).").Example(1)).
			P("limit", oas.Int().Desc("Page size.").Example(50)).
			P("total_items", oas.Int().Desc("Total matching calls.").Example(120)).
			P("total_pages", oas.Int().Desc("Total number of pages.").Example(3)).
			P("has_next_page", oas.Bool().Example(true)).
			P("has_previous_page", oas.Bool().Example(false)))
}

func listLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Pagination links as query URLs.").
		P("self", oas.Str().Example("/v1/calls?page=1&limit=50")).
		P("first", oas.Str().Example("/v1/calls?page=1&limit=50")).
		P("previous", oas.Str().Nullable().Desc("Previous page URL, or null on the first page.").Example(nil)).
		P("next", oas.Str().Nullable().Desc("Next page URL, or null on the last page.").Example("/v1/calls?page=2&limit=50")).
		P("last", oas.Str().Example("/v1/calls?page=3&limit=50"))
}

func callListResponseSchema() oas.Object {
	return oas.Obj().Desc("A page of call resources, newest first.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Calls retrieved successfully")).
		P("data", oas.Arr(oas.Ref("Call"))).
		P("meta", paginationMetaSchema()).
		P("links", listLinksSchema()).
		Build()
}

func updateCallRequestSchema() oas.Object {
	return oas.Obj().Desc("Partial update to a call. Supply at least one field.").
		P("status", oas.Str().Desc("New call lifecycle status.").
			Enum(models.CallStatuses()).Example(models.CallStatusEnded)).
		P("end_reason", oas.Str().Desc("New end reason; send an empty string to clear it.").Example("caller hung up")).
		Build()
}

func callDeleteResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful call deletion response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Call deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").
			P("id", oas.Str().Example("9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func componentSchemas() oas.Object {
	return oas.Object{
		"CreateCallRequest":  createCallRequestSchema(),
		"CreateCallResponse": createCallResponseSchema(),
		"Call":               callSchema(),
		"CallMessage":        callMessageSchema(),
		"CallResponse":       callResponseSchema(),
		"CallListResponse":   callListResponseSchema(),
		"UpdateCallRequest":  updateCallRequestSchema(),
		"CallDeleteResponse": callDeleteResponseSchema(),
	}
}
