package tools

import "whatsapp-ai-caller-server/internal/swagger/oas"

// toolParameterSchema is one argument the model has to fill in before the tool
// runs. It is rendered into the function schema the model sees, so the
// description is prompt text: it is the only thing telling the model what to put
// there.
func toolParameterSchema() oas.Object {
	return oas.Obj().Desc("One argument the model fills in when it calls the tool.").
		Req("name", "type", "description").
		P("name", oas.Str().Desc("Argument name, as it appears in the JSON the model produces.").
			Example("date")).
		P("type", oas.Str().Desc("JSON type of the argument.").
			Enum(ParameterTypes()).Default("string").Example("string")).
		P("description", oas.Str().Desc("What the model should put here, written for the model to read.").
			Example("Requested day in YYYY-MM-DD form.")).
		P("required", oas.Bool().Desc("Whether the model must supply this argument before the tool runs.").
			Default(false).Example(true)).
		Build()
}

// toolHeaderSchema is one outgoing HTTP header on an api_request tool. The value
// is stored and sent verbatim — there is no placeholder syntax — so a header
// carrying a credential stores that credential on the tool.
func toolHeaderSchema() oas.Object {
	return oas.Obj().Desc("One HTTP header sent with an api_request tool's request.").
		Req("key", "value").
		P("key", oas.Str().Desc("Header name.").Example("Authorization")).
		P("value", oas.Str().Desc("Header value, sent exactly as given. A credential put here is stored on "+
			"the tool, so prefer a scoped key over one that can do more than this tool needs.").
			Example("Bearer sk-live-2f9c1e...")).
		Build()
}

// apiRequestConfigSchema is the configuration read when type is api_request.
func apiRequestConfigSchema() oas.Object {
	return oas.Obj().Desc("Configuration for an api_request tool. Required when type is api_request, ignored otherwise.").
		Req("url").
		P("method", oas.Str().Desc("HTTP method for the request.").
			Enum(HTTPMethods()).Default("POST").Example("GET")).
		P("url", oas.Str().Desc("Absolute URL the request goes to. Must be http or https.").
			Format("uri").Example("https://api.acme-health.example/v1/availability")).
		P("timeout_seconds", oas.Int().Desc("How long to wait for the response before giving up. The caller "+
			"is sitting in silence while a blocking request runs, so keep this short.").
			Min(1).Max(60).Default(20).Example(20)).
		P("async", oas.Bool().Desc("When false (the default) the agent waits for the response before replying. "+
			"When true it fires the request and keeps talking, and the response is never spoken back.").
			Default(false).Example(false)).
		P("headers", oas.Arr(oas.Ref("ToolHeader")).Desc("Headers sent with the request.")).
		P("parameters", oas.Arr(oas.Ref("ToolParameter")).Desc("Arguments the model fills in. On GET they "+
			"become the query string; on every other method, the JSON request body.")).
		Build()
}

// transferCallConfigSchema is the configuration read when type is transfer_call.
//
// The description spells out what a transfer actually does here, because the
// name promises more than WhatsApp calling can deliver: there is no way to
// bridge a second leg onto a live call, so the agent announces the handover and
// releases the caller rather than connecting the two parties.
func transferCallConfigSchema() oas.Object {
	return oas.Obj().Desc("Configuration for a transfer_call tool. Required when type is transfer_call, ignored otherwise. "+
		"WhatsApp calling cannot bridge a second leg onto a live call, so the agent speaks the message, records the "+
		"destination against the call, and releases the caller.").
		Req("destination").
		P("destination", oas.Str().Desc("Number the caller is handed to, in E.164 format. Recorded in the call's "+
			"end reason so the handover is auditable.").
			Format("phone").Example("+8801639726992")).
		P("message", oas.Str().Desc("What the agent says before releasing the caller. Leave empty to let the "+
			"model word the handover itself.").
			Example("Connecting you to a teammate now, one moment.")).
		Build()
}

