// Package tools registers the "Tools" REST documentation — the actions an agent
// can take mid-call: hit an HTTP endpoint, transfer the caller, hang up, send a
// WhatsApp text.
//
// This package only mutates the served OpenAPI document so the endpoints render
// in Swagger UI; the routes themselves are served by handlers.ToolHandler. The
// tool types, HTTP verbs and parameter types are re-exported from the models
// package (see constants.go) rather than restated, so what is documented and
// what is accepted cannot drift apart.
package tools

import "whatsapp-ai-caller-server/internal/swagger/oas"

const (
	tagName        = "Tools"
	tagDescription = "Tool CRUD endpoints. Tools are the actions an agent can take during a call."
)

// InjectStaticToolDocs mutates the passed OpenAPI document in place:
//   - appends the Tools tag,
//   - adds the Tools CRUD paths,
//   - registers the component schemas those operations reference.
func InjectStaticToolDocs(document oas.OpenAPIObject) {
	appendTag(document, oas.Object{
		"name":        tagName,
		"description": tagDescription,
	})
	mergePaths(document, toolPaths())
	registerComponentSchemas(document, componentSchemas())
}

func appendTag(document oas.OpenAPIObject, tag oas.Object) {
	existing, _ := document["tags"].([]any)
	document["tags"] = append(existing, tag)
}

func mergePaths(document oas.OpenAPIObject, paths oas.Object) {
	target, ok := document["paths"].(oas.Object)
	if !ok {
		target = oas.Object{}
		document["paths"] = target
	}
	for path, item := range paths {
		if existing, ok := target[path].(oas.Object); ok {
			if newItem, ok := item.(oas.Object); ok {
				for method, op := range newItem {
					existing[method] = op
				}
				continue
			}
		}
		target[path] = item
	}
}

func registerComponentSchemas(document oas.OpenAPIObject, schemas oas.Object) {
	components, ok := document["components"].(oas.Object)
	if !ok {
		components = oas.Object{}
		document["components"] = components
	}
	target, ok := components["schemas"].(oas.Object)
	if !ok {
		target = oas.Object{}
		components["schemas"] = target
	}
	for name, schema := range schemas {
		target[name] = schema
	}
}
