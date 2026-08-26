package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/phonenumbers"
	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

// PhoneNumberHandler handles authenticated WhatsApp phone number login routes.
type PhoneNumberHandler struct {
	repo         *phonenumbers.Repository
	loginManager *whatsapplogin.Manager
}

// NewPhoneNumberHandler creates a phone number handler with its dependencies.
func NewPhoneNumberHandler(
	repo *phonenumbers.Repository,
	loginManager *whatsapplogin.Manager,
) *PhoneNumberHandler {
	return &PhoneNumberHandler{repo: repo, loginManager: loginManager}
}

type loginPhoneNumberRequest struct {
	PhoneNumberID *string `json:"phone_number_id" example:"phone_number_12345"`
	PhoneNumber   *string `json:"phone_number" example:"+15551234567"`
	Label         *string `json:"label" example:"Main support line"`
	ForceRePair   bool    `json:"force_repair,omitempty" example:"false"`
	// AgentID is the agent that gets this number as soon as the scan connects
	// it, which is how the agent editor links a number in one step. Omit it to
	// link a number that no agent uses yet.
	AgentID *string `json:"agent_id" example:"agent_12345"`
}

type logoutPhoneNumberRequest struct {
	PhoneNumberID string `json:"phone_number_id" example:"phone_number_12345"`
}

type phoneNumberEnvelope struct {
	Success bool               `json:"success"`
	Message string             `json:"message"`
	Data    models.PhoneNumber `json:"data"`
	Links   phoneNumberLinks   `json:"links"`
}

type phoneNumberListEnvelope struct {
	Success bool                 `json:"success"`
	Message string               `json:"message"`
	Data    []models.PhoneNumber `json:"data"`
}

type phoneNumberLogoutEnvelope struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    phoneNumberLogoutData `json:"data"`
}

type phoneNumberLogoutData struct {
	ID           string `json:"id"`
	Disconnected bool   `json:"disconnected"`
	Deleted      bool   `json:"deleted"`
}

type phoneNumberLinks struct {
	Self string `json:"self"`
}

// List returns the authenticated user's WhatsApp phone numbers.
//
// List godoc
// @Summary      Get phone numbers list
// @Description  Returns the WhatsApp phone numbers owned by the authenticated user.
// @Tags         phone-number
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  handlers.phoneNumberListEnvelope
// @Failure      401  {object}  models.APIResponse
// @Failure      500  {object}  models.APIResponse
// @Router       /v1/phone-number [get]
func (h *PhoneNumberHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	phoneNumbers, err := h.repo.ListByUser(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list phone numbers",
		})
		return
	}

	writeJSON(w, http.StatusOK, phoneNumberListEnvelope{
		Success: true,
		Message: "Phone numbers retrieved successfully",
		Data:    phoneNumbers,
	})
}

// Login starts a WhatsApp QR login session for the authenticated user. When
// phone_number_id is provided, the existing row is reused. force_repair also
// unlinks a connected companion while preserving that row and its assignments.
// agent_id names the agent the linked number is assigned to the moment the scan
// connects it, which is how the agent editor sets up a number in one step.
//
// Login godoc
// @Summary      Start WhatsApp QR login
// @Description  Starts a WhatsApp Linked Devices QR login session. Pass phone_number_id to restart a non-connected login; add force_repair to replace a connected companion without deleting its row. Pass agent_id to assign the linked number to that agent as soon as the scan connects it.
// @Tags         phone-number
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.loginPhoneNumberRequest  false  "Optional phone number metadata"
// @Success      200   {object}  handlers.phoneNumberEnvelope
// @Failure      400   {object}  models.APIResponse
// @Failure      401   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/phone-number/login [post]
func (h *PhoneNumberHandler) Login(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	req, ok := decodeLoginPhoneNumberRequest(w, r)
	if !ok {
		return
	}

	if req.ForceRePair && req.PhoneNumberID == nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "phone_number_id is required when force_repair is true",
		})
		return
	}

	if req.PhoneNumberID != nil {
		h.restartLogin(w, r, user.ID, *req.PhoneNumberID, req.ForceRePair, derefString(req.AgentID))
		return
	}

	phoneNumber, err := h.loginManager.StartLogin(r.Context(), user.ID, req.PhoneNumber, req.Label, derefString(req.AgentID))
	if err != nil {
		if errors.Is(err, whatsapplogin.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "agent not found",
			})
			return
		}
		if writePhoneNumberPostgresError(w, err) {
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to start phone number login",
		})
		return
	}

	writeJSON(w, http.StatusOK, phoneNumberEnvelope{
		Success: true,
		Message: "Phone number login started successfully",
		Data:    phoneNumber,
		Links:   phoneNumberLinks{Self: "/v1/phone-number/" + phoneNumber.ID},
	})
}

