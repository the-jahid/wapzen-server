package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/calls"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/phonenumbers"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

// Default and bound values for the list query parameters, mirroring the
// documented schema for GET /v1/calls (page ≥ 1 default 1; limit 1..200 default
// 50). Paging matches the agents collection; only the page-size bounds differ,
// since a user accumulates far more calls than agents.
const (
	defaultCallsPage  = 1
	defaultCallsLimit = 50
	maxCallsLimit     = 200
)

// callsListPath is the collection URL rendered into the calls list links.
const callsListPath = "/v1/calls"

// CallHandler serves the Calls endpoints. Inbound calls are recorded by the
// voice-call runtime as they arrive; Create places an outbound one, which is
// why the handler also needs the phone numbers repository (to check the caller
// owns the number it would dial from) and the login manager (which owns the
// live WhatsApp session that actually places it).
type CallHandler struct {
	repo         *calls.Repository
	phoneNumbers *phonenumbers.Repository
	loginManager *whatsapplogin.Manager
}

// NewCallHandler creates a call handler with its dependencies.
func NewCallHandler(
	repo *calls.Repository,
	phoneNumbers *phonenumbers.Repository,
	loginManager *whatsapplogin.Manager,
) *CallHandler {
	return &CallHandler{repo: repo, phoneNumbers: phoneNumbers, loginManager: loginManager}
}

type callEnvelope struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    models.Call `json:"data"`
	Links   callLinks   `json:"links"`
}

type callListEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    []models.Call   `json:"data"`
	Meta    *types.Meta     `json:"meta,omitempty"`
	Links   types.ListLinks `json:"links"`
}

type callDeleteEnvelope struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Data    callDeleteData `json:"data"`
}

type callDeleteData struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

type callLinks struct {
	Self string `json:"self"`
}

// updateCallRequest is the partial-update body. Both fields are optional
// pointers so an absent field leaves the stored value untouched.
type updateCallRequest struct {
	Status    *string `json:"status"`
	EndReason *string `json:"end_reason"`
}

// createCallRequest is the Create Call body. AgentID is optional because a phone
// number carries the agent that speaks on its calls; supplying it asserts which
// agent that is, and a value naming a different one is rejected rather than
// quietly overridden.
type createCallRequest struct {
	PhoneNumberID string `json:"phone_number_id"`
	To            string `json:"to"`
	AgentID       string `json:"agent_id"`
}

// Create places an outbound call from one of the authenticated user's connected
// WhatsApp numbers and records it.
//
// Create godoc
// @Summary      Create Call
// @Description  Places an outbound WhatsApp call from a connected phone number owned by the authenticated API key owner. The agent that speaks is the one assigned to that phone number; agent_id is optional and only asserts which agent that is. Returns as soon as the callee's phone is ringing — the returned call starts in the "received" status and reaches "answered" when the callee picks up, or "declined" if they cut it while it rings.
// @Tags         Calls
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.createCallRequest  true  "The call to place"
// @Success      201   {object}  handlers.callEnvelope
// @Failure      400   {object}  types.ErrorEnvelope
// @Failure      401   {object}  models.APIResponse
// @Failure      404   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      502   {object}  models.APIResponse
// @Failure      503   {object}  models.APIResponse
// @Router       /v1/calls [post]
func (h *CallHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	req, ok := decodeCreateCallRequest(w, r)
	if !ok {
		return
	}

	// Confirm the caller owns the number before anything else, so an id that is
	// not theirs reads as "no such phone number" rather than leaking its state.
	number, err := h.phoneNumbers.GetByUser(r.Context(), user.ID, req.PhoneNumberID)
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
			Message: "failed to load phone number",
		})
		return
	}
	if number.Status != models.PhoneNumberStatusConnected {
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: "phone number is not connected (status: " + number.Status + ")",
		})
		return
	}

	callRowID, err := h.loginManager.PlaceOutboundCall(r.Context(), whatsapplogin.OutboundCall{
		UserID:        user.ID,
		PhoneNumberID: req.PhoneNumberID,
		To:            req.To,
		ExpectAgentID: req.AgentID,
	})
	if err != nil {
		status, message := placeCallErrorResponse(err)
		writeJSON(w, status, models.APIResponse{Success: false, Message: message})
		return
	}

	// The call is ringing and recorded; hand back the stored row so the response
	// is the same resource shape Get returns.
	call, err := h.repo.GetByUser(r.Context(), user.ID, callRowID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "call was placed but could not be read back",
		})
		return
	}

	writeJSON(w, http.StatusCreated, callEnvelope{
		Success: true,
		Message: "Call created successfully",
		Data:    call,
		Links:   callLinks{Self: "/v1/calls/" + call.ID},
	})
}

