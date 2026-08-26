package calls

import "whatsapp-ai-caller-server/internal/swagger/oas"

// createCallRequestExample shows only the required fields, so the payload
// Swagger UI prefills into "Try it out" is the minimal valid one. The optional
// agent_id is documented on the schema instead.
func createCallRequestExample() oas.Object {
	return oas.Object{
		"phone_number_id": "phone_number_12345",
		"to":              "+15557654321",
	}
}

// placedCallExample is a call as it looks the instant it is placed: outbound and
// ringing, so everything that only exists once it connects is still null.
func placedCallExample() oas.Object {
	return oas.Object{
		"id":               "9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4",
		"call_id":          "C7A1B2C3D4E5F6",
		"phone_number_id":  "phone_number_12345",
		"agent_id":         "agent_12345",
		"campaign_id":      nil,
		"lead_id":          nil,
		"peer":             "15557654321@s.whatsapp.net",
		"call_type":        "outbound",
		"status":           "received",
		"end_reason":       nil,
		"duration_seconds": nil,
		"answered_at":      nil,
		"ended_at":         nil,
		"created_at":       "2026-07-24T10:00:00Z",
		"updated_at":       "2026-07-24T10:00:00Z",
	}
}

func createCallResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Call created successfully",
		"data":    placedCallExample(),
		"links":   oas.Object{"self": "/v1/calls/9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"},
	}
}

// callResourceExample is a realistic call resource matching the calls table,
// used by the implemented read/update endpoints.
func callResourceExample() oas.Object {
	return oas.Object{
		"id":               "9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4",
		"call_id":          "C7A1B2C3D4E5F6",
		"phone_number_id":  "phone_number_12345",
		"agent_id":         "agent_12345",
		"campaign_id":      nil,
		"lead_id":          nil,
		"peer":             "15557654321@s.whatsapp.net",
		"call_type":        "inbound",
		"status":           "ended",
		"end_reason":       "caller hung up",
		"duration_seconds": 42,
		"answered_at":      "2026-07-24T10:00:05Z",
		"ended_at":         "2026-07-24T10:00:47Z",
		"created_at":       "2026-07-24T10:00:00Z",
		"updated_at":       "2026-07-24T10:00:47Z",
	}
}

func callWithTranscriptExample() oas.Object {
	call := callResourceExample()
	call["messages"] = []any{
		oas.Object{"role": "assistant", "content": "Hello! How can I help you today?"},
		oas.Object{"role": "user", "content": "I'd like to check my order status."},
		oas.Object{"role": "assistant", "content": "Sure — what's your order number?"},
	}
	return call
}

func callResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Call retrieved successfully",
		"data":    callWithTranscriptExample(),
		"links":   oas.Object{"self": "/v1/calls/9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"},
	}
}

func callListResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Calls retrieved successfully",
		"data":    []any{callResourceExample()},
		"meta": oas.Object{
			"pagination": oas.Object{
				"page":              1,
				"limit":             50,
				"total_items":       120,
				"total_pages":       3,
				"has_next_page":     true,
				"has_previous_page": false,
			},
		},
		"links": oas.Object{
			"self":     "/v1/calls?page=1&limit=50",
			"first":    "/v1/calls?page=1&limit=50",
			"previous": nil,
			"next":     "/v1/calls?page=2&limit=50",
			"last":     "/v1/calls?page=3&limit=50",
		},
	}
}

func updateCallRequestExample() oas.Object {
	return oas.Object{
		"status":     "ended",
		"end_reason": "resolved by agent",
	}
}

func updateCallResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Call updated successfully",
		"data":    callResourceExample(),
		"links":   oas.Object{"self": "/v1/calls/9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"},
	}
}

func deleteCallResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Call deleted successfully",
		"data": oas.Object{
			"id":      "9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4",
			"deleted": true,
		},
	}
}
