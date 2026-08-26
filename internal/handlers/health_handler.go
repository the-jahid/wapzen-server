package handlers

import (
	"net/http"

	"whatsapp-ai-caller-server/internal/models"
)

// HealthCheck reports whether the server is up and running.
//
// HealthCheck godoc
// @Summary      Health check
// @Description  Returns the current status of the server.
// @Tags         health
// @Produce      json
// @Success      200  {object}  models.APIResponse
// @Router       /health [get]
func HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "Server is running",
	})
}
