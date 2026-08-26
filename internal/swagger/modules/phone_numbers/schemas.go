package phone_numbers

import "whatsapp-ai-caller-server/internal/swagger/oas"

func phoneNumberFieldSchema() oas.Object {
	return oas.Object{
		"type":        "string",
		"description": "Phone number in E.164 format.",
		"pattern":     "^\\+[1-9]\\d{1,14}$",
		"example":     "+15551234567",
	}
}

func nullablePhoneNumberFieldSchema() oas.Object {
	schema := phoneNumberFieldSchema()
	schema["nullable"] = true
	return schema
}

func labelFieldSchema() oas.Object {
	return oas.Object{
		"type":        "string",
		"description": "Human-readable label for the phone number.",
		"maxLength":   80,
		"example":     "Main support line",
	}
}

func nullableLabelFieldSchema() oas.Object {
	schema := labelFieldSchema()
	schema["nullable"] = true
	return schema
}

func qrCodeFieldSchema() oas.Object {
	return oas.Object{
		"type":        "string",
		"description": "Data URL containing a PNG QR code for WhatsApp Linked Devices scanning. Present while status is pending_qr.",
		"example":     "data:image/png;base64,iVBORw0KGgo...",
	}
}

func phoneNumberResourceSchema() oas.Object {
	return oas.Obj().Desc("A phone number owned by the authenticated user.").
		P("id", oas.Str().Desc("Unique phone number identifier.").Example("phone_number_12345")).
		P("phone_number", nullablePhoneNumberFieldSchema()).
		P("label", nullableLabelFieldSchema()).
		P("wa_jid", oas.Str().Nullable().Desc("WhatsApp device JID stored after pairing.").Example("15551234567:1@s.whatsapp.net")).
		P("status", oas.Str().Desc("WhatsApp login lifecycle status.").Enum([]string{"pending_qr", "connected", "disconnected", "failed", "expired"}).Example("pending_qr")).
		P("qr_code", qrCodeFieldSchema()).
		P("last_connected_at", oas.Str().Format("date-time").Nullable().Desc("Most recent successful WhatsApp connection timestamp.").Example("2026-07-03T10:00:00Z")).
		P("created_at", oas.Str().Format("date-time").Desc("Creation timestamp.").Example("2026-07-03T10:00:00Z")).
		P("updated_at", oas.Str().Format("date-time").Desc("Last-update timestamp.").Example("2026-07-03T10:00:00Z")).
		Req("id", "status", "created_at", "updated_at").
		Build()
}

func loginPhoneNumberRequestSchema() oas.Object {
	return oas.Obj().Desc("Optional metadata for starting a WhatsApp QR login. Pass phone_number_id to restart an existing non-connected login. Set force_repair with phone_number_id to unlink and replace a connected companion while preserving the phone-number row and agent assignment. Pass agent_id to hand the scanned number to that agent the moment it connects.").
		P("phone_number_id", oas.Str().Desc("Existing phone number id whose login session should be restarted.").Example("phone_number_12345")).
		P("agent_id", oas.Str().Desc("Agent that this number is assigned to as soon as the scan connects it. Must be one of the caller's agents. Ignored with force_repair, and skipped when the agent already has a number.").Example("agent_12345")).
		P("force_repair", oas.Bool().Desc("Unlink the current companion and issue a fresh QR without deleting the phone-number row. Requires phone_number_id.").Default(false).Example(false)).
		P("phone_number", phoneNumberFieldSchema()).
		P("label", labelFieldSchema()).
		Build()
}

func logoutPhoneNumberRequestSchema() oas.Object {
	return oas.Obj().Desc("Phone number logout request.").Req("phone_number_id").
		P("phone_number_id", oas.Str().Desc("Unique phone number identifier.").Example("phone_number_12345")).
		Build()
}

// createPhoneNumberRequestSchema is a documentation-only mock request body for
// registering a new phone number. No route consumes it yet; it renders in the
// Swagger UI Schemas section as a reference contract.
func createPhoneNumberRequestSchema() oas.Object {
	return oas.Obj().Desc("Request payload for creating a new phone number.").Req("phone_number").
		P("phone_number", phoneNumberFieldSchema()).
		P("label", labelFieldSchema()).
		Example(createPhoneNumberRequestExample()).
		Build()
}

func phoneNumberLinksSchema() *oas.Schema {
	return oas.Obj().Desc("Resource links.").
		P("self", oas.Str().Desc("Canonical URL of this resource.").Example("/v1/phone-number/phone_number_12345"))
}

func loginPhoneNumberResponseSchema() oas.Object {
	return phoneNumberEnvelopeSchema("Successful login response.", "Phone number logged in successfully")
}

func getPhoneNumberResponseSchema() oas.Object {
	return phoneNumberEnvelopeSchema("Successful read response.", "Phone number retrieved successfully")
}

func listPhoneNumbersResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful phone number list response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Phone numbers retrieved successfully")).
		P("data", oas.Arr(oas.Ref("PhoneNumberResource"))).
		Build()
}

func phoneNumberEnvelopeSchema(desc, message string) oas.Object {
	return oas.Obj().Desc(desc).Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example(message)).
		P("data", oas.Ref("PhoneNumberResource")).
		P("links", phoneNumberLinksSchema()).
		Build()
}

func logoutPhoneNumberResponseSchema() oas.Object {
	return oas.Obj().Desc("Successful logout response.").Req("success", "message", "data").
		P("success", oas.Bool().Example(true)).
		P("message", oas.Str().Example("Phone number logged out and deleted successfully")).
		P("data", oas.Obj().Req("id", "disconnected", "deleted").
			P("id", oas.Str().Example("phone_number_12345")).
			P("disconnected", oas.Bool().Example(true)).
			P("deleted", oas.Bool().Example(true))).
		Build()
}

func componentSchemas() oas.Object {
	return oas.Object{
		"PhoneNumberResource":       phoneNumberResourceSchema(),
		"LoginPhoneNumberRequest":   loginPhoneNumberRequestSchema(),
		"LogoutPhoneNumberRequest":  logoutPhoneNumberRequestSchema(),
		"CreatePhoneNumberRequest":  createPhoneNumberRequestSchema(),
		"LoginPhoneNumberResponse":  loginPhoneNumberResponseSchema(),
		"GetPhoneNumberResponse":    getPhoneNumberResponseSchema(),
		"ListPhoneNumbersResponse":  listPhoneNumbersResponseSchema(),
		"LogoutPhoneNumberResponse": logoutPhoneNumberResponseSchema(),
	}
}