// decodeCreateCallRequest reads and validates the Create Call body, writing the
// 400 itself and returning false when it is unusable.
func decodeCreateCallRequest(w http.ResponseWriter, r *http.Request) (createCallRequest, bool) {
	var req createCallRequest

	body, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "request body is required",
		})
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return req, false
	}

	req.PhoneNumberID = strings.TrimSpace(req.PhoneNumberID)
	req.To = strings.TrimSpace(req.To)
	req.AgentID = strings.TrimSpace(req.AgentID)

	var fieldErrs []types.FieldError
	if req.PhoneNumberID == "" {
		fieldErrs = append(fieldErrs, types.FieldError{
			Field:   "phone_number_id",
			Message: "phone_number_id is required",
		})
	}
	switch {
	case req.To == "":
		fieldErrs = append(fieldErrs, types.FieldError{
			Field:   "to",
			Message: "to is required",
		})
	case !isDialableTarget(req.To):
		fieldErrs = append(fieldErrs, types.FieldError{
			Field:   "to",
			Message: "to must be a phone number in E.164 format, for example +15557654321",
		})
	}
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid request body",
			Errors:  fieldErrs,
		})
		return req, false
	}
	return req, true
}

// isDialableTarget reports whether target is something the call stack can dial.
// A WhatsApp JID ("…@s.whatsapp.net") is passed through untouched; anything else
// has to be a plain international number, since that is what gets turned into a
// JID. Digits only, with an optional leading "+", bounded by the E.164 length
// limit — enough to reject typos before they become a bogus JID on the wire.
func isDialableTarget(target string) bool {
	if strings.ContainsRune(target, '@') {
		return true
	}
	digits := strings.TrimPrefix(target, "+")
	if len(digits) < 5 || len(digits) > 15 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// placeCallErrorResponse maps a call-placement failure onto its HTTP status and
// message. The number's own state and its agent assignment are conflicts the
// caller can fix; missing server credentials are an unavailable capability; and
// anything else means WhatsApp itself refused the offer.
func placeCallErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, whatsapplogin.ErrAgentMismatch):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, whatsapplogin.ErrNumberNotConnected),
		errors.Is(err, whatsapplogin.ErrCallHandlerUnavailable),
		errors.Is(err, whatsapplogin.ErrNoLiveAgent):
		return http.StatusConflict, err.Error()
	case errors.Is(err, whatsapplogin.ErrCallingUnavailable),
		errors.Is(err, whatsapplogin.ErrAgentUnavailable):
		return http.StatusServiceUnavailable, err.Error()
	default:
		return http.StatusBadGateway, "could not place the call: " + err.Error()
	}
}

