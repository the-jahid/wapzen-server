package swagger

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// TestEveryRefResolves walks the whole assembled document and asserts that every
// "$ref" points at a component schema that actually exists. A dangling ref still
// marshals to valid JSON, so nothing else here catches it — Swagger UI just
// renders the operation with an empty schema, which is how a documented endpoint
// silently stops describing itself after a schema is renamed or removed.
func TestEveryRefResolves(t *testing.T) {
	doc := BuildDocument()

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("document does not marshal to JSON: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("marshaled document is not valid JSON: %v", err)
	}

	components, _ := generic["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	if len(schemas) == 0 {
		t.Fatal("document has no component schemas")
	}

	refs := make(map[string]bool)
	collectRefs(generic, refs)
	if len(refs) == 0 {
		t.Fatal("document contains no $ref at all, which means this test is not looking where it thinks")
	}

	var dangling []string
	for ref := range refs {
		name, ok := strings.CutPrefix(ref, "#/components/schemas/")
		if !ok {
			dangling = append(dangling, ref+" (not a component schema reference)")
			continue
		}
		if _, defined := schemas[name]; !defined {
			dangling = append(dangling, ref)
		}
	}
	if len(dangling) > 0 {
		sort.Strings(dangling)
		t.Errorf("document references %d undefined schema(s):\n\t%s", len(dangling), strings.Join(dangling, "\n\t"))
	}
}

// collectRefs walks any decoded JSON value and records every "$ref" string value
// it finds, at any depth.
func collectRefs(node any, out map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "$ref" {
				if ref, ok := child.(string); ok {
					out[ref] = true
					continue
				}
			}
			collectRefs(child, out)
		}
	case []any:
		for _, child := range v {
			collectRefs(child, out)
		}
	}
}
