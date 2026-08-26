package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

func campaignLeadIDParam() oas.Object {
	return oas.Object{"name": "lead_id", "in": "path", "required": true, "description": "Unique identifier of the campaign lead.", "schema": oas.Object{"type": "string", "example": "lead_b7218f2265ca4000"}}
}

func campaignLeadStatusParam() oas.Object {
	return oas.Object{"name": "status", "in": "query", "required": false, "description": "Return only leads in this status.", "schema": oas.Str().Enum(campaignLeadStatusOptions).Example("pending").Build()}
}

func listCampaignLeadsOperation() oas.Object {
	return oas.Object{"tags": []any{tagName}, "operationId": "listCampaignLeads", "summary": "List Campaign Leads", "description": "Returns a paginated list of leads in an owned outbound campaign.", "security": apiKeySecurity(), "parameters": []any{campaignIDParam(), pageParam(), limitParam(), campaignLeadStatusParam()}, "responses": oas.Object{"200": okJSON("A page of campaign leads.", "ListCampaignLeadsResponse", listCampaignLeadsResponseExample()), "400": errJSON("Invalid query parameters.", listQueryValidationErrorExample()), "401": resp401(), "404": resp404(), "429": resp429(), "500": resp500()}}
}

// createCampaignLeadOperation documents the endpoint that also dials: adding a
// lead places the campaign's outbound call to it there and then, which is why
// the 201 carries a call and why the lead comes back already "calling".
func createCampaignLeadOperation() oas.Object {
	const description = "Adds a lead to an owned campaign and immediately calls it with the campaign's agent, from the phone number that agent speaks on. " +
		"Phone numbers use E.164 and are unique within the campaign; leads_count is updated automatically. " +
		"The response's `call` is the placed call, ringing and not yet answered, and the lead comes back in the `calling` status with its first attempt recorded. " +
		"When no call could be placed — the campaign has no agent, the agent has no number, the number is not connected, or the campaign is paused or finished — the lead is still created (201) and `call_error` says why it was not dialled."
	return oas.Object{"tags": []any{tagName}, "operationId": "createCampaignLead", "summary": "Create Campaign Lead", "description": description, "security": apiKeySecurity(), "parameters": []any{campaignIDParam()}, "requestBody": requestBodyNamedExamples("The lead to add.", "CreateCampaignLeadRequest", oas.Object{"lead": oas.Object{"summary": "Lead contact details", "value": campaignLeadRequestExample()}}), "responses": oas.Object{"201": okJSONNamedExamples("The created campaign lead, and the call placed to it.", "CreateCampaignLeadResponse", oas.Object{"dialled": oas.Object{"summary": "Lead added and called", "value": createCampaignLeadResponseExample()}, "notDialled": oas.Object{"summary": "Lead added, no call placed", "value": createCampaignLeadNotDialledExample()}}), "400": errJSON("Validation failed.", campaignLeadValidationErrorExample()), "401": resp401(), "404": resp404(), "409": errJSON("Phone number already exists in this campaign.", campaignLeadConflictErrorExample()), "429": resp429(), "500": resp500()}}
}

// listCampaignCallsOperation documents the campaign's own call history: the
// calls the campaign placed, in the same shape the Calls endpoints serve.
func listCampaignCallsOperation() oas.Object {
	return oas.Object{"tags": []any{tagName}, "operationId": "listCampaignCalls", "summary": "List Campaign Calls", "description": "Returns a paginated list of the calls an owned campaign placed, newest first. These are the same call resources GET /v1/calls serves, narrowed to the campaign that placed them.", "security": apiKeySecurity(), "parameters": []any{campaignIDParam(), pageParam(), campaignCallsLimitParam()}, "responses": oas.Object{"200": okJSON("A page of the campaign's calls.", "CampaignCallsListResponse", listCampaignCallsResponseExample()), "400": errJSON("Invalid query parameters.", listQueryValidationErrorExample()), "401": resp401(), "429": resp429(), "500": resp500()}}
}

// campaignCallsLimitParam is the calls page size (1..200), not the campaign one
// (1..100): this endpoint is served by the calls handler and honours its bounds.
func campaignCallsLimitParam() oas.Object {
	return oas.Object{"name": "limit", "in": "query", "required": false, "description": "Number of calls per page.", "schema": oas.Int().Min(1).Max(200).Default(50).Build()}
}

func campaignAnalyticsDaysParam() oas.Object {
	return oas.Object{"name": "days", "in": "query", "required": false, "description": "Length of the daily activity series, in days ending today.", "schema": oas.Int().Min(1).Max(90).Default(14).Build()}
}

