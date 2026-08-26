package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

// The campaign field contract follows this project's Knowledge Base and Agents
// modules: JSON request bodies, the {success, message, data} envelope with
// links, paginated lists, and the shared ErrorResponse envelope for failures.
var campaignStatusOptions = []string{"draft", "running", "paused", "completed", "failed"}

const maxCampaignNameLength = 80

// campaignNameSchema is written as a raw schema object because the fluent
// builder has no maxLength setter.
func campaignNameSchema() oas.Object {
	return oas.Object{
		"type":        "string",
		"description": "Name of the campaign. Must be unique among the owner's campaigns.",
		"maxLength":   maxCampaignNameLength,
		"example":     "July reactivation",
	}
}

func campaignStatusSchema() *oas.Schema {
	return oas.Str().
		Desc("Lifecycle state of the campaign. A campaign is created as draft; running and paused are the two live states; completed and failed are terminal.").
		Enum(campaignStatusOptions).
		Default("draft").
		Example("running")
}

// budgetSchema is nullable on purpose: no budget is a real, distinct setting
// rather than a budget of zero, and it is what the dashboard renders as
// "No Budget".
func budgetSchema() *oas.Schema {
	return oas.Num().
		Desc("Spend ceiling for the campaign in US dollars. Null means the campaign is uncapped and is shown as \"No Budget\".").
		Min(0).
		Nullable().
		Example(250)
}

// agentIDSchema is the one half of the agent/number pairing a caller sets.
// Picking the agent is what picks the number the campaign dials from, so there
// is deliberately no phone_number_id to send: an agent already carries the
// number it speaks on, and a campaign naming its own could disagree with it.
func agentIDSchema() *oas.Schema {
	return oas.Str().
		Desc("Identifier of the agent the campaign dials with. Must be one of the owner's agents and must already have a phone number assigned — that agent's number is the number the campaign calls from. Null detaches the agent, which is only allowed while the campaign is not running.").
		Nullable().
		Example("agent_9f1c2d3e4b5a6789")
}

// ---------------------------------------------------------------------------
// Request schemas.
// ---------------------------------------------------------------------------

// createOutboundCampaignRequestSchema carries only what a caller may set. The
// counters are server-maintained and are rejected here rather than being
// silently ignored, so a client cannot believe it seeded a campaign's totals.
func createOutboundCampaignRequestSchema() oas.Object {
	return oas.Obj().
		Desc("The campaign to create. Only campaign_name is required; a new campaign starts at status draft with every counter at zero. agent_id may be supplied now or added later, but a campaign cannot run without one.").
		Req("campaign_name").
		P("campaign_name", campaignNameSchema()).
		P("budget_usd", budgetSchema()).
		P("agent_id", agentIDSchema()).
		Build()
}

// updateOutboundCampaignRequestSchema is a partial update: omitted fields keep
// their stored value, and at least one field must be supplied.
//
// status is settable because pausing and resuming a campaign is a status
// change, not a separate endpoint. The counters stay read-only.
func updateOutboundCampaignRequestSchema() oas.Object {
	return oas.Obj().
		Desc("The fields to change. At least one is required. Counters, and the phone number the campaign inherits from its agent, cannot be written.").
		P("campaign_name", campaignNameSchema()).
		P("status", campaignStatusSchema()).
		P("budget_usd", budgetSchema()).
		P("agent_id", agentIDSchema()).
		Build()
}

// ---------------------------------------------------------------------------
// Resource schema.
// ---------------------------------------------------------------------------

