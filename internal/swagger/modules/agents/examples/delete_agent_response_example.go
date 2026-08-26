package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// DeleteAgentResponseExample is the 200 envelope returned by DELETE.
var DeleteAgentResponseExample = types.SuccessEnvelope{
	Success: true,
	Message: "Agent deleted successfully",
	Data: types.DeleteData{
		ID:      "agent_12345",
		Deleted: true,
	},
}
