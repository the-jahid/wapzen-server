// Package calls registers the "Calls" REST documentation.
// This package only mutates the served OpenAPI document so the endpoints render
// in Swagger UI. It intentionally does not define request or response schemas.
package calls

import "whatsapp-ai-caller-server/internal/swagger/oas"

const (
	tagName        = "Calls"
	tagDescription = "Call endpoints."
)

// InjectStaticCallDocs mutates the passed OpenAPI document in place:
//   - appends the Calls tag,
//   - adds the Calls CRUD paths,
//   - registers the component schemas the Create Call operation references.
func InjectStaticCallDocs(document oas.OpenAPIObject) {
	appendTag(document, oas.Object{
		"name":        tagName,
		"description": tagDescription,
	})
	mergePaths(document, callPaths())
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
