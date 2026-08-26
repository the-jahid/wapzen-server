// Package oas holds the loosely-typed OpenAPI 3 primitives shared by the
// static Swagger document builder and the per-module document mutators.
//
// The project's real endpoints are documented with swaggo annotations, which
// emit a Swagger 2.0 spec. The static "Agents" docs, however, rely on
// OpenAPI 3 features (a bearer security scheme, requestBody, components,
// multiple named examples, style/explode query parameters), so the served
// document is assembled here as OpenAPI 3.0.3 and mutated in place by each
// module — mirroring a NestJS `injectStaticAgentDocs(document)` flow.
package oas

// OpenAPIObject is the whole OpenAPI 3 document. It is a reference type, so the
// module mutators receive it by value and still mutate the caller's document.
type OpenAPIObject = map[string]any

// Object is a generic JSON object used everywhere in the spec.
type Object = map[string]any

// APIV1Prefix is the shared version prefix applied to every documented path,
// so paths read as `/v1/agents`, `/v1/agents/{agent_id}`, etc.
const APIV1Prefix = "/v1"

// BearerSecurity is the per-operation security requirement referencing the
// `bearer` security scheme registered on the document's components.
func BearerSecurity() []any {
	return []any{Object{"bearer": []any{}}}
}

// Schema is a tiny fluent builder over an OpenAPI Schema Object. It keeps the
// (large) Agents schema readable without hand-writing nested map literals.
type Schema struct{ m Object }

func newSchema(typ string) *Schema {
	s := &Schema{Object{}}
	if typ != "" {
		s.m["type"] = typ
	}
	return s
}

// Str, Int, Num, Bool create scalar schemas.
func Str() *Schema  { return newSchema("string") }
func Int() *Schema  { return newSchema("integer") }
func Num() *Schema  { return newSchema("number") }
func Bool() *Schema { return newSchema("boolean") }

// Obj creates an object schema with an (initially empty) properties map.
func Obj() *Schema {
	s := newSchema("object")
	s.m["properties"] = Object{}
	return s
}

// Arr creates an array schema with the given item schema.
func Arr(item any) *Schema {
	s := newSchema("array")
	s.m["items"] = schemaOf(item)
	return s
}

// MapOf creates an object schema constrained to string keys mapping to the
// given value schema (OpenAPI `additionalProperties`).
func MapOf(value any) *Schema {
	s := newSchema("object")
	s.m["additionalProperties"] = schemaOf(value)
	return s
}

// Ref returns a `$ref` pointing at a component schema by name.
func Ref(name string) Object { return Object{"$ref": "#/components/schemas/" + name} }

// schemaOf unwraps a *Schema into its underlying map; plain maps pass through.
func schemaOf(v any) any {
	switch t := v.(type) {
	case *Schema:
		return t.m
	case map[string]any: // also matches the Object alias
		return t
	default:
		return v
	}
}

func (s *Schema) Desc(d string) *Schema    { s.m["description"] = d; return s }
func (s *Schema) Example(v any) *Schema     { s.m["example"] = v; return s }
func (s *Schema) Default(v any) *Schema      { s.m["default"] = v; return s }
func (s *Schema) Format(f string) *Schema    { s.m["format"] = f; return s }
func (s *Schema) Min(v float64) *Schema      { s.m["minimum"] = v; return s }
func (s *Schema) Max(v float64) *Schema      { s.m["maximum"] = v; return s }
func (s *Schema) Nullable() *Schema          { s.m["nullable"] = true; return s }

// Enum sets the allowed values from a string slice (the constants packages
// expose these as `…Options`).
func (s *Schema) Enum(values []string) *Schema {
	arr := make([]any, len(values))
	for i, v := range values {
		arr[i] = v
	}
	s.m["enum"] = arr
	return s
}

// P adds a property to an object schema.
func (s *Schema) P(name string, child any) *Schema {
	props, ok := s.m["properties"].(Object)
	if !ok {
		props = Object{}
		s.m["properties"] = props
	}
	props[name] = schemaOf(child)
	return s
}

// Req marks the given property names as required on an object schema.
func (s *Schema) Req(names ...string) *Schema {
	arr := make([]any, len(names))
	for i, n := range names {
		arr[i] = n
	}
	s.m["required"] = arr
	return s
}

// Build returns the underlying Object so the schema can be embedded in the
// document (components, parameters, responses, …).
func (s *Schema) Build() Object { return s.m }

// Properties returns the live properties map of an object schema. Used by the
// derived schemas (update / resource) that spread the create properties.
func (s *Schema) Properties() Object {
	props, _ := s.m["properties"].(Object)
	return props
}
