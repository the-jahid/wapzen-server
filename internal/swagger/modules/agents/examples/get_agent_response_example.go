package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// GetAgentResponseExample is the 200 envelope returned by GET /v1/agents/{agent_id}.
var GetAgentResponseExample = types.SuccessEnvelope{
	Success: true,
	Message: "Agent retrieved successfully",
	Data:    AgentResourceExample,
	Links:   types.ResourceLinks{Self: "/v1/agents/agent_12345"},
}