// List returns a page of the authenticated user's calls, newest first.
//
// List godoc
// @Summary      List Calls
// @Description  Returns a paginated list of the calls handled for the authenticated API key owner, newest first.
// @Tags         Calls
// @Produce      json
// @Security     BearerAuth
// @Param        page   query     int  false  "Page number (1-based)"
// @Param        limit  query     int  false  "Items per page (1-200)"
// @Success      200    {object}  handlers.callListEnvelope
// @Failure      400    {object}  types.ErrorEnvelope
// @Failure      401    {object}  models.APIResponse
// @Failure      500    {object}  models.APIResponse
// @Router       /v1/calls [get]
func (h *CallHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	page, limit, fieldErrs := parseCallListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	list, total, err := h.repo.ListByUser(r.Context(), user.ID, limit, (page-1)*limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list calls",
		})
		return
	}

	meta := buildPaginationMeta(page, limit, total)

	writeJSON(w, http.StatusOK, callListEnvelope{
		Success: true,
		Message: "Calls retrieved successfully",
		Data:    list,
		Meta:    &meta,
		Links:   buildListLinks(callsListPath, page, limit, total, nil),
	})
}

// ListByCampaign returns a page of the calls one outbound campaign placed,
// newest first. It is the campaign's own call history: the same call resources
// List serves, narrowed to the calls that campaign caused.
//
// ListByCampaign godoc
// @Summary      List Campaign Calls
// @Description  Returns a paginated list of the calls placed by one of the authenticated user's outbound campaigns, newest first. These are the same call resources GET /v1/calls serves, narrowed to the campaign that placed them.
// @Tags         Outbound Campaign
// @Produce      json
// @Security     BearerAuth
// @Param        campaign_id  path      string  true   "Campaign id"
// @Param        page         query     int     false  "Page number (1-based)"
// @Param        limit        query     int     false  "Items per page (1-200)"
// @Success      200          {object}  handlers.callListEnvelope
// @Failure      400          {object}  types.ErrorEnvelope
// @Failure      401          {object}  models.APIResponse
// @Failure      500          {object}  models.APIResponse
// @Router       /v1/outbound-campaigns/{campaign_id}/calls [get]
func (h *CallHandler) ListByCampaign(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}

	page, limit, fieldErrs := parseCallListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	list, total, err := h.repo.ListByCampaign(r.Context(), user.ID, campaignID, limit, (page-1)*limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list campaign calls",
		})
		return
	}

	meta := buildPaginationMeta(page, limit, total)

	writeJSON(w, http.StatusOK, callListEnvelope{
		Success: true,
		Message: "Campaign calls retrieved successfully",
		Data:    list,
		Meta:    &meta,
		Links:   buildListLinks(outboundCampaignsListPath+"/"+campaignID+"/calls", page, limit, total, nil),
	})
}

// parseCallListQuery reads and validates the page/limit query parameters,
// returning the effective values plus any per-field validation errors. Absent
// parameters fall back to their documented defaults.
func parseCallListQuery(q url.Values) (page, limit int, errs []types.FieldError) {
	page, limit = defaultCallsPage, defaultCallsLimit

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err != nil || p < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			page = p
		}
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if l, err := strconv.Atoi(raw); err != nil || l < 1 || l > maxCallsLimit {
			errs = append(errs, types.FieldError{Field: "limit", Message: fmt.Sprintf("limit must be between 1 and %d", maxCallsLimit)})
		} else {
			limit = l
		}
	}

	return page, limit, errs
}

// Get returns a single call owned by the authenticated user, including its saved
// conversation transcript.
//
// Get godoc
// @Summary      Get Call
// @Description  Returns a single call owned by the authenticated user, including the saved AI/user conversation transcript.
// @Tags         Calls
// @Produce      json
// @Security     BearerAuth
// @Param        call_id  path      string  true  "Call id"
// @Success      200      {object}  handlers.callEnvelope
// @Failure      400      {object}  models.APIResponse
// @Failure      401      {object}  models.APIResponse
// @Failure      404      {object}  models.APIResponse
// @Failure      500      {object}  models.APIResponse
// @Router       /v1/calls/{call_id} [get]
func (h *CallHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	callID, ok := callIDParam(w, r)
	if !ok {
		return
	}

	call, err := h.repo.GetByUser(r.Context(), user.ID, callID)
	if err != nil {
		if errors.Is(err, calls.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "call not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to get call",
		})
		return
	}

	messages, err := h.repo.ListMessages(r.Context(), call.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to load call transcript",
		})
		return
	}
	call.Messages = messages

	writeJSON(w, http.StatusOK, callEnvelope{
		Success: true,
		Message: "Call retrieved successfully",
		Data:    call,
		Links:   callLinks{Self: "/v1/calls/" + call.ID},
	})
}

