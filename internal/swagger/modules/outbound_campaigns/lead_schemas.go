package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

var campaignLeadStatusOptions = []string{"pending", "calling", "called", "failed"}

func campaignLeadPhoneSchema() oas.Object {
	return oas.Object{"type": "string", "description": "Lead phone number in E.164 format. Unique within the campaign.", "pattern": `^\+[1-9][0-9]{7,14}$`, "example": "+14155550123"}
}

func campaignLeadOptionalNameSchema(label string) oas.Object {
	return oas.Object{"type": "string", "nullable": true, "maxLength": 80, "description": label + ". Send null to clear it.", "example": "Maya"}
}

func campaignLeadStatusSchema() *oas.Schema {
	return oas.Str().Desc("Lead lifecycle status.").Enum(campaignLeadStatusOptions).Default("pending").Example("pending")
}

func createCampaignLeadRequestSchema() oas.Object {
	return oas.Obj().Desc("A lead to add. The server initializes status, attempts and attempt timestamps.").Req("phone_number").
		P("phone_number", campaignLeadPhoneSchema()).
		P("email", oas.Str().Format("email").Nullable().Example("maya@example.com")).
		P("first_name", campaignLeadOptionalNameSchema("Optional first name")).
		P("last_name", campaignLeadOptionalNameSchema("Optional last name")).Build()
}

func updateCampaignLeadRequestSchema() oas.Object {
	return oas.Obj().Desc("Fields to update. At least one is required; null clears optional contact fields.").
		P("phone_number", campaignLeadPhoneSchema()).
		P("email", oas.Str().Format("email").Nullable().Example("maya@example.com")).
		P("first_name", campaignLeadOptionalNameSchema("Optional first name")).
		P("last_name", campaignLeadOptionalNameSchema("Optional last name")).
		P("status", campaignLeadStatusSchema()).Build()
}

func campaignLeadSchema() oas.Object {
	return oas.Obj().Desc("A contact loaded into an outbound campaign.").
		Req("lead_id", "campaign_id", "phone_number", "status", "attempts", "created_at", "updated_at").
		P("lead_id", oas.Str().Example("lead_b7218f2265ca4000")).
		P("campaign_id", oas.Str().Example("campaign_a456426614174000")).
		P("phone_number", campaignLeadPhoneSchema()).
		P("email", oas.Str().Format("email").Nullable().Example("maya@example.com")).
		P("first_name", campaignLeadOptionalNameSchema("Optional first name")).
		P("last_name", campaignLeadOptionalNameSchema("Optional last name")).
		P("status", campaignLeadStatusSchema()).
		P("attempts", oas.Int().Min(0).Desc("Dial attempts, maintained by the server.").Example(0)).
		P("last_attempted_at", oas.Str().Format("date-time").Nullable().Example(nil)).
		P("created_at", oas.Str().Format("date-time").Example("2026-08-10T10:00:00Z")).
		P("updated_at", oas.Str().Format("date-time").Example("2026-08-10T10:00:00Z")).Build()
}

// createLeadEnvelopeSchema is the Create response: the lead, plus the call
// adding it started. Adding a lead dials it, so the response has to say what
// happened to that call — call carries the ringing call, and call_error replaces
// it with the reason when none was placed. The lead is created either way, which
// is why a lead that could not be dialled is still a 201.
func createLeadEnvelopeSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).P("message", oas.Str().Example("Campaign lead created and the call is ringing")).
		P("data", oas.Ref("CampaignLead")).
		P("call", oas.Object{"nullable": true, "description": "The outbound call placed to the lead, ringing and not yet answered. Null when no call was placed.", "allOf": []any{oas.Ref("Call")}}).
		P("call_error", oas.Str().Desc("Why no call was placed. Absent when the call is ringing.").Example("this campaign has no agent to dial with, so the lead was added without calling it")).
		P("links", resourceLinksSchema()).Build()
}

// campaignCallsListResponseSchema is a page of the calls one campaign placed.
// The items are the shared Call resource, so a campaign's call and a call read
// from /v1/calls are the same shape.
func campaignCallsListResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).P("message", oas.Str().Example("Campaign calls retrieved successfully")).
		P("data", oas.Arr(oas.Ref("Call"))).P("meta", paginationMetaSchema()).P("links", listLinksSchema()).Build()
}

func leadEnvelopeSchema(message string) oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).P("message", oas.Str().Example(message)).
		P("data", oas.Ref("CampaignLead")).P("links", resourceLinksSchema()).Build()
}

func listCampaignLeadsResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).P("message", oas.Str().Example("Campaign leads retrieved successfully")).
		P("data", oas.Arr(oas.Ref("CampaignLead"))).P("meta", paginationMetaSchema()).P("links", listLinksSchema()).Build()
}

func deleteCampaignLeadResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).P("message", oas.Str().Example("Campaign lead deleted successfully")).
		P("data", oas.Obj().Req("id", "deleted").P("id", oas.Str().Example("lead_b7218f2265ca4000")).P("deleted", oas.Bool().Example(true))).Build()
}

// campaignAnalyticsSchema is one campaign's performance, aggregated from its
// leads and its calls rather than read off the campaign's stored counters.
func campaignAnalyticsSchema() oas.Object {
	leadTotals := oas.Obj().Desc("Leads by lifecycle status.").
		Req("total", "pending", "calling", "called", "failed").
		P("total", oas.Int().Example(120)).P("pending", oas.Int().Example(64)).
		P("calling", oas.Int().Desc("Leads on a live call right now.").Example(2)).
		P("called", oas.Int().Example(43)).P("failed", oas.Int().Example(11))

	callTotals := oas.Obj().Desc("Calls by lifecycle status, plus the two counts that cut across them.").
		Req("total", "received", "answered", "ended", "declined", "failed", "connected", "successful").
		P("total", oas.Int().Example(96)).
		P("received", oas.Int().Desc("Ringing, not yet picked up.").Example(1)).
		P("answered", oas.Int().Desc("Picked up and still talking.").Example(2)).
		P("ended", oas.Int().Example(70)).P("declined", oas.Int().Example(18)).P("failed", oas.Int().Example(5)).
		P("connected", oas.Int().Desc("Every call somebody picked up, whether or not it has ended since.").Example(60)).
		P("successful", oas.Int().Desc("Calls that were picked up and then ended normally.").Example(58))

	talkTime := oas.Obj().Desc("Time spent connected. Only answered calls have a duration, so these describe the conversations rather than the attempts.").
		Req("total_seconds", "average_seconds", "longest_seconds").
		P("total_seconds", oas.Int().Example(7420)).P("average_seconds", oas.Int().Example(124)).
		P("longest_seconds", oas.Int().Example(431))

	daily := oas.Arr(oas.Obj().Req("date", "calls", "answered").
		P("date", oas.Str().Format("date").Example("2026-08-14")).
		P("calls", oas.Int().Example(12)).P("answered", oas.Int().Example(7))).
		Desc("One row per day, oldest first, zero-filled: a day with no calls is a zero rather than a missing point.")

	endReasons := oas.Arr(oas.Obj().Req("reason", "count").
		P("reason", oas.Str().Example("callee hung up")).P("count", oas.Int().Example(23))).
		Desc("The most common hangup reasons, most frequent first. Calls that recorded none are left out.")

	return oas.Obj().Desc("A campaign's performance, aggregated from its leads and the calls it placed.").
		Req("campaign_id", "leads", "calls", "pickup_rate", "success_rate", "reach_rate", "talk_time", "daily", "end_reasons").
		P("campaign_id", oas.Str().Example("campaign_a456426614174000")).
		P("leads", leadTotals).P("calls", callTotals).
		P("pickup_rate", oas.Num().Desc("Connected calls over calls placed. 0 when none were placed.").Example(0.62)).
		P("success_rate", oas.Num().Desc("Successful calls over calls placed.").Example(0.28)).
		P("reach_rate", oas.Num().Desc("Called leads over total leads.").Example(0.41)).
		P("talk_time", talkTime).P("daily", daily).P("end_reasons", endReasons).
		P("first_call_at", oas.Str().Format("date-time").Nullable().Example("2026-08-10T10:00:00Z")).
		P("last_call_at", oas.Str().Format("date-time").Nullable().Example("2026-08-14T16:20:00Z")).Build()
}

func campaignAnalyticsResponseSchema() oas.Object {
	return oas.Obj().Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).P("message", oas.Str().Example("Campaign analytics retrieved successfully")).
		P("data", oas.Ref("CampaignAnalytics")).
		P("meta", oas.Obj().P("days", oas.Int().Desc("Length of the daily series returned.").Example(14))).
		P("links", resourceLinksSchema()).Build()
}

func campaignLeadComponentSchemas() oas.Object {
	return oas.Object{
		"CampaignLead":               campaignLeadSchema(),
		"CreateCampaignLeadRequest":  createCampaignLeadRequestSchema(),
		"UpdateCampaignLeadRequest":  updateCampaignLeadRequestSchema(),
		"CreateCampaignLeadResponse": createLeadEnvelopeSchema(),
		"CampaignCallsListResponse":  campaignCallsListResponseSchema(),
		"CampaignAnalytics":          campaignAnalyticsSchema(),
		"CampaignAnalyticsResponse":  campaignAnalyticsResponseSchema(),
		"GetCampaignLeadResponse":    leadEnvelopeSchema("Campaign lead retrieved successfully"),
		"UpdateCampaignLeadResponse": leadEnvelopeSchema("Campaign lead updated successfully"),
		"ListCampaignLeadsResponse":  listCampaignLeadsResponseSchema(),
		"DeleteCampaignLeadResponse": deleteCampaignLeadResponseSchema(),
	}
}