// endCallConfigSchema is the configuration read when type is end_call. It is
// optional: a tool with no message lets the model word its own goodbye.
func endCallConfigSchema() oas.Object {
	return oas.Obj().Desc("Configuration for an end_call tool. Optional when type is end_call, ignored otherwise.").
		P("message", oas.Str().Desc("What the agent says before hanging up. The line stays open until this has "+
			"been spoken. Leave empty to let the model word its own goodbye.").
			Example("Thanks for calling, goodbye.")).
		Build()
}

// sendTextConfigSchema is the configuration read when type is send_text.
func sendTextConfigSchema() oas.Object {
	return oas.Obj().Desc("Configuration for a send_text tool. Optional when type is send_text, ignored otherwise.").
		P("body", oas.Str().Desc("The message to send. Leave empty to let the model write the message "+
			"itself, in which case it is passed as a tool argument at call time.").
			Example("Here is the booking link we just talked about: https://acme-health.example/book")).
		Build()
}

// toolSchema is the tool resource the read endpoints serve. name and description
// are what the model reads when deciding whether to call the tool, so they are
// prompt text rather than labels: a vague description is the usual reason a tool
// never gets called.
func toolSchema() oas.Object {
	return oas.Obj().Desc("An action an agent can take during a call.").
		Req("id", "type", "name", "description", "created_at", "updated_at").
		P("id", oas.Str().Desc("Unique tool identifier.").Example("tool_12345")).
		P("type", oas.Str().Desc("Which action this tool performs. It decides which configuration block is read.").
			Enum(ToolTypes()).Example(ToolTypeAPIRequest)).
		P("name", oas.Str().Desc("Function name the model calls. Lowercase letters, digits and underscores "+
			"only, and unique across the tools attached to one agent.").
			Example("check_availability")).
		P("description", oas.Str().Desc("What the model reads when deciding whether to call this. Say what "+
			"the tool does and when to use it.").
			Example("Look up open appointment slots for a given day so the agent can offer times.")).
		P("agent_id", oas.Str().Nullable().Desc("Agent this tool belongs to, or null while it belongs to none. "+
			"Read-only here: attach a tool by writing the owning agent's `tools.tool_ids`. A tool belongs to one "+
			"agent, so a non-null value means attaching it elsewhere is rejected with 409 until it is detached "+
			"here. Deleting that agent deletes this tool with it.").
			Example(nil)).
		P("api_request", oas.Ref("ToolAPIRequestConfig")).
		P("transfer_call", oas.Ref("ToolTransferCallConfig")).
		P("send_text", oas.Ref("ToolSendTextConfig")).
		P("end_call", oas.Ref("ToolEndCallConfig")).
		P("created_at", oas.Str().Format("date-time").Desc("When the tool was created.").
			Example("2026-08-19T10:00:00Z")).
		P("updated_at", oas.Str().Format("date-time").Desc("Last-update timestamp.").
			Example("2026-08-19T10:00:00Z")).
		Build()
}

// createToolRequestSchema is the body for creating a tool. Which configuration
// block is required follows from type; end_call and send_text may omit theirs,
// because the model can supply the message at call time.
func createToolRequestSchema() oas.Object {
	return oas.Obj().Desc("Request payload for creating a tool. Supply the configuration block matching "+
		"type: api_request and transfer_call require theirs, while send_text and end_call may omit theirs.").
		Req("type", "name", "description").
		P("type", oas.Str().Desc("Which action this tool performs.").
			Enum(ToolTypes()).Example(ToolTypeAPIRequest)).
		P("name", oas.Str().Desc("Function name the model calls. Lowercase letters, digits and underscores only.").
			Example("check_availability")).
		P("description", oas.Str().Desc("What the model reads when deciding whether to call this.").
			Example("Look up open appointment slots for a given day so the agent can offer times.")).
		P("api_request", oas.Ref("ToolAPIRequestConfig")).
		P("transfer_call", oas.Ref("ToolTransferCallConfig")).
		P("send_text", oas.Ref("ToolSendTextConfig")).
		P("end_call", oas.Ref("ToolEndCallConfig")).
		Example(createToolRequestExample()).
		Build()
}

