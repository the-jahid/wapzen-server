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

	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/outboundcampaigns"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// Default and bound values for the list query parameters, mirroring the
// documented schema for GET /v1/outbound-campaigns (page ≥ 1 default 1;
// limit 1..100 default 20). They match the other collections' bounds today but
// are named separately so either can move without dragging the other.
const (
	defaultOutboundCampaignPage  = 1
	defaultOutboundCampaignLimit = 20
	maxOutboundCampaignLimit     = 100
)

// outboundCampaignsListPath is the collection URL rendered into the list links.
const outboundCampaignsListPath = "/v1/outbound-campaigns"

// OutboundCampaignHandler serves the Outbound Campaign endpoints.
type OutboundCampaignHandler struct {
	repo *outboundcampaigns.Repository
	// agents resolves the agent a campaign dials with: it confirms the agent is
	// the caller's and reports the number that agent speaks on, which is why a
	// campaign never names a phone number of its own.
	agents *agents.Repository
}

// NewOutboundCampaignHandler creates an outbound campaign handler with its
// dependencies.
func NewOutboundCampaignHandler(repo *outboundcampaigns.Repository, agentsRepo *agents.Repository) *OutboundCampaignHandler {
	return &OutboundCampaignHandler{repo: repo, agents: agentsRepo}
}

// resolveCampaignAgent checks that agentID names one of the caller's agents and
// that the agent has a phone number to dial from, writing the response itself
// and returning false when it does not.
//
// The number is deliberately not returned: the campaign stores the agent alone
// and reads the number back through it, so an agent reassigned to another number
// tomorrow moves its campaigns with it instead of leaving them pointing at a
// number their agent no longer answers on.
func (h *OutboundCampaignHandler) resolveCampaignAgent(w http.ResponseWriter, r *http.Request, userID, agentID string) bool {
	phoneNumberID, exists, err := h.agents.LookupPhoneNumberForAgent(r.Context(), userID, agentID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to load agent",
		})
		return false
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: "agent not found",
		})
		return false
	}
	if phoneNumberID == nil || strings.TrimSpace(*phoneNumberID) == "" {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid request body",
			Errors: []types.FieldError{{
				Field:   "agent_id",
				Message: "this agent has no phone number assigned; assign one to the agent before using it for outbound calling",
			}},
		})
		return false
	}
	return true
}

type outboundCampaignEnvelope struct {
	Success bool                    `json:"success"`
	Message string                  `json:"message"`
	Data    models.OutboundCampaign `json:"data"`
	Links   outboundCampaignLinks   `json:"links"`
}

type outboundCampaignListEnvelope struct {
	Success bool                      `json:"success"`
	Message string                    `json:"message"`
	Data    []models.OutboundCampaign `json:"data"`
	Meta    *types.Meta               `json:"meta,omitempty"`
	Links   types.ListLinks           `json:"links"`
}

type outboundCampaignLinks struct {
	Self string `json:"self"`
}

type outboundCampaignDeleteEnvelope struct {
	Success bool                       `json:"success"`
	Message string                     `json:"message"`
	Data    outboundCampaignDeleteData `json:"data"`
}

type outboundCampaignDeleteData struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// createOutboundCampaignRequest is the Create Outbound Campaign body. Only
// CampaignName is required; the budget is a pointer so an absent one is stored
// as NULL (uncapped) rather than as a budget of zero.
//
// The counters are deliberately absent: they are server-maintained, and a
// caller that supplied them would otherwise believe it had seeded a campaign's
// totals. rejectReadOnlyCampaignFields turns any attempt into a field error.
type createOutboundCampaignRequest struct {
	CampaignName string   `json:"campaign_name"`
	BudgetUSD    *float64 `json:"budget_usd"`
	// AgentID is optional at create time so a campaign can be drafted before its
	// agent is chosen, but it is required before the campaign may run.
	AgentID string `json:"agent_id"`
}

