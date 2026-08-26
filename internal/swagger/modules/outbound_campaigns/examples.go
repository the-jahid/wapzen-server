package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

// ---------------------------------------------------------------------------
// Resource examples.
// ---------------------------------------------------------------------------

// newCampaignExample is a freshly created campaign: a draft, with no budget set
// and every counter at zero. It is what the dashboard tiles read as
// 0 / 0 / 0 / "No Budget".
func newCampaignExample() oas.Object {
	return oas.Object{
		"campaign_id":         "campaign_a456426614174000",
		"campaign_name":       "July reactivation",
		"status":              "draft",
		"budget_usd":          nil,
		"agent_id":            nil,
		"agent_name":          nil,
		"phone_number_id":     nil,
		"phone_number":        nil,
		"leads_count":         0,
		"calls_placed":        0,
		"answered_calls":      0,
		"successful_calls":    0,
		"today_calls":         0,
		"today_calls_date":    nil,
		"total_usage_seconds": 0,
		"pickup_rate":         0,
		"success_rate":        0,
		"started_at":          nil,
		"completed_at":        nil,
		"created_at":          "2026-08-01T08:42:00Z",
		"updated_at":          "2026-08-01T08:42:00Z",
	}
}

// runningCampaignExample is the same campaign mid-flight, with the counters the
// dashboard tiles are built from and the agent it is dialling with. The number
// is the one assigned to that agent: it is shown, never sent.
func runningCampaignExample() oas.Object {
	c := newCampaignExample()
	c["status"] = "running"
	c["budget_usd"] = 250
	c["agent_id"] = "agent_9f1c2d3e4b5a6789"
	c["agent_name"] = "Pearl"
	c["phone_number_id"] = "pn_2c1d4e5f6a7b8c90"
	c["phone_number"] = "+390232164148"
	c["leads_count"] = 1240
	c["calls_placed"] = 656
	c["answered_calls"] = 412
	c["successful_calls"] = 188
	c["today_calls"] = 37
	c["today_calls_date"] = "2026-08-10"
	c["total_usage_seconds"] = 74520
	c["pickup_rate"] = 0.628
	c["success_rate"] = 0.287
	c["started_at"] = "2026-08-01T09:00:00Z"
	c["updated_at"] = "2026-08-10T11:15:00Z"
	return c
}

func selfLink() oas.Object {
	return oas.Object{"self": "/v1/outbound-campaigns/campaign_a456426614174000"}
}

// ---------------------------------------------------------------------------
// Request examples.
// ---------------------------------------------------------------------------

// createCampaignMinimalRequestExample is the smallest accepted body: the name
// alone. The campaign is created as a draft with no budget.
func createCampaignMinimalRequestExample() oas.Object {
	return oas.Object{
		"campaign_name": "July reactivation",
	}
}

func createCampaignRequestExample() oas.Object {
	return oas.Object{
		"campaign_name": "July reactivation",
		"budget_usd":    250,
		"agent_id":      "agent_9f1c2d3e4b5a6789",
	}
}

// startCampaignRequestExample is the update that puts a draft on the air. The
// campaign must already have an agent for this to be accepted.
func startCampaignRequestExample() oas.Object {
	return oas.Object{
		"status": "running",
	}
}

// assignCampaignAgentRequestExample points an existing campaign at an agent.
// There is no companion phone-number field: the campaign dials from whichever
// number that agent is assigned to.
func assignCampaignAgentRequestExample() oas.Object {
	return oas.Object{
		"agent_id": "agent_9f1c2d3e4b5a6789",
	}
}

func updateCampaignBudgetRequestExample() oas.Object {
	return oas.Object{
		"budget_usd": 500,
	}
}

// clearCampaignBudgetRequestExample uncaps a campaign. Null is the way to do
// it: omitting the field keeps the stored budget instead.
func clearCampaignBudgetRequestExample() oas.Object {
	return oas.Object{
		"budget_usd": nil,
	}
}

// ---------------------------------------------------------------------------
// Success response examples.
// ---------------------------------------------------------------------------

func createOutboundCampaignResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Outbound campaign created successfully",
		"data":    newCampaignExample(),
		"links":   selfLink(),
	}
}

func getOutboundCampaignResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Outbound campaign retrieved successfully",
		"data":    runningCampaignExample(),
		"links":   selfLink(),
	}
}

func updateOutboundCampaignResponseExample() oas.Object {
	c := runningCampaignExample()
	c["budget_usd"] = 500
	return oas.Object{
		"success": true,
		"message": "Outbound campaign updated successfully",
		"data":    c,
		"links":   selfLink(),
	}
}

func listOutboundCampaignsResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Outbound campaigns retrieved successfully",
		"data":    []any{runningCampaignExample(), newCampaignExample()},
		"meta": oas.Object{
			"pagination": oas.Object{
				"page":              1,
				"limit":             20,
				"total_items":       2,
				"total_pages":       1,
				"has_next_page":     false,
				"has_previous_page": false,
			},
		},
		"links": oas.Object{
			"self":     "/v1/outbound-campaigns?page=1&limit=20",
			"first":    "/v1/outbound-campaigns?page=1&limit=20",
			"previous": nil,
			"next":     nil,
			"last":     "/v1/outbound-campaigns?page=1&limit=20",
		},
	}
}

func deleteOutboundCampaignResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Outbound campaign deleted successfully",
		"data": oas.Object{
			"id":      "campaign_a456426614174000",
			"deleted": true,
		},
	}
}

// ---------------------------------------------------------------------------
// Error response examples — the shared ErrorResponse envelope.
// ---------------------------------------------------------------------------

func errorExample(message string, fieldErrors ...oas.Object) oas.Object {
	errs := make([]any, 0, len(fieldErrors))
	for _, e := range fieldErrors {
		errs = append(errs, e)
	}
	return oas.Object{
		"success": false,
		"message": message,
		"errors":  errs,
	}
}

func fieldError(field, message string) oas.Object {
	return oas.Object{"field": field, "message": message}
}

func validationErrorExample() oas.Object {
	return errorExample("Validation failed",
		fieldError("campaign_name", "campaign_name is required and must be at most 80 characters"),
		fieldError("budget_usd", "budget_usd must not be negative"),
	)
}

// updateValidationErrorExample shows the two ways an update is refused: an
// empty body, and an attempt to write a server-maintained counter.
func updateValidationErrorExample() oas.Object {
	return errorExample("Invalid request body",
		fieldError("body", "supply at least one field to update"),
		fieldError("calls_placed", "calls_placed is maintained by the server and cannot be set"),
	)
}

func listQueryValidationErrorExample() oas.Object {
	return errorExample("Invalid query parameters",
		fieldError("limit", "limit must be between 1 and 100"),
	)
}

func campaignIDValidationErrorExample() oas.Object {
	return errorExample("Invalid path parameter",
		fieldError("campaign_id", "campaign_id is not a valid identifier"),
	)
}

func unauthorizedErrorExample() oas.Object {
	return errorExample("Authentication required",
		fieldError("authorization", "Missing or invalid bearer token"),
	)
}

func notFoundErrorExample() oas.Object {
	return errorExample("Outbound campaign not found",
		fieldError("campaign_id", "No campaign exists with the given id"),
	)
}

func nameConflictErrorExample() oas.Object {
	return errorExample("Campaign name already in use",
		fieldError("campaign_name", "A campaign with this name already exists"),
	)
}

// statusConflictErrorExample is the illegal transition: a terminal campaign
// cannot be put back on the air.
func statusConflictErrorExample() oas.Object {
	return errorExample("Invalid campaign status transition",
		fieldError("status", "A completed campaign cannot be moved back to running"),
	)
}

func deleteRunningConflictErrorExample() oas.Object {
	return errorExample("Campaign is running",
		fieldError("status", "Pause the campaign before deleting it"),
	)
}

func tooManyRequestsErrorExample() oas.Object {
	return errorExample("Too many requests",
		fieldError("rate_limit", "Request quota exceeded. Retry after 30 seconds"),
	)
}

func internalErrorExample() oas.Object {
	return errorExample("Internal server error",
		fieldError("server", "An unexpected error occurred while processing the request"),
	)
}
