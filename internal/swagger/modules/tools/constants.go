package tools

import "whatsapp-ai-caller-server/internal/models"

// The tool types, re-exported from the models package so the documentation and
// the endpoints can never advertise different sets. The type decides which
// configuration block on the resource is read: everything else is ignored for
// that tool.
const (
	// ToolTypeAPIRequest calls an HTTP endpoint mid-call and hands the response
	// to the model, which speaks the useful part of it back to the caller.
	ToolTypeAPIRequest = models.ToolTypeAPIRequest
	// ToolTypeTransferCall announces the handover and releases the caller.
	ToolTypeTransferCall = models.ToolTypeTransferCall
	// ToolTypeEndCall hangs up.
	ToolTypeEndCall = models.ToolTypeEndCall
	// ToolTypeSendText sends a WhatsApp message to the caller during the call.
	ToolTypeSendText = models.ToolTypeSendText
)

// ToolTypes lists every tool type, in the order the dashboard offers them.
func ToolTypes() []string { return models.ToolTypes() }

// HTTPMethods are the verbs an api_request tool may use.
func HTTPMethods() []string { return models.ToolHTTPMethods() }

// ParameterTypes are the JSON types a tool parameter may declare. They are the
// types the model can reliably fill in from a spoken conversation, which is why
// there is no object or array here.
func ParameterTypes() []string { return models.ToolParameterTypes() }