// Create stores an outbound campaign owned by the authenticated user. The
// campaign starts as a draft with every counter at zero; nothing is dialled here,
// since a campaign only calls once it is given leads to call.
//
// Create godoc
// @Summary      Create Outbound Campaign
// @Description  Creates an outbound campaign owned by the authenticated API key owner. campaign_name is the only required field and must be unique among the owner's campaigns. The campaign is created at status draft with every counter at zero; omitting budget_usd leaves it uncapped. agent_id is optional here but required before the campaign can run: it must name one of the owner's agents that already has a phone number assigned, and that agent's number is the one the campaign dials from — a campaign never names a number of its own.
// @Tags         Outbound Campaign
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.createOutboundCampaignRequest  true  "Campaign to create"
// @Success      201   {object}  handlers.outboundCampaignEnvelope
// @Failure      400   {object}  types.ErrorEnvelope
// @Failure      401   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/outbound-campaigns [post]
func (h *OutboundCampaignHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	req, ok := decodeCreateOutboundCampaignRequest(w, r)
	if !ok {
		return
	}

	var agentID *string
	if req.AgentID != "" {
		if !h.resolveCampaignAgent(w, r, user.ID, req.AgentID) {
			return
		}
		agentID = &req.AgentID
	}

	campaign, err := h.repo.Create(r.Context(), models.NewOutboundCampaign{
		UserID:    user.ID,
		Name:      req.CampaignName,
		BudgetUSD: req.BudgetUSD,
		AgentID:   agentID,
	})
	if err != nil {
		if errors.Is(err, outboundcampaigns.ErrNameTaken) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a campaign with this name already exists",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to create outbound campaign",
		})
		return
	}

	writeJSON(w, http.StatusCreated, outboundCampaignEnvelope{
		Success: true,
		Message: "Outbound campaign created successfully",
		Data:    campaign,
		Links:   outboundCampaignLinks{Self: outboundCampaignsListPath + "/" + campaign.ID},
	})
}

// List returns a paginated page of the authenticated user's campaigns,
// optionally narrowed to one status.
//
// List godoc
// @Summary      List Outbound Campaigns
// @Description  Returns a paginated list of the outbound campaigns owned by the authenticated API key owner, newest first, each with its current counters.
// @Tags         Outbound Campaign
// @Produce      json
// @Security     BearerAuth
// @Param        page    query     int     false  "Page number (1-based)"
// @Param        limit   query     int     false  "Items per page (1-100)"
// @Param        status  query     string  false  "Filter by campaign status"
// @Success      200     {object}  handlers.outboundCampaignListEnvelope
// @Failure      400     {object}  types.ErrorEnvelope
// @Failure      401     {object}  models.APIResponse
// @Failure      500     {object}  models.APIResponse
// @Router       /v1/outbound-campaigns [get]
func (h *OutboundCampaignHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	page, limit, status, fieldErrs := parseOutboundCampaignListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	list, total, err := h.repo.ListByUser(r.Context(), user.ID, status, limit, (page-1)*limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list outbound campaigns",
		})
		return
	}

	meta := buildPaginationMeta(page, limit, total)

	writeJSON(w, http.StatusOK, outboundCampaignListEnvelope{
		Success: true,
		Message: "Outbound campaigns retrieved successfully",
		Data:    list,
		Meta:    &meta,
		Links:   buildOutboundCampaignListLinks(page, limit, total, status),
	})
}

// parseOutboundCampaignListQuery reads and validates the page/limit/status
// query parameters, returning the effective values plus any per-field
// validation errors. Absent parameters fall back to their documented defaults,
// and an absent status means every status.
func parseOutboundCampaignListQuery(q url.Values) (page, limit int, status string, errs []types.FieldError) {
	page, limit = defaultOutboundCampaignPage, defaultOutboundCampaignLimit

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err != nil || p < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			page = p
		}
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if l, err := strconv.Atoi(raw); err != nil || l < 1 || l > maxOutboundCampaignLimit {
			errs = append(errs, types.FieldError{Field: "limit", Message: fmt.Sprintf("limit must be between 1 and %d", maxOutboundCampaignLimit)})
		} else {
			limit = l
		}
	}

	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		if !models.IsValidCampaignStatus(raw) {
			errs = append(errs, types.FieldError{
				Field:   "status",
				Message: fmt.Sprintf("status must be one of %s", strings.Join(models.CampaignStatuses(), ", ")),
			})
		} else {
			status = raw
		}
	}

	return page, limit, status, errs
}

