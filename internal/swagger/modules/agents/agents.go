// Package agents registers the "Agents" REST documentation. The live routes
// are registered in routes.NewRouter — this package only mutates the served
// OpenAPI document so the endpoints render in Swagger UI with full schemas,
// examples and status codes. This is the Go analogue of a NestJS
// `injectStaticAgentDocs(document)` document mutator.
package agents

import "whatsapp-ai-caller-server/internal/swagger/oas"

const (
	// tagName groups every Agents operation under one Swagger section.
	tagName = "Agents"
	// tagDescription describes the Agents section in Swagger UI. Every
	// documented operation is served by a live Go route (see routes.NewRouter).
	tagDescription = "Agent CRUD endpoints backed by live Go routes."
)

// InjectStaticAgentDocs mutates the passed OpenAPI document in place:
//   - appends the Agents tag,
//   - adds the 5 Agents paths (preserving any existing paths),
//   - registers the component schemas the operations reference.
//
// It is wired into the Swagger setup after the base document is built and
// before the spec is served.
func InjectStaticAgentDocs(document oas.OpenAPIObject) {
	appendTag(document, oas.Object{
		"name":        tagName,
		"description": tagDescription,
	})
	mergePaths(document, agentPaths())
	registerComponentSchemas(document, componentSchemas())
}

// appendTag appends a tag, creating the tags slice if necessary.
func appendTag(document oas.OpenAPIObject, tag oas.Object) {
	existing, _ := document["tags"].([]any)
	document["tags"] = append(existing, tag)
}

// mergePaths copies each path into document.paths without clobbering existing
// paths (and merges operations into a shared path item if one already exists).
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

// registerComponentSchemas adds schemas under components.schemas, creating the
// components/schemas objects if needed and preserving existing schemas.
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
