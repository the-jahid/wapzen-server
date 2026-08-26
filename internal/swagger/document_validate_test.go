package swagger

import (
	"encoding/json"
	"testing"
)

// TestBuildDocumentStructure validates that the assembled OpenAPI 3 document is
// valid JSON and carries the static Agents docs: the tag (with its exact
// description), the 5 paths, the component schemas, the bearer security
// schemes, and the two named list examples.
func TestBuildDocumentStructure(t *testing.T) {
	b, err := json.Marshal(BuildDocument())
	if err != nil {
		t.Fatalf("document does not marshal to JSON: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("marshaled document is not valid JSON: %v", err)
	}

	if got := doc["openapi"]; got != "3.0.3" {
		t.Errorf("openapi version = %v, want 3.0.3", got)
	}

	// --- Agents tag with the exact description. ---
	tags, _ := doc["tags"].([]any)
	var agentsTag map[string]any
	for _, raw := range tags {
		tag, _ := raw.(map[string]any)
		if tag["name"] == "Agents" {
			agentsTag = tag
		}
	}
	if agentsTag == nil {
		t.Fatal("Agents tag not found")
	}
	const wantDesc = "Agent CRUD endpoints backed by live Go routes."
	if got := agentsTag["description"]; got != wantDesc {
		t.Errorf("Agents tag description = %q, want %q", got, wantDesc)
	}
	var phoneNumberTag map[string]any
	for _, raw := range tags {
		tag, _ := raw.(map[string]any)
		if tag["name"] == "phone-number" {
			phoneNumberTag = tag
		}
	}
	if phoneNumberTag == nil {
		t.Fatal("phone-number tag not found")
	}
	var callsTag map[string]any
	for _, raw := range tags {
		tag, _ := raw.(map[string]any)
		if tag["name"] == "Calls" {
			callsTag = tag
		}
	}
	if callsTag == nil {
		t.Fatal("Calls tag not found")
	}

	// --- 5 Agents operations across the 2 paths. ---
	paths, _ := doc["paths"].(map[string]any)
	collection, _ := paths["/v1/agents"].(map[string]any)
	if collection == nil {
		t.Fatal("path /v1/agents not found")
	}
	for _, m := range []string{"get", "post"} {
		if _, ok := collection[m]; !ok {
			t.Errorf("/v1/agents missing %s operation", m)
		}
	}
	item, _ := paths["/v1/agents/{agent_id}"].(map[string]any)
	if item == nil {
		t.Fatal("path /v1/agents/{agent_id} not found")
	}
	for _, m := range []string{"get", "patch", "delete"} {
		if _, ok := item[m]; !ok {
			t.Errorf("/v1/agents/{agent_id} missing %s operation", m)
		}
	}
	phoneNumberCollection, _ := paths["/v1/phone-number"].(map[string]any)
	if phoneNumberCollection == nil {
		t.Fatal("path /v1/phone-number not found")
	}
	if _, ok := phoneNumberCollection["get"]; !ok {
		t.Error("/v1/phone-number missing get operation")
	}
	assertOperationSecurity(t, phoneNumberCollection, "get", "apiKeyBearer")
	phoneNumberLogin, _ := paths["/v1/phone-number/login"].(map[string]any)
	if phoneNumberLogin == nil {
		t.Fatal("path /v1/phone-number/login not found")
	}
	if _, ok := phoneNumberLogin["post"]; !ok {
		t.Error("/v1/phone-number/login missing post operation")
	}
	assertOperationSecurity(t, phoneNumberLogin, "post", "apiKeyBearer")
	phoneNumberLogout, _ := paths["/v1/phone-number/logout"].(map[string]any)
	if phoneNumberLogout == nil {
		t.Fatal("path /v1/phone-number/logout not found")
	}
	if _, ok := phoneNumberLogout["post"]; !ok {
		t.Error("/v1/phone-number/logout missing post operation")
	}
	assertOperationSecurity(t, phoneNumberLogout, "post", "apiKeyBearer")
	phoneNumberItem, _ := paths["/v1/phone-number/{phone_number_id}"].(map[string]any)
	if phoneNumberItem == nil {
		t.Fatal("path /v1/phone-number/{phone_number_id} not found")
	}
	if _, ok := phoneNumberItem["get"]; !ok {
		t.Error("/v1/phone-number/{phone_number_id} missing get operation")
	}
	assertOperationSecurity(t, phoneNumberItem, "get", "apiKeyBearer")
	callsCollection, _ := paths["/v1/calls"].(map[string]any)
	if callsCollection == nil {
		t.Fatal("path /v1/calls not found")
	}
	for _, m := range []string{"get", "post"} {
		if _, ok := callsCollection[m]; !ok {
			t.Errorf("/v1/calls missing %s operation", m)
		}
		assertOperationSecurity(t, callsCollection, m, "apiKeyBearer")
	}
	callsItem, _ := paths["/v1/calls/{call_id}"].(map[string]any)
	if callsItem == nil {
		t.Fatal("path /v1/calls/{call_id} not found")
	}
	for _, m := range []string{"get", "patch", "delete"} {
		if _, ok := callsItem[m]; !ok {
			t.Errorf("/v1/calls/{call_id} missing %s operation", m)
		}
		assertOperationSecurity(t, callsItem, m, "apiKeyBearer")
	}

	// Make sure the ported base endpoints survived the merge.
	for _, p := range []string{"/health", "/api/webhooks/clerk", "/v1/api-keys", "/v1/api-keys/{api_key_id}", "/v1/api-keys/{api_key_id}/default"} {
		if _, ok := paths[p]; !ok {
			t.Errorf("base path %s missing after merge", p)
		}
	}

	// --- Components: schemas + bearer security scheme. ---
	components, _ := doc["components"].(map[string]any)
	if components == nil {
		t.Fatal("components missing")
	}
	schemas, _ := components["schemas"].(map[string]any)
	for _, name := range []string{
		"CreateAgentRequest", "UpdateAgentRequest", "AgentResource",
		"CreateAgentResponse", "GetAgentResponse", "ListAgentsResponse",
		"DeleteAgentResponse", "ErrorResponse",
		"APIKey", "CreatedAPIKey", "CreateAPIKeyRequest",
		"ListAPIKeysResponse", "CreateAPIKeyResponse", "SetDefaultAPIKeyResponse", "RevokeAPIKeyResponse",
		"PhoneNumberResource", "LoginPhoneNumberRequest", "LogoutPhoneNumberRequest",
		"LoginPhoneNumberResponse", "GetPhoneNumberResponse", "ListPhoneNumbersResponse", "LogoutPhoneNumberResponse",
	} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("components.schemas missing %s", name)
		}
	}
	secSchemes, _ := components["securitySchemes"].(map[string]any)
	bearer, _ := secSchemes["bearer"].(map[string]any)
	if bearer == nil {
		t.Fatal("components.securitySchemes.bearer missing")
	}
	if bearer["type"] != "http" || bearer["scheme"] != "bearer" {
		t.Errorf("bearer scheme = %v, want http/bearer", bearer)
	}
	apiKeyBearer, _ := secSchemes["apiKeyBearer"].(map[string]any)
	if apiKeyBearer == nil {
		t.Fatal("components.securitySchemes.apiKeyBearer missing")
	}
	if apiKeyBearer["type"] != "http" || apiKeyBearer["scheme"] != "bearer" {
		t.Errorf("apiKeyBearer scheme = %v, want http/bearer", apiKeyBearer)
	}

	// --- Two named list examples on GET /v1/agents 200. ---
	listGet, _ := collection["get"].(map[string]any)
	security, _ := listGet["security"].([]any)
	if len(security) != 1 {
		t.Fatalf("GET /v1/agents security = %v, want exactly one requirement", security)
	}
	requirement, _ := security[0].(map[string]any)
	if _, ok := requirement["apiKeyBearer"]; !ok {
		t.Errorf("GET /v1/agents security = %v, want apiKeyBearer", security)
	}
	responses, _ := listGet["responses"].(map[string]any)
	ok200, _ := responses["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	appJSON, _ := content["application/json"].(map[string]any)
	exs, _ := appJSON["examples"].(map[string]any)
	for _, name := range []string{"allFields", "selectedFields"} {
		if _, ok := exs[name]; !ok {
			t.Errorf("GET /v1/agents 200 missing named example %q", name)
		}
	}

	// --- $ref integrity: every #/components/schemas/X ref must resolve. ---
	checkRefs(t, doc, schemas)
}

func assertOperationSecurity(t *testing.T, pathItem map[string]any, method, scheme string) {
	t.Helper()

	operation, _ := pathItem[method].(map[string]any)
	if operation == nil {
		t.Fatalf("%s operation missing", method)
	}
	security, _ := operation["security"].([]any)
	if len(security) != 1 {
		t.Fatalf("%s security = %v, want exactly one requirement", method, security)
	}
	requirement, _ := security[0].(map[string]any)
	if _, ok := requirement[scheme]; !ok {
		t.Errorf("%s security = %v, want %s", method, security, scheme)
	}
}

// checkRefs walks the document and verifies each "$ref" into
// #/components/schemas/ points at a registered schema.
func checkRefs(t *testing.T, node any, schemas map[string]any) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if k == "$ref" {
				if ref, ok := v.(string); ok {
					const prefix = "#/components/schemas/"
					if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
						name := ref[len(prefix):]
						if _, ok := schemas[name]; !ok {
							t.Errorf("dangling $ref %q", ref)
						}
					}
				}
				continue
			}
			checkRefs(t, v, schemas)
		}
	case []any:
		for _, v := range n {
			checkRefs(t, v, schemas)
		}
	}
}
