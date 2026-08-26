package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/apikeys"
	"whatsapp-ai-caller-server/internal/models"
)

// APIKeyHandler handles authenticated API-key management endpoints.
type APIKeyHandler struct {
	repo *apikeys.Repository
}

// NewAPIKeyHandler creates an API-key handler with its dependencies.
func NewAPIKeyHandler(repo *apikeys.Repository) *APIKeyHandler {
	return &APIKeyHandler{repo: repo}
}

type createAPIKeyRequest struct {
	Name string `json:"name" example:"Production"`
}

type createdAPIKeyEnvelope struct {
	Success bool                 `json:"success"`
	Message string               `json:"message"`
	Data    models.CreatedAPIKey `json:"data"`
}

type apiKeyListEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    []models.APIKey `json:"data"`
}

type defaultAPIKeyEnvelope struct {
	Success bool          `json:"success"`
	Message string        `json:"message"`
	Data    models.APIKey `json:"data"`
}

type revokedAPIKeyEnvelope struct {
	Success bool              `json:"success"`
	Message string            `json:"message"`
	Data    revokedAPIKeyData `json:"data"`
}

type revokedAPIKeyData struct {
	ID      string `json:"id" example:"8c3f910d-4f81-4e8e-a4e0-54a9c9d0e55f"`
	Revoked bool   `json:"revoked" example:"true"`
}

// List returns the authenticated user's active API keys.
//
// List godoc
// @Summary      List API keys
// @Description  Returns metadata for the authenticated user's active API keys. Secrets are never returned after creation.
// @Tags         apiKeys
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  handlers.apiKeyListEnvelope
// @Failure      401  {object}  models.APIResponse
// @Failure      500  {object}  models.APIResponse
// @Router       /v1/api-keys [get]
func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	keys, err := h.repo.List(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list api keys",
		})
		return
	}

	writeJSON(w, http.StatusOK, apiKeyListEnvelope{
		Success: true,
		Message: "API keys retrieved successfully",
		Data:    keys,
	})
}

// Create creates a new API key owned by the authenticated user.
//
// Create godoc
// @Summary      Create API key
// @Description  Creates a new API key for the authenticated user and returns the bearer secret once. Store the key securely; future list responses only include metadata.
// @Tags         apiKeys
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.createAPIKeyRequest  false  "API key name"
// @Success      201   {object}  handlers.createdAPIKeyEnvelope
// @Failure      400   {object}  models.APIResponse
// @Failure      401   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/api-keys [post]
func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	req, ok := decodeCreateAPIKeyRequest(w, r)
	if !ok {
		return
	}

	created, err := h.repo.Create(r.Context(), user.ID, req.Name)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23514":
				writeJSON(w, http.StatusBadRequest, models.APIResponse{
					Success: false,
					Message: "name must be 80 characters or fewer",
				})
				return
			case "23503":
				writeJSON(w, http.StatusNotFound, models.APIResponse{
					Success: false,
					Message: "user not found",
				})
				return
			}
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to create api key",
		})
		return
	}

	writeJSON(w, http.StatusCreated, createdAPIKeyEnvelope{
		Success: true,
		Message: "API key created successfully",
		Data:    created,
	})
}

// Revoke revokes a user-owned API key.
//
// Revoke godoc
// @Summary      Delete API key
// @Description  Deletes an API key owned by the authenticated user. Deleted keys can no longer be used for bearer authentication. A user cannot delete their only remaining active API key.
// @Tags         apiKeys
// @Produce      json
// @Security     BearerAuth
// @Param        api_key_id  path      string  true  "API key id"
// @Success      200         {object}  handlers.revokedAPIKeyEnvelope
// @Failure      400         {object}  models.APIResponse
// @Failure      401         {object}  models.APIResponse
// @Failure      404         {object}  models.APIResponse
// @Failure      409         {object}  models.APIResponse
// @Failure      500         {object}  models.APIResponse
// @Router       /v1/api-keys/{api_key_id} [delete]
func (h *APIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	keyID := strings.TrimSpace(chi.URLParam(r, "api_key_id"))
	if keyID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "api_key_id path parameter is required",
		})
		return
	}

	if err := h.repo.Revoke(r.Context(), user.ID, keyID); err != nil {
		if errors.Is(err, apikeys.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "api key not found",
			})
			return
		}
		if errors.Is(err, apikeys.ErrLastActiveKey) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "cannot delete the last api key",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to delete api key",
		})
		return
	}

	writeJSON(w, http.StatusOK, revokedAPIKeyEnvelope{
		Success: true,
		Message: "API key deleted successfully",
		Data:    revokedAPIKeyData{ID: keyID, Revoked: true},
	})
}

// SetDefault marks a user-owned API key as the default key.
//
// SetDefault godoc
// @Summary      Set default API key
// @Description  Marks an active API key owned by the authenticated user as the default API key. Exactly one active key is default per user.
// @Tags         apiKeys
// @Produce      json
// @Security     BearerAuth
// @Param        api_key_id  path      string  true  "API key id"
// @Success      200         {object}  handlers.defaultAPIKeyEnvelope
// @Failure      400         {object}  models.APIResponse
// @Failure      401         {object}  models.APIResponse
// @Failure      404         {object}  models.APIResponse
// @Failure      500         {object}  models.APIResponse
// @Router       /v1/api-keys/{api_key_id}/default [patch]
func (h *APIKeyHandler) SetDefault(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	keyID := strings.TrimSpace(chi.URLParam(r, "api_key_id"))
	if keyID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "api_key_id path parameter is required",
		})
		return
	}

	key, err := h.repo.SetDefault(r.Context(), user.ID, keyID)
	if err != nil {
		if errors.Is(err, apikeys.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "api key not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to set default api key",
		})
		return
	}

	writeJSON(w, http.StatusOK, defaultAPIKeyEnvelope{
		Success: true,
		Message: "Default API key updated successfully",
		Data:    key,
	})
}

func decodeCreateAPIKeyRequest(w http.ResponseWriter, r *http.Request) (createAPIKeyRequest, bool) {
	var req createAPIKeyRequest
	if r.Body == nil {
		return req, true
	}

	err := json.NewDecoder(r.Body).Decode(&req)
	if err == nil {
		req.Name = strings.TrimSpace(req.Name)
		if len(req.Name) > apikeys.MaxNameLength {
			writeJSON(w, http.StatusBadRequest, models.APIResponse{
				Success: false,
				Message: "name must be 80 characters or fewer",
			})
			return createAPIKeyRequest{}, false
		}
		return req, true
	}
	if errors.Is(err, io.EOF) {
		return req, true
	}

	writeJSON(w, http.StatusBadRequest, models.APIResponse{
		Success: false,
		Message: "Invalid request body",
	})
	return createAPIKeyRequest{}, false
}
