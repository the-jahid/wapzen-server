package handlers

import (
	"net/http"

	"whatsapp-ai-caller-server/internal/models"
)

// UserHandler handles user resolution endpoints.
type UserHandler struct{}

// NewUserHandler creates a user handler.
func NewUserHandler() *UserHandler {
	return &UserHandler{}
}

// userEnvelope is the documented success envelope for a single user resource.
type userEnvelope struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    models.User `json:"data"`
}

// Me returns the authenticated application user. The auth middleware has
// already verified the Clerk token and resolved (or just-in-time provisioned)
// the user row, so this endpoint is how the frontend learns its identity —
// replacing the old unauthenticated POST /v1/users/sync.
//
// Me godoc
// @Summary      Get current user
// @Description  Returns the application user for the authenticated Clerk session, including the internal user id.
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  handlers.userEnvelope
// @Failure      401  {object}  models.APIResponse
// @Router       /v1/users/me [get]
func (h *UserHandler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, userEnvelope{
		Success: true,
		Message: "User retrieved successfully",
		Data:    user,
	})
}
