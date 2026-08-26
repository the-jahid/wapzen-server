package constants

// SelectableFieldsOptions enumerates the values accepted by the `fields`
// sparse-fieldset query parameter on GET /v1/agents. Dot-notation selects a
// nested property of a top-level section.
var SelectableFieldsOptions = []string{
	"id",
	"created_at",
	"updated_at",
	"agent",
	"agent.name",
	"agent.language",
	"agent.timezone",
	"agent.phone_number_id",
	"agent.call_direction",
	"agent.status",
	"model",
	"prompt",
	"voice",
	"transcriber",
	"post_call",
	"knowledge_base",
	"knowledge_base.knowledge_base_ids",
	"tools",
	"tools.tool_ids",
}

// SelectableField is the union of SelectableFieldsOptions.
type SelectableField = string
