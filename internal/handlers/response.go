package handlers

import "whatsapp-ai-caller-server/internal/httpx"

// The response helpers are shared with the feature packages that serve their
// own routes (see internal/agents), so they live in internal/httpx; these keep
// the short local names every handler in this package uses.
var (
	writeJSON           = httpx.WriteJSON
	currentUser         = httpx.CurrentUser
	buildPaginationMeta = httpx.PaginationMeta
	buildListLinks      = httpx.ListLinks
)
