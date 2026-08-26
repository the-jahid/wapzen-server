package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// AgentResourceExample is the persisted form of CreateAgentRequestExample: the
// submitted config echoed back with server-managed fields. Reused by the get,
// list and update response examples.
var AgentResourceExample = types.AgentResource{
	ID:                 "agent_12345",
	CreatedAt:          "2026-01-15T09:30:00Z",
	UpdatedAt:          "2026-01-15T09:30:00Z",
	CreateAgentRequest: CreateAgentRequestExample,
}

// CreateAgentResponseExample is the 201 envelope returned by POST /v1/agents.
var CreateAgentResponseExample = types.SuccessEnvelope{
	Success: true,
	Message: "Agent created successfully",
	Data:    AgentResourceExample,
	Links:   types.ResourceLinks{Self: "/v1/agents/agent_12345"},
}
