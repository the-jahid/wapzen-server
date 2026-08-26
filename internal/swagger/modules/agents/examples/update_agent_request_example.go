package examples

// UpdateAgentRequestExample is a partial update payload: only the fields being
// changed are sent. It is expressed as an explicit map (rather than the typed
// CreateAgentRequest) so unset fields are genuinely omitted — a typed struct
// would render always-present nullable fields like agent.timezone as null.
var UpdateAgentRequestExample = map[string]any{
	"agent": map[string]any{
		"name": "Jarvis (Updated)",
	},
	"model": map[string]any{
		"temperature": 0.4,
	},
	"knowledge_base": map[string]any{
		"knowledge_base_ids": []string{"knowledge_base_a456426614174000"},
	},
}
