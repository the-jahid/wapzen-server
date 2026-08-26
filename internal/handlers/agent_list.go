package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"whatsapp-ai-caller-server/internal/models"
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

// agentsListPath is the collection URL rendered into the agents list links.
const agentsListPath = "/v1/agents"

// selectableFieldSet is the lookup form of constants.SelectableFieldsOptions,
// used to validate each entry of the `fields` sparse-fieldset parameter.
var selectableFieldSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(constants.SelectableFieldsOptions))
	for _, f := range constants.SelectableFieldsOptions {
		set[f] = struct{}{}
	}
	return set
}()

// List returns a paginated page of the authenticated user's agents, optionally
// trimmed to a sparse fieldset.
//
// List godoc
// @Summary      Get All Agents
// @Description  Returns a paginated list of the agents owned by the authenticated user. Supports sparse fieldsets via the fields query parameter (dot-notation selects a nested property, e.g. agent.language).
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        page    query     int     false  "Page number (1-based)"
// @Param        limit   query     int     false  "Items per page (1-100)"
// @Param        fields  query     string  false  "Comma-separated sparse fieldset"
// @Success      200     {object}  types.SuccessEnvelope
// @Failure      400     {object}  types.ErrorEnvelope
// @Failure      401     {object}  models.APIResponse
// @Failure      500     {object}  models.APIResponse
// @Router       /v1/agents [get]
func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	page, limit, fields, fieldErrs := parseListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	resources, total, err := h.repo.List(r.Context(), user.ID, limit, (page-1)*limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list agents",
		})
		return
	}

	data, err := applyFieldSelection(resources, fields)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list agents",
		})
		return
	}

	meta := buildPaginationMeta(page, limit, total)
	links := buildListLinks(agentsListPath, page, limit, total, fields)

	writeJSON(w, http.StatusOK, types.SuccessEnvelope{
		Success: true,
		Message: "Agents retrieved successfully",
		Data:    data,
		Meta:    &meta,
		Links:   links,
	})
}

// parseListQuery reads and validates the page/limit/fields query parameters,
// returning the effective values plus any per-field validation errors. Absent
// parameters fall back to their documented defaults; the returned fields slice
// preserves request order and contains only recognized selectable fields.
func parseListQuery(q url.Values) (page, limit int, fields []string, errs []types.FieldError) {
	page, limit = defaultPage, defaultLimit

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err != nil || p < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			page = p
		}
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if l, err := strconv.Atoi(raw); err != nil || l < 1 || l > maxLimit {
			errs = append(errs, types.FieldError{Field: "limit", Message: fmt.Sprintf("limit must be between 1 and %d", maxLimit)})
		} else {
			limit = l
		}
	}

	if raw := strings.TrimSpace(q.Get("fields")); raw != "" {
		for _, f := range strings.Split(raw, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if _, ok := selectableFieldSet[f]; !ok {
				errs = append(errs, types.FieldError{
					Field:   "fields",
					Message: fmt.Sprintf("unknown field '%s' in fields selection", f),
				})
				continue
			}
			fields = append(fields, f)
		}
	}

	return page, limit, fields, errs
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

// buildPaginationMeta computes the pagination block for the current page.
func buildPaginationMeta(page, limit, total int) types.Meta {
	return types.Meta{
		Pagination: types.Pagination{
			Page:            page,
			Limit:           limit,
			TotalItems:      total,
			TotalPages:      totalPages(total, limit),
			HasNextPage:     page < totalPages(total, limit),
			HasPreviousPage: page > 1,
		},
	}
}

// buildListLinks builds the HATEOAS link block for the collection at basePath,
// preserving the sparse fieldset on every page URL. Previous/Next are null at
// the respective ends of the collection. Scoping needs no query parameter: it
// follows the bearer token.
func buildListLinks(basePath string, page, limit, total int, fields []string) types.ListLinks {
	pages := totalPages(total, limit)
	last := pages
	if last < 1 {
		last = 1
	}

	links := types.ListLinks{
		Self:  listURL(basePath, page, limit, fields),
		First: listURL(basePath, 1, limit, fields),
		Last:  listURL(basePath, last, limit, fields),
	}
	if page > 1 {
		prev := listURL(basePath, page-1, limit, fields)
		links.Previous = &prev
	}
	if page < pages {
		next := listURL(basePath, page+1, limit, fields)
		links.Next = &next
	}
	return links
}

// listURL renders a collection URL for the given page, carrying the fieldset
// query parameter when present. The comma-separated fields list is emitted
// verbatim (unescaped) to match the documented link format.
func listURL(basePath string, page, limit int, fields []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s?page=%d&limit=%d", basePath, page, limit)
	if len(fields) > 0 {
		b.WriteString("&fields=")
		b.WriteString(strings.Join(fields, ","))
	}
	return b.String()
}

// totalPages is the number of pages needed to hold total items at the given
// page size (0 when there are no items).
func totalPages(total, limit int) int {
	if total <= 0 || limit <= 0 {
		return 0
	}
	return (total + limit - 1) / limit
}
