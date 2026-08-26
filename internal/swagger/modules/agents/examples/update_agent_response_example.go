package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// UpdateAgentResponseExample is the 200 envelope returned by PATCH: the full
// resource reflecting the applied changes, with a bumped updated_at.
var UpdateAgentResponseExample = buildUpdateAgentResponseExample()

func buildUpdateAgentResponseExample() types.SuccessEnvelope {
	// Copy the config and override only the changed sections with fresh
	// pointers, so the shared AgentResourceExample is never mutated.
	config := CreateAgentRequestExample
	tz := "Asia/Dhaka"
	config.Agent = &types.AgentSection{
		Name:          "Jarvis (Updated)",
		Language:      "en-US",
		Timezone:      &tz,
		PhoneNumberID: nil,
		CallDirection: "outbound",
		Status:        "active",
	}
	config.Model = &types.ModelSection{
		Provider:    "openai",
		Name:        "gpt-4.1-mini",
		Temperature: 0.4,
	}

	resource := types.AgentResource{
		ID:                 "agent_12345",
		CreatedAt:          "2026-01-15T09:30:00Z",
		UpdatedAt:          "2026-01-15T10:05:00Z",
		CreateAgentRequest: config,
	}

	return types.SuccessEnvelope{
		Success: true,
		Message: "Agent updated successfully",
		Data:    resource,
		Links:   types.ResourceLinks{Self: "/v1/agents/agent_12345"},
	}
}
