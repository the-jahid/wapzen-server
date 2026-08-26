package examples

import "whatsapp-ai-caller-server/internal/swagger/modules/agents/types"

// PaginationMetaExample is a sample collection meta block (page 1 of 3).
var PaginationMetaExample = types.Meta{
	Pagination: types.Pagination{
		Page:            1,
		Limit:           20,
		TotalItems:      42,
		TotalPages:      3,
		HasNextPage:     true,
		HasPreviousPage: false,
	},
}

// ListLinksExample is a sample collection link block matching PaginationMetaExample.
var ListLinksExample = types.ListLinks{
	Self:     "/v1/agents?page=1&limit=20",
	First:    "/v1/agents?page=1&limit=20",
	Previous: nil,
	Next:     strptr("/v1/agents?page=2&limit=20"),
	Last:     "/v1/agents?page=3&limit=20",
}

// ListAgentsResponseExample is the 200 envelope returned by GET /v1/agents when
// no `fields` filter is supplied (full resources).
var ListAgentsResponseExample = types.SuccessEnvelope{
	Success: true,
	Message: "Agents retrieved successfully",
	Data:    []types.AgentResource{AgentResourceExample},
	Meta:    &PaginationMetaExample,
	Links:   ListLinksExample,
}

// listLinksSelectedExample mirrors ListLinksExample but carries the active
// `fields` selection in each URL.
var listLinksSelectedExample = types.ListLinks{
	Self:     "/v1/agents?page=1&limit=20&fields=id,agent.status,agent.language,agent.call_direction",
	First:    "/v1/agents?page=1&limit=20&fields=id,agent.status,agent.language,agent.call_direction",
	Previous: nil,
	Next:     strptr("/v1/agents?page=2&limit=20&fields=id,agent.status,agent.language,agent.call_direction"),
	Last:     "/v1/agents?page=3&limit=20&fields=id,agent.status,agent.language,agent.call_direction",
}

// ListAgentsSelectedFieldsExample is the 200 envelope returned by GET /v1/agents
// with `fields=id,agent.status,agent.language,agent.call_direction`: each resource is
// trimmed to exactly the requested (dot-notation) fields.
var ListAgentsSelectedFieldsExample = types.SuccessEnvelope{
	Success: true,
	Message: "Agents retrieved successfully",
	Data: []map[string]any{
		{
			"id": "agent_12345",
			"agent": map[string]any{
				"status":         "active",
				"language":       "en-US",
				"call_direction": "outbound",
			},
		},
	},
	Meta:  &PaginationMetaExample,
	Links: listLinksSelectedExample,
}
