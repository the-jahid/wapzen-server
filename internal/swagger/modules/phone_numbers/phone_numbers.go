// Package phone_numbers registers the "phone-number" REST documentation.
// This package only mutates the served OpenAPI document so the endpoints render
// in Swagger UI with schemas, examples and status codes.
package phone_numbers

import "whatsapp-ai-caller-server/internal/swagger/oas"

const (
	tagName        = "phone-number"
	tagDescription = "WhatsApp phone number QR login endpoints."
)

// InjectStaticPhoneNumberDocs mutates the passed OpenAPI document in place:
//   - appends the phone-number tag,
//   - adds the phone-number QR login paths,
//   - registers the component schemas the operations reference.
func InjectStaticPhoneNumberDocs(document oas.OpenAPIObject) {
	appendTag(document, oas.Object{
		"name":        tagName,
		"description": tagDescription,
	})
	mergePaths(document, phoneNumberPaths())
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
