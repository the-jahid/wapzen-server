package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/calls"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/outboundcampaigns"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

const campaignLeadsPathSuffix = "/leads"

// CampaignLeadHandler serves the campaign lead endpoints. Adding a lead also
// dials it, which is why the handler needs more than the campaign repository:
// loginManager places the call from the campaign agent's number, and calls reads
// the resulting row back so the response carries the call it started.
type CampaignLeadHandler struct {
	repo         *outboundcampaigns.Repository
	calls        *calls.Repository
	loginManager *whatsapplogin.Manager
}

func NewCampaignLeadHandler(repo *outboundcampaigns.Repository, callsRepo *calls.Repository, loginManager *whatsapplogin.Manager) *CampaignLeadHandler {
	return &CampaignLeadHandler{repo: repo, calls: callsRepo, loginManager: loginManager}
}

type campaignLeadEnvelope struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    models.CampaignLead   `json:"data"`
	Links   outboundCampaignLinks `json:"links"`
}

// campaignLeadCreateEnvelope is the Create response. It is the lead envelope
// plus the call the lead was dialled with: Call is the ringing call when one was
// placed, and CallError says why not when it was not. The lead is created either
// way — a number that cannot be dialled right now is still a lead — so the two
// fields report the dialling outcome instead of the request failing.
type campaignLeadCreateEnvelope struct {
	Success   bool                  `json:"success"`
	Message   string                `json:"message"`
	Data      models.CampaignLead   `json:"data"`
	Call      *models.Call          `json:"call"`
	CallError string                `json:"call_error,omitempty"`
	Links     outboundCampaignLinks `json:"links"`
}

type campaignLeadListEnvelope struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    []models.CampaignLead `json:"data"`
	Meta    *types.Meta           `json:"meta,omitempty"`
	Links   types.ListLinks       `json:"links"`
}

type campaignLeadDeleteEnvelope struct {
	Success bool                       `json:"success"`
	Message string                     `json:"message"`
	Data    outboundCampaignDeleteData `json:"data"`
}

type createCampaignLeadRequest struct {
	PhoneNumber string  `json:"phone_number"`
	Email       *string `json:"email"`
	FirstName   *string `json:"first_name"`
	LastName    *string `json:"last_name"`
}

type nullableLeadString struct {
	Present bool
	Value   *string
}

func (v *nullableLeadString) UnmarshalJSON(data []byte) error {
	v.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		v.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

type updateCampaignLeadRequest struct {
	PhoneNumber *string            `json:"phone_number"`
	Email       nullableLeadString `json:"email"`
	FirstName   nullableLeadString `json:"first_name"`
	LastName    nullableLeadString `json:"last_name"`
	Status      *string            `json:"status"`
}

func (h *CampaignLeadHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}
	req, ok := decodeCreateCampaignLeadRequest(w, r)
	if !ok {
		return
	}
	lead, err := h.repo.CreateLead(r.Context(), user.ID, models.NewCampaignLead{
		CampaignID: campaignID, PhoneNumber: req.PhoneNumber, Email: req.Email,
		FirstName: req.FirstName, LastName: req.LastName,
	})
	if err != nil {
		switch {
		case errors.Is(err, outboundcampaigns.ErrNotFound):
			writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "outbound campaign not found"})
		case errors.Is(err, outboundcampaigns.ErrLeadPhoneNumberTaken):
			writeJSON(w, http.StatusConflict, models.APIResponse{Success: false, Message: "a lead with this phone number already exists in the campaign"})
		default:
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "failed to create campaign lead"})
		}
		return
	}

	// Adding a lead is what starts calling it. The call is placed after the lead
	// exists so the call can be recorded against it, and its failure never undoes
	// the lead: the contact is saved either way and the response says what
	// happened to the call.
	call, callErr := h.dialNewLead(r.Context(), user.ID, campaignID, lead)
	message := "Campaign lead created successfully"
	if call != nil {
		message = "Campaign lead created and the call is ringing"
		// The insert stamped the lead's first attempt and moved it to "calling",
		// so the stored row is now ahead of the one CreateLead returned.
		if refreshed, err := h.repo.GetLeadByUser(r.Context(), user.ID, campaignID, lead.ID); err == nil {
			lead = refreshed
		}
	}
	writeJSON(w, http.StatusCreated, campaignLeadCreateEnvelope{Success: true, Message: message, Data: lead, Call: call, CallError: callErr, Links: outboundCampaignLinks{Self: campaignLeadPath(campaignID, lead.ID)}})
}