// Update applies a partial update to a call owned by the authenticated user.
// Only status and end_reason may be changed; absent fields keep their value.
//
// Update godoc
// @Summary      Update Call
// @Description  Applies a partial update to a call owned by the authenticated user. Only status and end_reason may be changed; at least one must be supplied. status must be one of received, answered, ended, declined, failed.
// @Tags         Calls
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        call_id  path      string                    true  "Call id"
// @Param        body     body      handlers.updateCallRequest  true  "Fields to change"
// @Success      200      {object}  handlers.callEnvelope
// @Failure      400      {object}  models.APIResponse
// @Failure      401      {object}  models.APIResponse
// @Failure      404      {object}  models.APIResponse
// @Failure      500      {object}  models.APIResponse
// @Router       /v1/calls/{call_id} [patch]
func (h *CallHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	callID, ok := callIDParam(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "request body is required",
		})
		return
	}

	var req updateCallRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	if req.Status == nil && req.EndReason == nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "at least one of status or end_reason is required",
		})
		return
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if !models.IsValidCallStatus(status) {
			writeJSON(w, http.StatusBadRequest, models.APIResponse{
				Success: false,
				Message: "status must be one of " + strings.Join(models.CallStatuses(), ", "),
			})
			return
		}
		req.Status = &status
	}

	call, err := h.repo.UpdateByUser(r.Context(), user.ID, callID, req.Status, req.EndReason)
	if err != nil {
		if errors.Is(err, calls.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "call not found",
			})
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23514", "23502", "22P02": // check / not-null / invalid input
				writeJSON(w, http.StatusBadRequest, models.APIResponse{
					Success: false,
					Message: pgErr.Message,
				})
				return
			}
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to update call",
		})
		return
	}

	writeJSON(w, http.StatusOK, callEnvelope{
		Success: true,
		Message: "Call updated successfully",
		Data:    call,
		Links:   callLinks{Self: "/v1/calls/" + call.ID},
	})
}

// Delete permanently removes a call owned by the authenticated user, together
// with its saved transcript (cascaded by the schema).
//
// Delete godoc
// @Summary      Delete Call
// @Description  Deletes a call owned by the authenticated user. The saved conversation transcript is removed with it.
// @Tags         Calls
// @Produce      json
// @Security     BearerAuth
// @Param        call_id  path      string  true  "Call id"
// @Success      200      {object}  handlers.callDeleteEnvelope
// @Failure      400      {object}  models.APIResponse
// @Failure      401      {object}  models.APIResponse
// @Failure      404      {object}  models.APIResponse
// @Failure      500      {object}  models.APIResponse
// @Router       /v1/calls/{call_id} [delete]
func (h *CallHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	callID, ok := callIDParam(w, r)
	if !ok {
		return
	}

	if err := h.repo.DeleteByUser(r.Context(), user.ID, callID); err != nil {
		if errors.Is(err, calls.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "call not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to delete call",
		})
		return
	}

	writeJSON(w, http.StatusOK, callDeleteEnvelope{
		Success: true,
		Message: "Call deleted successfully",
		Data:    callDeleteData{ID: callID, Deleted: true},
	})
}

// callIDParam reads and validates the {call_id} path parameter, writing a 400
// and returning false when it is missing.
func callIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	callID := strings.TrimSpace(chi.URLParam(r, "call_id"))
	if callID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "call_id path parameter is required",
		})
		return "", false
	}
	return callID, true
}
