package phone_numbers

import "whatsapp-ai-caller-server/internal/swagger/oas"

func phoneNumberExample() oas.Object {
	return oas.Object{
		"id":                "phone_number_12345",
		"phone_number":      "+15551234567",
		"label":             "Main support line",
		"wa_jid":            nil,
		"status":            "pending_qr",
		"qr_code":           "data:image/png;base64,iVBORw0KGgo...",
		"last_connected_at": nil,
		"created_at":        "2026-07-03T10:00:00Z",
		"updated_at":        "2026-07-03T10:00:00Z",
	}
}

func loginPhoneNumberRequestExample() oas.Object {
	return oas.Object{
		"label": "Main support line",
	}
}

func logoutPhoneNumberRequestExample() oas.Object {
	return oas.Object{
		"phone_number_id": "phone_number_12345",
	}
}

func createPhoneNumberRequestExample() oas.Object {
	return oas.Object{
		"phone_number": "+15551234567",
		"label":        "Main support line",
	}
}

func phoneNumberResponseExample(message string) oas.Object {
	return oas.Object{
		"success": true,
		"message": message,
		"data":    phoneNumberExample(),
		"links": oas.Object{
			"self": "/v1/phone-number/phone_number_12345",
		},
	}
}

func listPhoneNumbersResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Phone numbers retrieved successfully",
		"data":    []any{phoneNumberExample()},
	}
}

func logoutPhoneNumberResponseExample() oas.Object {
	return oas.Object{
		"success": true,
		"message": "Phone number logged out and deleted successfully",
		"data": oas.Object{
			"id":           "phone_number_12345",
			"disconnected": true,
			"deleted":      true,
		},
	}
}