// updateToolRequestSchema is a partial update. A supplied configuration block
// replaces the stored one wholesale — headers and parameters are arrays, so
// there is no way to merge them row by row without an identity for each row.
func updateToolRequestSchema() oas.Object {
	return oas.Obj().Desc("Partial update to a tool. Supply at least one field. A configuration block "+
		"replaces the stored one wholesale rather than merging into it. type cannot be changed after "+
		"creation; delete the tool and create it again instead.").
		P("name", oas.Str().Desc("New function name.").Example("check_availability")).
		P("description", oas.Str().Desc("New description.").
			Example("Look up open appointment slots for a given day.")).
		P("api_request", oas.Ref("ToolAPIRequestConfig")).
		P("transfer_call", oas.Ref("ToolTransferCallConfig")).
		P("send_text", oas.Ref("ToolSendTextConfig")).
		P("end_call", oas.Ref("ToolEndCallConfig")).
		Build()
}

func createToolResponseSchema() oas.Object {
	return oas.Obj().Desc("The tool that was created.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Tool created successfully")).
		P("data", oas.Ref("Tool")).
		P("links", oas.Obj().P("self", oas.Str().Example("/v1/tools/tool_12345"))).
		Build()
}

func toolResponseSchema() oas.Object {
	return oas.Obj().Desc("A single tool resource.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Tool retrieved successfully")).
		P("data", oas.Ref("Tool")).
		P("links", oas.Obj().P("self", oas.Str().Example("/v1/tools/tool_12345"))).
		Build()
}

// paginationMetaSchema and listLinksSchema mirror the calls collection so every
// paginated endpoint reports progress through a page the same way.
func paginationMetaSchema() *oas.Schema {
	return oas.Obj().Desc("Collection metadata.").
		P("pagination", oas.Obj().
			P("page", oas.Int().Desc("Current page (1-based).").Example(1)).
			P("limit", oas.Int().Desc("Page size.").Example(50)).
			P("total_items", oas.Int().Desc("Total matching tools.").Example(3)).
			P("total_pages", oas.Int().Desc("Total number of pages.").Example(1)).
			P("has_next_page", oas.Bool().Example(false)).
			P("has_previous_page", oas.Bool().Example(false)))
}

func listLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Pagination links as query URLs.").
		P("self", oas.Str().Example("/v1/tools?page=1&limit=50")).
		P("first", oas.Str().Example("/v1/tools?page=1&limit=50")).
		P("previous", oas.Str().Nullable().Desc("Previous page URL, or null on the first page.").Example(nil)).
		P("next", oas.Str().Nullable().Desc("Next page URL, or null on the last page.").Example(nil)).
		P("last", oas.Str().Example("/v1/tools?page=1&limit=50"))
}

func toolListResponseSchema() oas.Object {
	return oas.Obj().Desc("A page of tool resources, newest first.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Tools retrieved successfully")).
		P("data", oas.Arr(oas.Ref("Tool"))).
		P("meta", paginationMetaSchema()).
		P("links", listLinksSchema()).
		Build()
}

func toolDeleteResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful tool deletion response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Tool deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").
			P("id", oas.Str().Example("tool_12345")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func componentSchemas() oas.Object {
	return oas.Object{
		"Tool":                   toolSchema(),
		"ToolParameter":          toolParameterSchema(),
		"ToolHeader":             toolHeaderSchema(),
		"ToolAPIRequestConfig":   apiRequestConfigSchema(),
		"ToolTransferCallConfig": transferCallConfigSchema(),
		"ToolSendTextConfig":     sendTextConfigSchema(),
		"ToolEndCallConfig":      endCallConfigSchema(),
		"CreateToolRequest":      createToolRequestSchema(),
		"CreateToolResponse":     createToolResponseSchema(),
		"UpdateToolRequest":      updateToolRequestSchema(),
		"ToolResponse":           toolResponseSchema(),
		"ToolListResponse":       toolListResponseSchema(),
		"ToolDeleteResponse":     toolDeleteResponseSchema(),
	}
}