// buildOutboundCampaignListLinks builds the collection link block, carrying the
// status filter on every page URL so paging through a filtered listing stays
// filtered.
func buildOutboundCampaignListLinks(page, limit, total int, status string) types.ListLinks {
	links := buildListLinks(outboundCampaignsListPath, page, limit, total, nil)
	if status == "" {
		return links
	}

	suffix := "&status=" + status
	links.Self += suffix
	links.First += suffix
	links.Last += suffix
	if links.Previous != nil {
		previous := *links.Previous + suffix
		links.Previous = &previous
	}
	if links.Next != nil {
		next := *links.Next + suffix
		links.Next = &next
	}
	return links
}

// campaignAnalyticsEnvelope is the Analytics response. Days echoes the size of
// the daily window that was actually used, since the request's value is clamped.
type campaignAnalyticsEnvelope struct {
	Success bool                     `json:"success"`
	Message string                   `json:"message"`
	Data    models.CampaignAnalytics `json:"data"`
	Meta    campaignAnalyticsMeta    `json:"meta"`
	Links   outboundCampaignLinks    `json:"links"`
}

type campaignAnalyticsMeta struct {
	Days int `json:"days" example:"14"`
}

// Analytics returns one campaign's performance, aggregated from its leads and
// the calls it placed.
//
// Analytics godoc
// @Summary      Get Outbound Campaign Analytics
// @Description  Returns the campaign's performance: lead and call breakdowns by status, pickup/success/reach rates, talk time, a zero-filled daily series of calls placed and answered, and the most common hangup reasons. Everything is aggregated from the leads and calls themselves rather than read off the campaign's stored counters. days sets the length of the daily series (1-90, default 14).
// @Tags         Outbound Campaign
// @Produce      json
// @Security     BearerAuth
// @Param        campaign_id  path      string  true   "Campaign id"
// @Param        days         query     int     false  "Days of daily activity to return (1-90)"
// @Success      200          {object}  handlers.campaignAnalyticsEnvelope
// @Failure      400          {object}  types.ErrorEnvelope
// @Failure      401          {object}  models.APIResponse
// @Failure      404          {object}  models.APIResponse
// @Failure      500          {object}  models.APIResponse
// @Router       /v1/outbound-campaigns/{campaign_id}/analytics [get]
func (h *OutboundCampaignHandler) Analytics(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}
	days, ok := parseAnalyticsDays(w, r.URL.Query())
	if !ok {
		return
	}

	analytics, err := h.repo.Analytics(r.Context(), user.ID, campaignID, days)
	if err != nil {
		if errors.Is(err, outboundcampaigns.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "outbound campaign not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "failed to load campaign analytics"})
		return
	}

	writeJSON(w, http.StatusOK, campaignAnalyticsEnvelope{
		Success: true,
		Message: "Campaign analytics retrieved successfully",
		Data:    analytics,
		Meta:    campaignAnalyticsMeta{Days: len(analytics.Daily)},
		Links:   outboundCampaignLinks{Self: outboundCampaignsListPath + "/" + campaignID + "/analytics"},
	})
}

// parseAnalyticsDays reads the optional days window, writing the 400 itself when
// it is not a number in range. An absent value takes the default; the bound is
// enforced here rather than silently clamped, so a caller asking for a year is
// told the limit instead of quietly given three months.
func parseAnalyticsDays(w http.ResponseWriter, q url.Values) (int, bool) {
	raw := strings.TrimSpace(q.Get("days"))
	if raw == "" {
		return models.CampaignAnalyticsDefaultDays, true
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > models.CampaignAnalyticsMaxDays {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors: []types.FieldError{{
				Field:   "days",
				Message: fmt.Sprintf("days must be between 1 and %d", models.CampaignAnalyticsMaxDays),
			}},
		})
		return 0, false
	}
	return days, true
}

