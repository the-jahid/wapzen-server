// Package outbound_campaigns registers the "Outbound Campaign" REST
// documentation. This package mutates the served OpenAPI document so the
// campaign operations and everything nested under a campaign — its leads, the
// calls it placed, and its analytics — render in Swagger UI.
//
// A campaign dials with an agent, and the agent carries the number it speaks on,
// so the documentation describes that pairing rather than a number of the
// campaign's own. Adding a lead is what puts a call on the wire, which is why
// the Create Campaign Lead response documents a call as well as a lead.
package outbound_campaigns

import "whatsapp-ai-caller-server/internal/swagger/oas"

const (
	tagName        = "Outbound Campaign"
	tagDescription = "Outbound campaigns, their leads, the calls they place, and their analytics."
)

// InjectStaticOutboundCampaignDocs mutates the passed OpenAPI document in place:
//   - appends the Outbound Campaign tag,
//   - adds the campaign paths and the nested lead, call and analytics paths,
//   - registers the component schemas the operations reference.
func InjectStaticOutboundCampaignDocs(document oas.OpenAPIObject) {
	appendTag(document, oas.Object{
		"name":        tagName,
		"description": tagDescription,
	})
	mergePaths(document, outboundCampaignPaths())
	mergePaths(document, campaignLeadPaths())
	registerComponentSchemas(document, componentSchemas())
	registerComponentSchemas(document, campaignLeadComponentSchemas())
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
