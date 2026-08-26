// Package knowledge_base registers the "Knowledge Base" REST documentation.
// This package only mutates the served OpenAPI document so the endpoints render
// in Swagger UI. Every operation is implemented (handlers.KnowledgeBaseHandler)
// except Delete Knowledge Base Source, which is documented ahead of its
// handler. Add Knowledge Base Sources indexes raw texts only for now; its url
// and file fields are documented as rejected until their fetchers land.
package knowledge_base

import "whatsapp-ai-caller-server/internal/swagger/oas"

const (
	tagName        = "Knowledge Base"
	tagDescription = "Knowledge base and knowledge base source endpoints."
)

// InjectStaticKnowledgeBaseDocs mutates the passed OpenAPI document in place:
//   - appends the Knowledge Base tag,
//   - adds the knowledge base and source paths,
//   - registers the component schemas the operations reference.
func InjectStaticKnowledgeBaseDocs(document oas.OpenAPIObject) {
	appendTag(document, oas.Object{
		"name":        tagName,
		"description": tagDescription,
	})
	mergePaths(document, knowledgeBasePaths())
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