// Get returns a single campaign owned by the authenticated user.
//
// Get godoc
// @Summary      Get Outbound Campaign
// @Description  Returns a single outbound campaign by id, scoped to the authenticated API key owner, with its counters and the pickup_rate and success_rate derived from them.
// @Tags         Outbound Campaign
// @Produce      json
// @Security     BearerAuth
// @Param        campaign_id  path      string  true  "Campaign id"
// @Success      200          {object}  handlers.outboundCampaignEnvelope
// @Failure      400          {object}  models.APIResponse
// @Failure      401          {object}  models.APIResponse
// @Failure      404          {object}  models.APIResponse
// @Failure      500          {object}  models.APIResponse
// @Router       /v1/outbound-campaigns/{campaign_id} [get]
func (h *OutboundCampaignHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}

	campaign, err := h.repo.GetByUser(r.Context(), user.ID, campaignID)
	if err != nil {
		h.writeLoadError(w, err, "failed to get outbound campaign")
		return
	}

	writeJSON(w, http.StatusOK, outboundCampaignEnvelope{
		Success: true,
		Message: "Outbound campaign retrieved successfully",
		Data:    campaign,
		Links:   outboundCampaignLinks{Self: outboundCampaignsListPath + "/" + campaign.ID},
	})
}

// updateOutboundCampaignRequest is the partial-update body. Every field is
// optional; at least one must be supplied.
//
// BudgetUSD is decoded as raw JSON because three cases have to stay apart:
// absent (keep the stored budget), null (remove the cap), and a number (set
// it). A *float64 would collapse the first two into nil.
type updateOutboundCampaignRequest struct {
	CampaignName *string         `json:"campaign_name"`
	Status       *string         `json:"status"`
	BudgetUSD    json.RawMessage `json:"budget_usd"`
	// AgentID is raw for the same three-way reason as BudgetUSD: absent keeps
	// the stored agent, null detaches it, and a string attaches that agent.
	AgentID json.RawMessage `json:"agent_id"`

	// budget is the decoded BudgetUSD, and clearBudget records that it was sent
	// as null. agent and clearAgent are the same pair for AgentID. All four are
	// filled in by decodeUpdateOutboundCampaignRequest.
	budget      *float64
	clearBudget bool
	agent       *string
	clearAgent  bool
}

// Update changes the name, status and budget of a campaign owned by the
// authenticated user.
//
// The status transition is checked against the stored row, so an illegal move
// (restarting a completed campaign, say) is refused before anything is written
// rather than being silently applied.
//
// Update godoc
// @Summary      Update Outbound Campaign
// @Description  Applies a partial update to a campaign owned by the authenticated API key owner. Only campaign_name, status, budget_usd and agent_id may be changed; at least one field must be supplied and omitted fields keep their stored value. Send budget_usd null to remove a spend cap, or agent_id null to detach the agent. agent_id must name one of the owner's agents that has a phone number assigned; the campaign dials from that agent's number, which is why the number itself is read-only. Legal status moves are draft/paused to running, running to paused or completed, and any live status to failed; completed and failed are terminal. A campaign cannot be running without an agent, so starting one without an agent, or detaching the agent of a running one, is refused with 409.
// @Tags         Outbound Campaign
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        campaign_id  path      string                                   true  "Campaign id"
// @Param        body         body      handlers.updateOutboundCampaignRequest   true  "Fields to change"
// @Success      200          {object}  handlers.outboundCampaignEnvelope
// @Failure      400          {object}  types.ErrorEnvelope
// @Failure      401          {object}  models.APIResponse
// @Failure      404          {object}  models.APIResponse
// @Failure      409          {object}  models.APIResponse
// @Failure      500          {object}  models.APIResponse
// @Router       /v1/outbound-campaigns/{campaign_id} [patch]
func (h *OutboundCampaignHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}

	req, ok := decodeUpdateOutboundCampaignRequest(w, r)
	if !ok {
		return
	}

	// The stored row supplies the status being moved from, and doubles as the
	// ownership check, so a campaign that is not the caller's reads as missing
	// before anything is written.
	current, err := h.repo.GetByUser(r.Context(), user.ID, campaignID)
	if err != nil {
		h.writeLoadError(w, err, "failed to load outbound campaign")
		return
	}

	if req.Status != nil && !models.CanTransitionCampaignStatus(current.Status, *req.Status) {
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: fmt.Sprintf("a %s campaign cannot be moved to %s", current.Status, *req.Status),
		})
		return
	}

	if req.agent != nil && !h.resolveCampaignAgent(w, r, user.ID, *req.agent) {
		return
	}

	// A campaign that is on the air has to have an agent to dial with, so the
	// agent this update leaves behind is checked against the status it leaves
	// behind: detaching the agent of a running campaign and starting one that
	// has none are the same missing pairing, caught here rather than at the
	// first call the dialler tries to place.
	if !h.checkRunningCampaignHasAgent(w, current, req) {
		return
	}

	campaign, err := h.repo.UpdateByUser(r.Context(), user.ID, campaignID, models.OutboundCampaignUpdate{
		Name:        req.CampaignName,
		Status:      req.Status,
		BudgetUSD:   req.budget,
		ClearBudget: req.clearBudget,
		AgentID:     req.agent,
		ClearAgent:  req.clearAgent,
	})
	if err != nil {
		switch {
		case errors.Is(err, outboundcampaigns.ErrNotFound):
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "outbound campaign not found",
			})
		case errors.Is(err, outboundcampaigns.ErrNameTaken):
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a campaign with this name already exists",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Message: "failed to update outbound campaign",
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, outboundCampaignEnvelope{
		Success: true,
		Message: "Outbound campaign updated successfully",
		Data:    campaign,
		Links:   outboundCampaignLinks{Self: outboundCampaignsListPath + "/" + campaign.ID},
	})
}