// dialNewLead places the campaign's call to a lead that was just added, and
// returns the ringing call. A second return value that is non-empty means no
// call was placed and says why, in words meant for whoever added the lead.
//
// The campaign is dialled with its own agent, from the number that agent speaks
// on — a campaign never names a number of its own — so a campaign without an
// agent, or whose agent has no number, has nothing to dial from. A campaign that
// is paused or finished is not dialling at all, and adding a lead to it does not
// restart it.
func (h *CampaignLeadHandler) dialNewLead(ctx context.Context, userID, campaignID string, lead models.CampaignLead) (*models.Call, string) {
	if h.loginManager == nil || h.calls == nil {
		return nil, "outbound calling is not configured on this server, so the lead was added without calling it"
	}

	campaign, err := h.repo.GetByUser(ctx, userID, campaignID)
	if err != nil {
		return nil, "the campaign could not be read back, so the lead was added without calling it"
	}
	if reason := campaignDialBlockReason(campaign); reason != "" {
		return nil, reason
	}

	callRowID, err := h.loginManager.PlaceOutboundCall(ctx, whatsapplogin.OutboundCall{
		UserID:        userID,
		PhoneNumberID: *campaign.PhoneNumberID,
		To:            lead.PhoneNumber,
		ExpectAgentID: *campaign.AgentID,
		CampaignID:    campaignID,
		LeadID:        lead.ID,
	})
	if err != nil {
		_, message := placeCallErrorResponse(err)
		return nil, "the lead was added but the call could not be placed: " + message
	}

	call, err := h.calls.GetByUser(ctx, userID, callRowID)
	if err != nil {
		return nil, "the call was placed but could not be read back"
	}
	return &call, ""
}

// campaignDialBlockReason reports why a campaign cannot dial a lead right now,
// or "" when it can. A campaign dials with its own agent, from the number that
// agent speaks on, so one without either has nothing to dial from; a paused or
// finished campaign is not dialling at all, and adding a lead to it is not a way
// to restart it.
func campaignDialBlockReason(campaign models.OutboundCampaign) string {
	switch {
	case campaign.AgentID == nil || strings.TrimSpace(*campaign.AgentID) == "":
		return "this campaign has no agent to dial with, so the lead was added without calling it"
	case campaign.PhoneNumberID == nil || strings.TrimSpace(*campaign.PhoneNumberID) == "":
		return "this campaign's agent has no phone number assigned, so the lead was added without calling it"
	case campaign.Status == models.CampaignStatusPaused || models.IsTerminalCampaignStatus(campaign.Status):
		return "the campaign is " + campaign.Status + ", so the lead was added without calling it"
	default:
		return ""
	}
}

func (h *CampaignLeadHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}
	page, limit, status, fieldErrs := parseCampaignLeadListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{Success: false, Message: "Invalid query parameters", Errors: fieldErrs})
		return
	}
	leads, total, err := h.repo.ListLeadsByCampaign(r.Context(), user.ID, campaignID, status, limit, (page-1)*limit)
	if err != nil {
		if errors.Is(err, outboundcampaigns.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "outbound campaign not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "failed to list campaign leads"})
		}
		return
	}
	meta := buildPaginationMeta(page, limit, total)
	writeJSON(w, http.StatusOK, campaignLeadListEnvelope{Success: true, Message: "Campaign leads retrieved successfully", Data: leads, Meta: &meta, Links: buildCampaignLeadListLinks(campaignID, page, limit, total, status)})
}

func (h *CampaignLeadHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}
	leadID, ok := campaignLeadIDParam(w, r)
	if !ok {
		return
	}
	lead, err := h.repo.GetLeadByUser(r.Context(), user.ID, campaignID, leadID)
	if err != nil {
		h.writeLeadError(w, err, "failed to get campaign lead")
		return
	}
	writeJSON(w, http.StatusOK, campaignLeadEnvelope{Success: true, Message: "Campaign lead retrieved successfully", Data: lead, Links: outboundCampaignLinks{Self: campaignLeadPath(campaignID, lead.ID)}})
}

