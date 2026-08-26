package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	svix "github.com/svix/svix-webhooks/go"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/users"
)

// WebhookHandler handles public webhook callbacks.
type WebhookHandler struct {
	repo          *users.Repository
	signingSecret string
}

// NewWebhookHandler creates a webhook handler with its dependencies.
func NewWebhookHandler(repo *users.Repository, signingSecret string) *WebhookHandler {
	return &WebhookHandler{
		repo:          repo,
		signingSecret: signingSecret,
	}
}

// Clerk receives and verifies Clerk user webhooks.
//
// Clerk godoc
// @Summary      Clerk webhook
// @Description  Verifies Svix headers and syncs Clerk user events into Postgres.
// @Tags         webhooks
// @Accept       json
// @Produce      json
// @Success      200  {object}  models.APIResponse
// @Failure      400  {object}  models.APIResponse
// @Failure      500  {object}  models.APIResponse
// @Router       /api/webhooks/clerk [post]
func (h *WebhookHandler) Clerk(w http.ResponseWriter, r *http.Request) {
	if h.signingSecret == "" {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "Clerk webhook signing secret is not configured",
		})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid webhook body",
		})
		return
	}

	webhook, err := svix.NewWebhook(h.signingSecret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "Invalid Clerk webhook signing secret",
		})
		return
	}
	if err := webhook.Verify(body, r.Header); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid webhook signature",
		})
		return
	}

	var event clerkWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid webhook payload",
		})
		return
	}

	if err := h.handleClerkEvent(r, event); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "Webhook processed",
	})
}

func (h *WebhookHandler) handleClerkEvent(r *http.Request, event clerkWebhookEvent) error {
	switch event.Type {
	case "user.created", "user.updated":
		email := event.Data.PrimaryEmail()
		if event.Data.ID == "" {
			return fmt.Errorf("webhook user id is required")
		}
		if email == "" {
			return fmt.Errorf("webhook user email is required")
		}

		_, err := h.repo.Upsert(r.Context(), users.UpsertParams{
			ClerkID:  event.Data.ID,
			Email:    email,
			Username: event.Data.Username,
		})
		if err != nil {
			return fmt.Errorf("sync webhook user: %w", err)
		}
	case "user.deleted":
		if event.Data.ID == "" {
			return fmt.Errorf("webhook user id is required")
		}
		if err := h.repo.DeleteByClerkID(r.Context(), event.Data.ID); err != nil {
			return fmt.Errorf("delete webhook user: %w", err)
		}
	default:
		return nil
	}

	return nil
}

type clerkWebhookEvent struct {
	Type string           `json:"type"`
	Data clerkWebhookUser `json:"data"`
}

type clerkWebhookUser struct {
	ID                    string              `json:"id"`
	EmailAddresses        []clerkWebhookEmail `json:"email_addresses"`
	PrimaryEmailAddressID string              `json:"primary_email_address_id"`
	Username              *string             `json:"username"`
}

type clerkWebhookEmail struct {
	ID           string `json:"id"`
	EmailAddress string `json:"email_address"`
}

func (u clerkWebhookUser) PrimaryEmail() string {
	if u.PrimaryEmailAddressID != "" {
		for _, email := range u.EmailAddresses {
			if email.ID == u.PrimaryEmailAddressID {
				return email.EmailAddress
			}
		}
	}

	if len(u.EmailAddresses) > 0 {
		return u.EmailAddresses[0].EmailAddress
	}

	return ""
}