// restartLogin reissues a QR code for an existing row. assignAgentID names the
// agent the number goes to once the scan lands (empty for none); re-pairing
// ignores it, because it keeps the assignments the row already has.
func (h *PhoneNumberHandler) restartLogin(w http.ResponseWriter, r *http.Request, userID, phoneNumberID string, forceRePair bool, assignAgentID string) {
	var phoneNumber models.PhoneNumber
	var err error
	if forceRePair {
		phoneNumber, err = h.loginManager.RePair(r.Context(), userID, phoneNumberID)
	} else {
		phoneNumber, err = h.loginManager.RestartLogin(r.Context(), userID, phoneNumberID, assignAgentID)
	}
	if err != nil {
		switch {
		case errors.Is(err, whatsapplogin.ErrAgentNotFound):
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "agent not found",
			})
		case errors.Is(err, phonenumbers.ErrNotFound):
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "phone number not found",
			})
		case errors.Is(err, whatsapplogin.ErrAlreadyConnected):
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "phone number is already connected",
			})
		case errors.Is(err, whatsapplogin.ErrLoginInProgress):
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "phone number login is already in progress",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Message: "failed to restart phone number login",
			})
		}
		return
	}

	message := "Phone number login restarted successfully"
	if forceRePair {
		message = "Phone number re-pairing started successfully"
	}
	writeJSON(w, http.StatusOK, phoneNumberEnvelope{
		Success: true,
		Message: message,
		Data:    phoneNumber,
		Links:   phoneNumberLinks{Self: "/v1/phone-number/" + phoneNumber.ID},
	})
}

// Get returns one authenticated-user-owned WhatsApp phone number.
//
// Get godoc
// @Summary      Get one phone number
// @Description  Returns a single WhatsApp phone number by id, scoped to the authenticated user.
// @Tags         phone-number
// @Produce      json
// @Security     BearerAuth
// @Param        phone_number_id  path      string  true  "Phone number id"
// @Success      200              {object}  handlers.phoneNumberEnvelope
// @Failure      400              {object}  models.APIResponse
// @Failure      401              {object}  models.APIResponse
// @Failure      404              {object}  models.APIResponse
// @Failure      500              {object}  models.APIResponse
// @Router       /v1/phone-number/{phone_number_id} [get]
func (h *PhoneNumberHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	phoneNumberID := strings.TrimSpace(chi.URLParam(r, "phone_number_id"))
	if phoneNumberID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "phone_number_id path parameter is required",
		})
		return
	}

	phoneNumber, err := h.repo.GetByUser(r.Context(), user.ID, phoneNumberID)
	if err != nil {
		if errors.Is(err, phonenumbers.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "phone number not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to get phone number",
		})
		return
	}

	writeJSON(w, http.StatusOK, phoneNumberEnvelope{
		Success: true,
		Message: "Phone number retrieved successfully",
		Data:    phoneNumber,
		Links:   phoneNumberLinks{Self: "/v1/phone-number/" + phoneNumber.ID},
	})
}

// Logout disconnects a WhatsApp phone number owned by the authenticated user
// and deletes its local database record.
//
// Logout godoc
// @Summary      Logout
// @Description  Disconnects a WhatsApp phone number owned by the authenticated user and deletes its local database record.
// @Tags         phone-number
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.logoutPhoneNumberRequest  true  "Phone number logout request"
// @Success      200   {object}  handlers.phoneNumberLogoutEnvelope
// @Failure      400   {object}  models.APIResponse
// @Failure      401   {object}  models.APIResponse
// @Failure      404   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/phone-number/logout [post]
func (h *PhoneNumberHandler) Logout(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	var req logoutPhoneNumberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}
	req.PhoneNumberID = strings.TrimSpace(req.PhoneNumberID)
	if req.PhoneNumberID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "phone_number_id is required",
		})
		return
	}

	if _, err := h.loginManager.Logout(r.Context(), user.ID, req.PhoneNumberID); err != nil {
		if errors.Is(err, phonenumbers.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "phone number not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to logout phone number",
		})
		return
	}

	writeJSON(w, http.StatusOK, phoneNumberLogoutEnvelope{
		Success: true,
		Message: "Phone number logged out and deleted successfully",
		Data: phoneNumberLogoutData{
			ID:           req.PhoneNumberID,
			Disconnected: true,
			Deleted:      true,
		},
	})
}

func decodeLoginPhoneNumberRequest(w http.ResponseWriter, r *http.Request) (loginPhoneNumberRequest, bool) {
	var req loginPhoneNumberRequest
	if r.Body == nil {
		return req, true
	}

	err := json.NewDecoder(r.Body).Decode(&req)
	if err == nil {
		if req.PhoneNumberID != nil {
			req.PhoneNumberID = optionalString(*req.PhoneNumberID)
		}
		if req.AgentID != nil {
			req.AgentID = optionalString(*req.AgentID)
		}
		if req.PhoneNumber != nil {
			phoneNumber := strings.TrimSpace(*req.PhoneNumber)
			if phoneNumber != "" && !isE164(phoneNumber) {
				writeJSON(w, http.StatusBadRequest, models.APIResponse{
					Success: false,
					Message: "phone_number must be in E.164 format",
				})
				return loginPhoneNumberRequest{}, false
			}
			req.PhoneNumber = optionalString(phoneNumber)
		}
		if req.Label != nil {
			label := strings.TrimSpace(*req.Label)
			if len(label) > 80 {
				writeJSON(w, http.StatusBadRequest, models.APIResponse{
					Success: false,
					Message: "label must be 80 characters or fewer",
				})
				return loginPhoneNumberRequest{}, false
			}
			req.Label = optionalString(label)
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
	return loginPhoneNumberRequest{}, false
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func isE164(value string) bool {
	if len(value) < 3 || len(value) > 16 || value[0] != '+' {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	if value[1] == '0' {
		return false
	}
	return true
}

func writePhoneNumberPostgresError(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	switch pgErr.Code {
	case "23503":
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: "user not found",
		})
		return true
	case "23505":
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: "phone number already exists",
		})
		return true
	case "23514", "23502", "22P02":
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: pgErr.Message,
		})
		return true
	default:
		return false
	}
}