func (h *CampaignLeadHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}
	leadID, ok := campaignLeadIDParam(w, r)
	if !ok {
		return
	}
	req, ok := decodeUpdateCampaignLeadRequest(w, r)
	if !ok {
		return
	}
	params := models.CampaignLeadUpdate{}
	if req.PhoneNumber != nil {
		params.SetPhone = true
		params.PhoneNumber = *req.PhoneNumber
	}
	if req.Email.Present {
		params.SetEmail = true
		params.Email = req.Email.Value
	}
	if req.FirstName.Present {
		params.SetFirst = true
		params.FirstName = req.FirstName.Value
	}
	if req.LastName.Present {
		params.SetLast = true
		params.LastName = req.LastName.Value
	}
	if req.Status != nil {
		params.SetStatus = true
		params.Status = *req.Status
	}
	lead, err := h.repo.UpdateLeadByUser(r.Context(), user.ID, campaignID, leadID, params)
	if err != nil {
		if errors.Is(err, outboundcampaigns.ErrLeadPhoneNumberTaken) {
			writeJSON(w, http.StatusConflict, models.APIResponse{Success: false, Message: "a lead with this phone number already exists in the campaign"})
		} else {
			h.writeLeadError(w, err, "failed to update campaign lead")
		}
		return
	}
	writeJSON(w, http.StatusOK, campaignLeadEnvelope{Success: true, Message: "Campaign lead updated successfully", Data: lead, Links: outboundCampaignLinks{Self: campaignLeadPath(campaignID, lead.ID)}})
}

func (h *CampaignLeadHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}
	leadID, ok := campaignLeadIDParam(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteLeadByUser(r.Context(), user.ID, campaignID, leadID); err != nil {
		h.writeLeadError(w, err, "failed to delete campaign lead")
		return
	}
	writeJSON(w, http.StatusOK, campaignLeadDeleteEnvelope{Success: true, Message: "Campaign lead deleted successfully", Data: outboundCampaignDeleteData{ID: leadID, Deleted: true}})
}

func (h *CampaignLeadHandler) writeLeadError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, outboundcampaigns.ErrLeadNotFound) || errors.Is(err, outboundcampaigns.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "campaign lead not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: message})
}

func campaignLeadIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "lead_id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "lead_id path parameter is required"})
		return "", false
	}
	return id, true
}

func campaignLeadPath(campaignID, leadID string) string {
	return outboundCampaignsListPath + "/" + campaignID + campaignLeadsPathSuffix + "/" + leadID
}

func parseCampaignLeadListQuery(q url.Values) (page, limit int, status string, errs []types.FieldError) {
	page, limit = defaultOutboundCampaignPage, defaultOutboundCampaignLimit
	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if value, err := strconv.Atoi(raw); err != nil || value < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			page = value
		}
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if value, err := strconv.Atoi(raw); err != nil || value < 1 || value > maxOutboundCampaignLimit {
			errs = append(errs, types.FieldError{Field: "limit", Message: fmt.Sprintf("limit must be between 1 and %d", maxOutboundCampaignLimit)})
		} else {
			limit = value
		}
	}
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		if !models.IsValidCampaignLeadStatus(raw) {
			errs = append(errs, types.FieldError{Field: "status", Message: fmt.Sprintf("status must be one of %s", strings.Join(models.CampaignLeadStatuses(), ", "))})
		} else {
			status = raw
		}
	}
	return
}

func buildCampaignLeadListLinks(campaignID string, page, limit, total int, status string) types.ListLinks {
	base := outboundCampaignsListPath + "/" + campaignID + campaignLeadsPathSuffix
	links := buildListLinks(base, page, limit, total, nil)
	if status == "" {
		return links
	}
	suffix := "&status=" + url.QueryEscape(status)
	links.Self += suffix
	links.First += suffix
	links.Last += suffix
	if links.Previous != nil {
		value := *links.Previous + suffix
		links.Previous = &value
	}
	if links.Next != nil {
		value := *links.Next + suffix
		links.Next = &value
	}
	return links
}

func decodeCreateCampaignLeadRequest(w http.ResponseWriter, r *http.Request) (createCampaignLeadRequest, bool) {
	var req createCampaignLeadRequest
	body, ok := readLeadBody(w, r)
	if !ok {
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return req, false
	}
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	req.Email = normalizedOptionalString(req.Email)
	req.FirstName = normalizedOptionalString(req.FirstName)
	req.LastName = normalizedOptionalString(req.LastName)
	errs := validateCampaignLeadFields(req.PhoneNumber, req.Email, req.FirstName, req.LastName, nil, true)
	errs = append(errs, rejectCampaignLeadReadOnlyFields(body)...)
	errs = append(errs, rejectCreateCampaignLeadStatus(body)...)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{Success: false, Message: "Invalid request body", Errors: errs})
		return req, false
	}
	return req, true
}