// Delete removes a campaign owned by the authenticated user.
//
// A running campaign is refused rather than deleted mid-flight: pause it first.
// Calls it has already placed are not touched — they are the call history, not
// the campaign's.
//
// Delete godoc
// @Summary      Delete Outbound Campaign
// @Description  Deletes a campaign by id, scoped to the authenticated API key owner. A running campaign is refused with 409; pause it first. Calls the campaign already placed are not deleted.
// @Tags         Outbound Campaign
// @Produce      json
// @Security     BearerAuth
// @Param        campaign_id  path      string  true  "Campaign id"
// @Success      200          {object}  handlers.outboundCampaignDeleteEnvelope
// @Failure      400          {object}  models.APIResponse
// @Failure      401          {object}  models.APIResponse
// @Failure      404          {object}  models.APIResponse
// @Failure      409          {object}  models.APIResponse
// @Failure      500          {object}  models.APIResponse
// @Router       /v1/outbound-campaigns/{campaign_id} [delete]
func (h *OutboundCampaignHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	campaignID, ok := outboundCampaignIDParam(w, r)
	if !ok {
		return
	}

	current, err := h.repo.GetByUser(r.Context(), user.ID, campaignID)
	if err != nil {
		h.writeLoadError(w, err, "failed to load outbound campaign")
		return
	}
	if current.Status == models.CampaignStatusRunning {
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: "pause the campaign before deleting it",
		})
		return
	}

	if err := h.repo.DeleteByUser(r.Context(), user.ID, campaignID); err != nil {
		if errors.Is(err, outboundcampaigns.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "outbound campaign not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to delete outbound campaign",
		})
		return
	}

	writeJSON(w, http.StatusOK, outboundCampaignDeleteEnvelope{
		Success: true,
		Message: "Outbound campaign deleted successfully",
		Data:    outboundCampaignDeleteData{ID: campaignID, Deleted: true},
	})
}

// checkRunningCampaignHasAgent enforces "a live campaign has an agent" against
// the state the update would leave: the status it ends in and the agent it ends
// with. It writes the 409 itself and returns false when the two do not agree.
//
// Only running is guarded. A paused campaign is allowed to sit without an agent
// — that is how an owner takes a campaign off the air to re-point it — and the
// terminal statuses are history, which stays readable however it ended.
//
// An update that touches neither the status nor the agent is let through even
// when the stored pair is already broken, which a running campaign whose agent
// was deleted is: that row is the deletion's doing, not this request's, and
// refusing to rename it would leave the owner unable to touch it at all.
func (h *OutboundCampaignHandler) checkRunningCampaignHasAgent(
	w http.ResponseWriter,
	current models.OutboundCampaign,
	req updateOutboundCampaignRequest,
) bool {
	if req.Status == nil && req.agent == nil && !req.clearAgent {
		return true
	}

	status := current.Status
	if req.Status != nil {
		status = *req.Status
	}
	if status != models.CampaignStatusRunning {
		return true
	}

	hasAgent := current.AgentID != nil
	switch {
	case req.clearAgent:
		hasAgent = false
	case req.agent != nil:
		hasAgent = true
	}
	if hasAgent {
		return true
	}

	message := "a running campaign needs an agent to dial with; assign one before starting it"
	if req.clearAgent {
		message = "a running campaign needs an agent to dial with; pause it before removing its agent"
	}
	writeJSON(w, http.StatusConflict, models.APIResponse{Success: false, Message: message})
	return false
}

