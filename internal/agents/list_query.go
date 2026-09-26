package agents

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/constants"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// Default and bound values for the list query parameters. These mirror the
// documented schema for GET /v1/agents (page ≥ 1 default 1; limit 1..100
// default 20).
const (
	defaultPage  = 1
	defaultLimit = 20
	maxLimit     = 100
)

// selectableFields is the lookup form of constants.SelectableFieldsOptions,
// used to validate each entry of the `fields` sparse-fieldset parameter.
var selectableFields = func() map[string]struct{} {
	set := make(map[string]struct{}, len(constants.SelectableFieldsOptions))
	for _, f := range constants.SelectableFieldsOptions {
		set[f] = struct{}{}
	}
	return set
}()

// listQuery is the validated page/limit/fields of a list request.
type listQuery struct {
	page   int
	limit  int
	fields []string
}

// parseListQuery reads and validates the page/limit/fields query parameters,
// returning the effective values plus any per-field validation errors. Absent
// parameters fall back to their documented defaults; fields preserves request
// order and contains only recognized selectable fields.
func parseListQuery(q url.Values) (listQuery, []types.FieldError) {
	query := listQuery{page: defaultPage, limit: defaultLimit}
	var errs []types.FieldError

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err != nil || p < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			query.page = p
		}
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if l, err := strconv.Atoi(raw); err != nil || l < 1 || l > maxLimit {
			errs = append(errs, types.FieldError{Field: "limit", Message: fmt.Sprintf("limit must be between 1 and %d", maxLimit)})
		} else {
			query.limit = l
		}
	}

	if raw := strings.TrimSpace(q.Get("fields")); raw != "" {
		for _, f := range strings.Split(raw, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if _, ok := selectableFields[f]; !ok {
				errs = append(errs, types.FieldError{
					Field:   "fields",
					Message: fmt.Sprintf("unknown field '%s' in fields selection", f),
				})
				continue
			}
			query.fields = append(query.fields, f)
		}
	}

	return query, errs
}

// applyFieldSelection trims each resource to the requested sparse fieldset. When
// no fields are requested it returns the full resources unchanged (always as a
// non-nil slice so an empty page serializes as [] rather than null).
func applyFieldSelection(resources []types.AgentResource, fields []string) (any, error) {
	if len(fields) == 0 {
		if resources == nil {
			return []types.AgentResource{}, nil
		}
		return resources, nil
	}

	out := make([]map[string]any, 0, len(resources))
	for _, res := range resources {
		full, err := toMap(res)
		if err != nil {
			return nil, err
		}
		out = append(out, selectFields(full, fields))
	}
	return out, nil
}

// selectFields projects the requested (dot-notation) fields out of a fully
// serialized resource. A bare key (e.g. "id", "agent") copies the whole value;
// a "section.property" key copies just that nested property into the section.
func selectFields(full map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		section, prop, nested := strings.Cut(f, ".")
		if !nested {
			if v, ok := full[section]; ok {
				out[section] = v
			}
			continue
		}

		sec, ok := full[section].(map[string]any)
		if !ok {
			continue
		}
		val, ok := sec[prop]
		if !ok {
			continue
		}
		dst, ok := out[section].(map[string]any)
		if !ok {
			dst = make(map[string]any)
			out[section] = dst
		}
		dst[prop] = val
	}
	return out
}

// toMap round-trips a value through JSON so its keys match the resource's JSON
// tags exactly — keeping the sparse-fieldset projection in sync with the wire
// contract without duplicating field names.
func toMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