func decodeUpdateCampaignLeadRequest(w http.ResponseWriter, r *http.Request) (updateCampaignLeadRequest, bool) {
	var req updateCampaignLeadRequest
	body, ok := readLeadBody(w, r)
	if !ok {
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return req, false
	}
	if req.PhoneNumber != nil {
		value := strings.TrimSpace(*req.PhoneNumber)
		req.PhoneNumber = &value
	}
	if req.Email.Present {
		req.Email.Value = normalizedOptionalString(req.Email.Value)
	}
	if req.FirstName.Present {
		req.FirstName.Value = normalizedOptionalString(req.FirstName.Value)
	}
	if req.LastName.Present {
		req.LastName.Value = normalizedOptionalString(req.LastName.Value)
	}
	if req.Status != nil {
		value := strings.TrimSpace(*req.Status)
		req.Status = &value
	}
	if req.PhoneNumber == nil && !req.Email.Present && !req.FirstName.Present && !req.LastName.Present && req.Status == nil {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{Success: false, Message: "Invalid request body", Errors: []types.FieldError{{Field: "body", Message: "supply at least one lead field to update"}}})
		return req, false
	}
	phone := ""
	requiredPhone := false
	if req.PhoneNumber != nil {
		phone = *req.PhoneNumber
		requiredPhone = true
	}
	errs := validateCampaignLeadFields(phone, req.Email.Value, req.FirstName.Value, req.LastName.Value, req.Status, requiredPhone)
	errs = append(errs, rejectCampaignLeadReadOnlyFields(body)...)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{Success: false, Message: "Invalid request body", Errors: errs})
		return req, false
	}
	return req, true
}

func readLeadBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "request body is required"})
		return nil, false
	}
	return body, true
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func validateCampaignLeadFields(phone string, email, first, last, status *string, validatePhone bool) []types.FieldError {
	var errs []types.FieldError
	if validatePhone && !isCampaignLeadE164(phone) {
		errs = append(errs, types.FieldError{Field: "phone_number", Message: "phone_number must be a valid E.164 number (for example +14155550123)"})
	}
	if email != nil {
		address, err := mail.ParseAddress(*email)
		if err != nil || address.Address != *email {
			errs = append(errs, types.FieldError{Field: "email", Message: "email must be a valid email address"})
		}
	}
	for _, field := range []struct {
		name  string
		value *string
	}{{"first_name", first}, {"last_name", last}} {
		if field.value != nil && utf8.RuneCountInString(*field.value) > models.CampaignLeadMaxNameLength {
			errs = append(errs, types.FieldError{Field: field.name, Message: field.name + " must be at most 80 characters"})
		}
	}
	if status != nil && !models.IsValidCampaignLeadStatus(*status) {
		errs = append(errs, types.FieldError{Field: "status", Message: fmt.Sprintf("status must be one of %s", strings.Join(models.CampaignLeadStatuses(), ", "))})
	}
	return errs
}

func isCampaignLeadE164(value string) bool {
	if len(value) < 9 || len(value) > 16 || value[0] != '+' || value[1] == '0' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

var campaignLeadReadOnlyFields = []string{"lead_id", "campaign_id", "attempts", "last_attempted_at", "created_at", "updated_at"}

func rejectCampaignLeadReadOnlyFields(body []byte) []types.FieldError {
	var supplied map[string]json.RawMessage
	if json.Unmarshal(body, &supplied) != nil {
		return nil
	}
	var errs []types.FieldError
	for _, field := range campaignLeadReadOnlyFields {
		if _, ok := supplied[field]; ok {
			errs = append(errs, types.FieldError{Field: field, Message: field + " is maintained by the server and cannot be set"})
		}
	}
	return errs
}

func rejectCreateCampaignLeadStatus(body []byte) []types.FieldError {
	var supplied map[string]json.RawMessage
	if json.Unmarshal(body, &supplied) != nil {
		return nil
	}
	if _, ok := supplied["status"]; !ok {
		return nil
	}
	return []types.FieldError{{Field: "status", Message: "status is initialized to pending and cannot be set when creating a lead"}}
}