func getCampaignAnalyticsOperation() oas.Object {
	const description = "Returns the campaign's performance: lead and call breakdowns by status, pickup/success/reach rates, talk time, a zero-filled daily series of calls placed and answered, and the most common hangup reasons. " +
		"Everything is aggregated from the leads and calls themselves rather than read off the campaign's stored counters — the two agree in normal operation, but the counters are lifetime totals that survive a deleted call."
	return oas.Object{"tags": []any{tagName}, "operationId": "getCampaignAnalytics", "summary": "Get Outbound Campaign Analytics", "description": description, "security": apiKeySecurity(), "parameters": []any{campaignIDParam(), campaignAnalyticsDaysParam()}, "responses": oas.Object{"200": okJSON("The campaign's analytics.", "CampaignAnalyticsResponse", campaignAnalyticsResponseExample()), "400": errJSON("Invalid query parameters.", campaignAnalyticsDaysErrorExample()), "401": resp401(), "404": resp404(), "429": resp429(), "500": resp500()}}
}

func getCampaignLeadOperation() oas.Object {
	return oas.Object{"tags": []any{tagName}, "operationId": "getCampaignLead", "summary": "Get Campaign Lead", "security": apiKeySecurity(), "parameters": []any{campaignIDParam(), campaignLeadIDParam()}, "responses": oas.Object{"200": okJSON("The campaign lead.", "GetCampaignLeadResponse", campaignLeadResponseExample("Campaign lead retrieved successfully")), "400": errJSON("Invalid path parameters.", campaignIDValidationErrorExample()), "401": resp401(), "404": errJSON("Campaign or lead not found.", campaignLeadNotFoundErrorExample()), "429": resp429(), "500": resp500()}}
}

func updateCampaignLeadOperation() oas.Object {
	return oas.Object{"tags": []any{tagName}, "operationId": "updateCampaignLead", "summary": "Update Campaign Lead", "description": "Updates contact details or lifecycle status. Attempts and last_attempted_at are server-maintained.", "security": apiKeySecurity(), "parameters": []any{campaignIDParam(), campaignLeadIDParam()}, "requestBody": requestBodyNamedExamples("Fields to update.", "UpdateCampaignLeadRequest", oas.Object{"contacted": oas.Object{"summary": "Mark contacted", "value": oas.Object{"status": "contacted"}}}), "responses": oas.Object{"200": okJSON("The updated lead.", "UpdateCampaignLeadResponse", campaignLeadResponseExample("Campaign lead updated successfully")), "400": errJSON("Validation failed.", campaignLeadValidationErrorExample()), "401": resp401(), "404": errJSON("Campaign or lead not found.", campaignLeadNotFoundErrorExample()), "409": errJSON("Phone number already exists in this campaign.", campaignLeadConflictErrorExample()), "429": resp429(), "500": resp500()}}
}

func deleteCampaignLeadOperation() oas.Object {
	return oas.Object{"tags": []any{tagName}, "operationId": "deleteCampaignLead", "summary": "Delete Campaign Lead", "description": "Deletes a lead and decrements the campaign leads_count.", "security": apiKeySecurity(), "parameters": []any{campaignIDParam(), campaignLeadIDParam()}, "responses": oas.Object{"200": okJSON("The lead was deleted.", "DeleteCampaignLeadResponse", deleteCampaignLeadResponseExample()), "400": errJSON("Invalid path parameters.", campaignIDValidationErrorExample()), "401": resp401(), "404": errJSON("Campaign or lead not found.", campaignLeadNotFoundErrorExample()), "429": resp429(), "500": resp500()}}
}

func campaignLeadPaths() oas.Object {
	collection := oas.APIV1Prefix + "/outbound-campaigns/{campaign_id}/leads"
	item := collection + "/{lead_id}"
	calls := oas.APIV1Prefix + "/outbound-campaigns/{campaign_id}/calls"
	analytics := oas.APIV1Prefix + "/outbound-campaigns/{campaign_id}/analytics"
	return oas.Object{collection: oas.Object{"get": listCampaignLeadsOperation(), "post": createCampaignLeadOperation()}, item: oas.Object{"get": getCampaignLeadOperation(), "patch": updateCampaignLeadOperation(), "delete": deleteCampaignLeadOperation()}, calls: oas.Object{"get": listCampaignCallsOperation()}, analytics: oas.Object{"get": getCampaignAnalyticsOperation()}}
}