// outboundCampaignSchema is the stored campaign row plus the two rates derived
// from its counters.
//
// The rates are returned rather than left to the caller because every consumer
// computes the same two quotients from the same three counters, and a client
// that divides by a calls_placed of zero renders NaN on an untouched campaign.
// They are read-only: writing them is a validation error.
//
// The counters advance on their own as the campaign's calls progress, so a
// caller that polls this resource sees the campaign move. A breakdown of the
// same activity — per status, per day, with talk time — is the analytics
// endpoint.
func outboundCampaignSchema() oas.Object {
	return oas.Obj().
		Desc("An outbound calling campaign owned by the authenticated API key owner. Its counters advance as the calls it places are answered and end; GET /v1/outbound-campaigns/{campaign_id}/analytics breaks the same activity down by status, by day, and by talk time.").
		Req("campaign_id", "campaign_name", "status", "leads_count", "calls_placed", "answered_calls",
			"successful_calls", "today_calls", "total_usage_seconds", "created_at", "updated_at").
		P("campaign_id", oas.Str().Desc("Unique identifier of the campaign.").Example("campaign_a456426614174000")).
		P("campaign_name", campaignNameSchema()).
		P("status", campaignStatusSchema()).
		P("budget_usd", budgetSchema()).

		// The agent, and the two number fields read back through it. Only
		// agent_id is settable; the rest describe the agent that id names, and
		// follow it if the agent is later moved to another number.
		P("agent_id", agentIDSchema()).
		P("agent_name", oas.Str().Nullable().Desc("Name of the agent the campaign dials with. Read-only; null when no agent is attached.").Example("Pearl")).
		P("phone_number_id", oas.Str().Nullable().Desc("Identifier of the phone number the campaign calls from. Read-only: it is the number assigned to the campaign's agent, not a setting of its own.").Example("pn_2c1d4e5f6a7b8c90")).
		P("phone_number", oas.Str().Nullable().Desc("The agent's phone number in E.164. Read-only; null when the campaign has no agent.").Example("+390232164148")).

		// Counters. Server-maintained as the campaign's leads are added and its
		// calls progress; read-only on every request body.
		P("leads_count", oas.Int().Desc("Number of leads loaded into the campaign.").Min(0).Example(1240)).
		P("calls_placed", oas.Int().Desc("Total calls the campaign has placed.").Min(0).Example(656)).
		P("answered_calls", oas.Int().Desc("Calls the callee picked up. Numerator of pickup_rate.").Min(0).Example(412)).
		P("successful_calls", oas.Int().Desc("Calls that met the campaign's success condition. Numerator of success_rate.").Min(0).Example(188)).
		P("today_calls", oas.Int().Desc("Calls placed on today_calls_date. It is reported as 0 once that date is no longer today, so a stale counter never renders as today's activity.").Min(0).Example(37)).
		P("today_calls_date", oas.Str().Format("date").Nullable().Desc("The day today_calls counts. Null until the campaign places its first call.").Example("2026-08-10")).
		P("total_usage_seconds", oas.Int().Desc("Total connected call time in seconds. Dashboards render this in minutes.").Min(0).Example(74520)).

		// Derived, read-only.
		P("pickup_rate", oas.Num().Desc("answered_calls / calls_placed, as a fraction between 0 and 1. Read-only; 0 when no calls have been placed.").Min(0).Max(1).Example(0.628)).
		P("success_rate", oas.Num().Desc("successful_calls / calls_placed, as a fraction between 0 and 1. Read-only; 0 when no calls have been placed.").Min(0).Max(1).Example(0.287)).
		P("started_at", oas.Str().Format("date-time").Nullable().Desc("When the campaign first moved to running. Null while it is still a draft.").Example("2026-08-01T09:00:00Z")).
		P("completed_at", oas.Str().Format("date-time").Nullable().Desc("When the campaign reached a terminal status. Null until then.").Example(nil)).
		P("created_at", oas.Str().Format("date-time").Example("2026-08-01T08:42:00Z")).
		P("updated_at", oas.Str().Format("date-time").Example("2026-08-10T11:15:00Z")).
		Build()
}

// ---------------------------------------------------------------------------
// Response envelopes — same shape as the Knowledge Base module.
// ---------------------------------------------------------------------------

func resourceLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Resource links.").
		P("self", oas.Str().Desc("Canonical URL of this resource.").Example("/v1/outbound-campaigns/campaign_a456426614174000"))
}

func listLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Pagination links as query URLs.").
		P("self", oas.Str().Example("/v1/outbound-campaigns?page=1&limit=20")).
		P("first", oas.Str().Example("/v1/outbound-campaigns?page=1&limit=20")).
		P("previous", oas.Str().Nullable().Desc("Previous page URL, or null on the first page.").Example(nil)).
		P("next", oas.Str().Nullable().Desc("Next page URL, or null on the last page.").Example(nil)).
		P("last", oas.Str().Example("/v1/outbound-campaigns?page=1&limit=20"))
}

func paginationMetaSchema() *oas.Schema {
	return oas.Obj().Desc("Collection metadata.").
		P("pagination", oas.Obj().
			P("page", oas.Int().Desc("Current page (1-based).").Example(1)).
			P("limit", oas.Int().Desc("Page size.").Example(20)).
			P("total_items", oas.Int().Desc("Total matching items.").Example(1)).
			P("total_pages", oas.Int().Desc("Total number of pages.").Example(1)).
			P("has_next_page", oas.Bool().Example(false)).
			P("has_previous_page", oas.Bool().Example(false)))
}

func campaignEnvelopeSchema(desc, message string) oas.Object {
	return oas.Obj().Desc(desc).Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example(message)).
		P("data", oas.Ref("OutboundCampaign")).
		P("links", resourceLinksSchema()).
		Build()
}

func createOutboundCampaignResponseSchema() oas.Object {
	return campaignEnvelopeSchema("Successful create response.", "Outbound campaign created successfully")
}

func getOutboundCampaignResponseSchema() oas.Object {
	return campaignEnvelopeSchema("Successful single campaign response.", "Outbound campaign retrieved successfully")
}

func updateOutboundCampaignResponseSchema() oas.Object {
	return campaignEnvelopeSchema("Successful update response.", "Outbound campaign updated successfully")
}

func listOutboundCampaignsResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful paginated list response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Outbound campaigns retrieved successfully")).
		P("data", oas.Arr(oas.Ref("OutboundCampaign")).Desc("Page of campaign resources.")).
		P("meta", paginationMetaSchema()).
		P("links", listLinksSchema()).
		Build()
}

func deleteOutboundCampaignResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful delete response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Outbound campaign deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").
			P("id", oas.Str().Desc("Identifier of the deleted campaign.").Example("campaign_a456426614174000")).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func componentSchemas() oas.Object {
	return oas.Object{
		"OutboundCampaign":               outboundCampaignSchema(),
		"CreateOutboundCampaignRequest":  createOutboundCampaignRequestSchema(),
		"UpdateOutboundCampaignRequest":  updateOutboundCampaignRequestSchema(),
		"CreateOutboundCampaignResponse": createOutboundCampaignResponseSchema(),
		"GetOutboundCampaignResponse":    getOutboundCampaignResponseSchema(),
		"UpdateOutboundCampaignResponse": updateOutboundCampaignResponseSchema(),
		"ListOutboundCampaignsResponse":  listOutboundCampaignsResponseSchema(),
		"DeleteOutboundCampaignResponse": deleteOutboundCampaignResponseSchema(),
	}
}
