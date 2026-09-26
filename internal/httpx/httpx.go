// Package httpx holds the HTTP response helpers shared by every controller: the
// JSON writer, the authenticated-user lookup, and the pagination blocks of the
// documented list envelope. It lives apart from internal/handlers so feature
// packages that serve their own routes (internal/agents) can use them without
// an import cycle.
package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"whatsapp-ai-caller-server/internal/middleware"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// WriteJSON writes payload as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteError writes the documented failure envelope.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, models.APIResponse{Success: false, Message: message})
}

// CurrentUser returns the authenticated user resolved by the auth middleware.
// Protected routes always carry one; reaching a handler without it means the
// route was registered outside the auth group, so the request is refused with a
// 401 (and false is returned so the handler can bail out).
func CurrentUser(w http.ResponseWriter, r *http.Request) (models.User, bool) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return models.User{}, false
	}
	return user, true
}

// PaginationMeta computes the pagination block for the current page.
func PaginationMeta(page, limit, total int) types.Meta {
	pages := totalPages(total, limit)
	return types.Meta{
		Pagination: types.Pagination{
			Page:            page,
			Limit:           limit,
			TotalItems:      total,
			TotalPages:      pages,
			HasNextPage:     page < pages,
			HasPreviousPage: page > 1,
		},
	}
}

// ListLinks builds the HATEOAS link block for the collection at basePath,
// preserving the sparse fieldset on every page URL. Previous/Next are null at
// the respective ends of the collection. Scoping needs no query parameter: it
// follows the bearer token.
func ListLinks(basePath string, page, limit, total int, fields []string) types.ListLinks {
	pages := totalPages(total, limit)
	last := max(pages, 1)

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
