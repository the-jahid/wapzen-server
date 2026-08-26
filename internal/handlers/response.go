package handlers

import (
	"encoding/json"
	"net/http"

	"whatsapp-ai-caller-server/internal/middleware"
	"whatsapp-ai-caller-server/internal/models"
)

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// currentUser returns the authenticated user resolved by the auth middleware.
// Protected routes always carry one; reaching a handler without it means the
// route was registered outside the auth group, so the request is refused with
// a 401 (and false is returned so the handler can bail out).
func currentUser(w http.ResponseWriter, r *http.Request) (models.User, bool) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, models.APIResponse{
			Success: false,
			Message: "Authentication required",
		})
		return models.User{}, false
	}
	return user, true
}
