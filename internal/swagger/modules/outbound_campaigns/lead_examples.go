package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

func campaignLeadExample() oas.Object {
	return oas.Object{"lead_id": "lead_b7218f2265ca4000", "campaign_id": "campaign_a456426614174000", "phone_number": "+14155550123", "email": "maya@example.com", "first_name": "Maya", "last_name": "Chen", "status": "pending", "attempts": 0, "last_attempted_at": nil, "created_at": "2026-08-10T10:00:00Z", "updated_at": "2026-08-10T10:00:00Z"}
}

func campaignLeadRequestExample() oas.Object {
	return oas.Object{"phone_number": "+14155550123", "email": "maya@example.com", "first_name": "Maya", "last_name": "Chen"}
}

func campaignLeadResponseExample(message string) oas.Object {
	return oas.Object{"success": true, "message": message, "data": campaignLeadExample(), "links": oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000/leads/lead_b7218f2265ca4000"}}
}

// campaignCallExample is a call the campaign placed to a lead, as it looks the
// instant it goes on the wire: ringing, so everything that only exists once it
// connects is still null.
func campaignCallExample() oas.Object {
	return oas.Object{"id": "9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4", "call_id": "C7A1B2C3D4E5F6", "phone_number_id": "phone_number_12345", "agent_id": "agent_12345", "campaign_id": "campaign_a456426614174000", "lead_id": "lead_b7218f2265ca4000", "peer": "14155550123@s.whatsapp.net", "call_type": "outbound", "status": "received", "end_reason": nil, "duration_seconds": nil, "answered_at": nil, "ended_at": nil, "created_at": "2026-08-10T10:00:00Z", "updated_at": "2026-08-10T10:00:00Z"}
}

// createCampaignLeadResponseExample shows the lead as it stands the moment its
// call is ringing: the insert stamped the first attempt and moved it to
// "calling", so the lead here is already past "pending".
func createCampaignLeadResponseExample() oas.Object {
	lead := campaignLeadExample()
	lead["status"] = "calling"
	lead["attempts"] = 1
	lead["last_attempted_at"] = "2026-08-10T10:00:00Z"
	return oas.Object{"success": true, "message": "Campaign lead created and the call is ringing", "data": lead, "call": campaignCallExample(), "links": oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000/leads/lead_b7218f2265ca4000"}}
}

// createCampaignLeadNotDialledExample is the same 201 for a campaign that has
// nothing to dial from: the lead is saved, stays pending, and call_error says
// why it was not called.
func createCampaignLeadNotDialledExample() oas.Object {
	return oas.Object{"success": true, "message": "Campaign lead created successfully", "data": campaignLeadExample(), "call": nil, "call_error": "this campaign has no agent to dial with, so the lead was added without calling it", "links": oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000/leads/lead_b7218f2265ca4000"}}
}

func campaignAnalyticsResponseExample() oas.Object {
	return oas.Object{"success": true, "message": "Campaign analytics retrieved successfully", "data": oas.Object{
		"campaign_id": "campaign_a456426614174000",
		"leads":       oas.Object{"total": 120, "pending": 64, "calling": 2, "contacted": 41, "failed": 11, "opted_out": 2},
		"calls":       oas.Object{"total": 96, "received": 1, "answered": 2, "ended": 70, "declined": 18, "failed": 5, "connected": 60, "successful": 58},
		"pickup_rate": 0.625, "success_rate": 0.604, "reach_rate": 0.342,
		"talk_time": oas.Object{"total_seconds": 7420, "average_seconds": 124, "longest_seconds": 431},
		"daily": []any{
			oas.Object{"date": "2026-08-12", "calls": 31, "answered": 19},
			oas.Object{"date": "2026-08-13", "calls": 44, "answered": 28},
			oas.Object{"date": "2026-08-14", "calls": 21, "answered": 13},
		},
		"end_reasons":   []any{oas.Object{"reason": "callee hung up", "count": 38}, oas.Object{"reason": "agent ended the call", "count": 20}},
		"first_call_at": "2026-08-12T09:12:00Z", "last_call_at": "2026-08-14T16:20:00Z",
	}, "meta": oas.Object{"days": 14}, "links": oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000/analytics"}}
}

func listCampaignCallsResponseExample() oas.Object {
	return oas.Object{"success": true, "message": "Campaign calls retrieved successfully", "data": []any{campaignCallExample()}, "meta": oas.Object{"pagination": oas.Object{"page": 1, "limit": 50, "total_items": 1, "total_pages": 1, "has_next_page": false, "has_previous_page": false}}, "links": oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000/calls?page=1&limit=50", "first": "/v1/outbound-campaigns/campaign_a456426614174000/calls?page=1&limit=50", "previous": nil, "next": nil, "last": "/v1/outbound-campaigns/campaign_a456426614174000/calls?page=1&limit=50"}}
}

func listCampaignLeadsResponseExample() oas.Object {
	return oas.Object{"success": true, "message": "Campaign leads retrieved successfully", "data": []any{campaignLeadExample()}, "meta": oas.Object{"pagination": oas.Object{"page": 1, "limit": 20, "total_items": 1, "total_pages": 1, "has_next_page": false, "has_previous_page": false}}, "links": oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000/leads?page=1&limit=20", "first": "/v1/outbound-campaigns/campaign_a456426614174000/leads?page=1&limit=20", "previous": nil, "next": nil, "last": "/v1/outbound-campaigns/campaign_a456426614174000/leads?page=1&limit=20"}}
}

func deleteCampaignLeadResponseExample() oas.Object {
	return oas.Object{"success": true, "message": "Campaign lead deleted successfully", "data": oas.Object{"id": "lead_b7218f2265ca4000", "deleted": true}}
}

func campaignLeadValidationErrorExample() oas.Object {
	return errorExample("Invalid request body", fieldError("phone_number", "phone_number must be a valid E.164 number (for example +14155550123)"))
}

func campaignLeadConflictErrorExample() oas.Object {
	return errorExample("Lead phone number already exists", fieldError("phone_number", "A lead with this phone number already exists in the campaign"))
}

func campaignAnalyticsDaysErrorExample() oas.Object {
	return errorExample("Invalid query parameters", fieldError("days", "days must be between 1 and 90"))
}

func campaignLeadNotFoundErrorExample() oas.Object {
	return errorExample("Campaign lead not found", fieldError("lead_id", "No lead exists with the given id in this campaign"))
}