// writeLoadError reports a failed campaign read: a missing (or someone else's)
// campaign as 404, anything else as a 500 carrying the caller-facing message.
func (h *OutboundCampaignHandler) writeLoadError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, outboundcampaigns.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: "outbound campaign not found",
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, models.APIResponse{
		Success: false,
		Message: message,
	})
}

// outboundCampaignIDParam reads and validates the {campaign_id} path parameter,
// writing a 400 and returning false when it is missing.
func outboundCampaignIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "campaign_id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "campaign_id path parameter is required",
		})
		return "", false
	}
	return id, true
}

// decodeCreateOutboundCampaignRequest reads and validates the Create body,
// writing the 400 itself and returning false when it is unusable.
func decodeCreateOutboundCampaignRequest(w http.ResponseWriter, r *http.Request) (createOutboundCampaignRequest, bool) {
	var req createOutboundCampaignRequest

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

	req.CampaignName = strings.TrimSpace(req.CampaignName)
	req.AgentID = strings.TrimSpace(req.AgentID)

	fieldErrs := validateCreateOutboundCampaignRequest(req)
	fieldErrs = append(fieldErrs, rejectReadOnlyCampaignFields(body)...)
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

// validateCreateOutboundCampaignRequest checks the create body against the same
// bounds the outbound_campaigns CHECK constraints enforce, so a bad value reads
// as a field error rather than a 500 from the insert.
func validateCreateOutboundCampaignRequest(req createOutboundCampaignRequest) []types.FieldError {
	var errs []types.FieldError

	switch {
	case req.CampaignName == "":
		errs = append(errs, types.FieldError{
			Field:   "campaign_name",
			Message: "campaign_name is required",
		})
	case len(req.CampaignName) > models.CampaignMaxNameLength:
		errs = append(errs, types.FieldError{
			Field:   "campaign_name",
			Message: fmt.Sprintf("campaign_name must be at most %d characters", models.CampaignMaxNameLength),
		})
	}

	if req.BudgetUSD != nil && *req.BudgetUSD < 0 {
		errs = append(errs, types.FieldError{
			Field:   "budget_usd",
			Message: "budget_usd must not be negative",
		})
	}

	return errs
}

// decodeUpdateOutboundCampaignRequest reads and validates the Update body,
// writing the 400 itself and returning false when it is unusable. The status
// transition is left to the caller, which has the stored row the move starts
// from.
func decodeUpdateOutboundCampaignRequest(w http.ResponseWriter, r *http.Request) (updateOutboundCampaignRequest, bool) {
	var req updateOutboundCampaignRequest

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

	if req.CampaignName != nil {
		trimmed := strings.TrimSpace(*req.CampaignName)
		req.CampaignName = &trimmed
	}
	if req.Status != nil {
		trimmed := strings.TrimSpace(*req.Status)
		req.Status = &trimmed
	}

	budgetErr := req.resolveBudget()
	agentErr := req.resolveAgent()

	fieldErrs := validateUpdateOutboundCampaignRequest(req)
	if budgetErr != nil {
		fieldErrs = append(fieldErrs, *budgetErr)
	}
	if agentErr != nil {
		fieldErrs = append(fieldErrs, *agentErr)
	}
	fieldErrs = append(fieldErrs, rejectReadOnlyCampaignFields(body)...)
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

// resolveBudget turns the raw budget_usd JSON into either a value to write or a
// request to clear the column, reporting a field error for anything else.
func (req *updateOutboundCampaignRequest) resolveBudget() *types.FieldError {
	raw := bytes.TrimSpace(req.BudgetUSD)
	if len(raw) == 0 {
		return nil // absent: keep the stored budget
	}
	if bytes.Equal(raw, []byte("null")) {
		req.clearBudget = true
		return nil
	}

	var budget float64
	if err := json.Unmarshal(raw, &budget); err != nil {
		return &types.FieldError{Field: "budget_usd", Message: "budget_usd must be a number or null"}
	}
	if budget < 0 {
		return &types.FieldError{Field: "budget_usd", Message: "budget_usd must not be negative"}
	}
	req.budget = &budget
	return nil
}

// resolveAgent turns the raw agent_id JSON into either an agent to attach or a
// request to detach the stored one, reporting a field error for anything else.
// Whether the agent exists and is the caller's is not decided here — that needs
// the database and the authenticated user, so the handler checks it.
func (req *updateOutboundCampaignRequest) resolveAgent() *types.FieldError {
	raw := bytes.TrimSpace(req.AgentID)
	if len(raw) == 0 {
		return nil // absent: keep the stored agent
	}
	if bytes.Equal(raw, []byte("null")) {
		req.clearAgent = true
		return nil
	}

	var agentID string
	if err := json.Unmarshal(raw, &agentID); err != nil {
		return &types.FieldError{Field: "agent_id", Message: "agent_id must be a string or null"}
	}
	// An empty string is the same intent as null rather than a lookup of the
	// agent named "", which no agent is.
	if agentID = strings.TrimSpace(agentID); agentID == "" {
		req.clearAgent = true
		return nil
	}
	req.agent = &agentID
	return nil
}

// validateUpdateOutboundCampaignRequest checks each supplied field, and rejects
// a body that would change nothing.
func validateUpdateOutboundCampaignRequest(req updateOutboundCampaignRequest) []types.FieldError {
	if req.CampaignName == nil && req.Status == nil &&
		len(bytes.TrimSpace(req.BudgetUSD)) == 0 && len(bytes.TrimSpace(req.AgentID)) == 0 {
		return []types.FieldError{{
			Field:   "body",
			Message: "supply at least one of campaign_name, status, budget_usd or agent_id",
		}}
	}

	var errs []types.FieldError

	if req.CampaignName != nil {
		switch {
		case *req.CampaignName == "":
			errs = append(errs, types.FieldError{
				Field:   "campaign_name",
				Message: "campaign_name must not be empty",
			})
		case len(*req.CampaignName) > models.CampaignMaxNameLength:
			errs = append(errs, types.FieldError{
				Field:   "campaign_name",
				Message: fmt.Sprintf("campaign_name must be at most %d characters", models.CampaignMaxNameLength),
			})
		}
	}

	if req.Status != nil && !models.IsValidCampaignStatus(*req.Status) {
		errs = append(errs, types.FieldError{
			Field:   "status",
			Message: fmt.Sprintf("status must be one of %s", strings.Join(models.CampaignStatuses(), ", ")),
		})
	}

	return errs
}

// readOnlyCampaignFields are the server-maintained counters, the derived rates,
// and the agent details a campaign reads off its agent. A body carrying one is
// refused rather than having it dropped: a caller that thinks it set a
// campaign's totals — or pointed it at a phone number — should hear otherwise.
//
// phone_number_id and phone_number are here because choosing the agent is what
// chooses the number. agent_id, the one half a caller does set, is not.
var readOnlyCampaignFields = []string{
	"leads_count", "calls_placed", "answered_calls", "successful_calls",
	"today_calls", "today_calls_date", "total_usage_seconds",
	"pickup_rate", "success_rate",
	"agent_name", "phone_number_id", "phone_number",
}

func rejectReadOnlyCampaignFields(body []byte) []types.FieldError {
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(body, &supplied); err != nil {
		return nil
	}

	var errs []types.FieldError
	for _, field := range readOnlyCampaignFields {
		if _, ok := supplied[field]; ok {
			errs = append(errs, types.FieldError{
				Field:   field,
				Message: fmt.Sprintf("%s is maintained by the server and cannot be set", field),
			})
		}
	}
	return errs
}
