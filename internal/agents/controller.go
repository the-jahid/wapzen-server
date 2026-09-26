package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/httpx"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// agentsPath is the collection URL the agent endpoints render into their links.
const agentsPath = "/v1/agents"

// Controller is the HTTP layer for agents: it decodes requests, hands them to
// the Service, and renders the documented envelopes. It holds no business
// rules; every domain error is mapped to a status in writeError.
type Controller struct {
	service *Service
}

// NewController creates the agents HTTP controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// agentEnvelope is the documented success envelope for a single agent resource.
type agentEnvelope struct {
	Success bool                `json:"success"`
	Message string              `json:"message"`
	Data    types.AgentResource `json:"data"`
	Links   agentLinks          `json:"links"`
}

type agentLinks struct {
	Self string `json:"self"`
}

func writeAgent(w http.ResponseWriter, status int, message string, res types.AgentResource) {
	httpx.WriteJSON(w, status, agentEnvelope{
		Success: true,
		Message: message,
		Data:    res,
		Links:   agentLinks{Self: agentsPath + "/" + res.ID},
	})
}

// Create persists a new agent owned by the authenticated user.
//
// Create godoc
// @Summary      Create Agent
// @Description  Creates a new outbound agent owned by the authenticated user. Only the agent.name field is required; every other field falls back to its schema default. If agent.phone_number_id is set, the phone number must belong to the user and must not already be assigned to another agent. Every id in knowledge_base.knowledge_base_ids must reference a knowledge base owned by the same user.
// @Tags         agents
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      types.CreateAgentRequest  true  "Agent configuration"
// @Success      201   {object}  agents.agentEnvelope
// @Failure      400   {object}  models.APIResponse
// @Failure      401   {object}  models.APIResponse
// @Failure      404   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/agents [post]
func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}

	var req types.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	res, err := c.service.Create(r.Context(), user.ID, req)
	if err != nil {
		writeError(w, err, "failed to create agent")
		return
	}
	writeAgent(w, http.StatusCreated, "Agent created successfully", res)
}

// List returns a paginated page of the authenticated user's agents, optionally
// trimmed to a sparse fieldset.
//
// List godoc
// @Summary      Get All Agents
// @Description  Returns a paginated list of the agents owned by the authenticated user. Supports sparse fieldsets via the fields query parameter (dot-notation selects a nested property, e.g. agent.language).
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        page    query     int     false  "Page number (1-based)"
// @Param        limit   query     int     false  "Items per page (1-100)"
// @Param        fields  query     string  false  "Comma-separated sparse fieldset"
// @Success      200     {object}  types.SuccessEnvelope
// @Failure      400     {object}  types.ErrorEnvelope
// @Failure      401     {object}  models.APIResponse
// @Failure      500     {object}  models.APIResponse
// @Router       /v1/agents [get]
func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}

	query, fieldErrs := parseListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	resources, total, err := c.service.List(r.Context(), user.ID, query.page, query.limit)
	if err != nil {
		writeError(w, err, "failed to list agents")
		return
	}

	data, err := applyFieldSelection(resources, query.fields)
	if err != nil {
		writeError(w, err, "failed to list agents")
		return
	}

	meta := httpx.PaginationMeta(query.page, query.limit, total)
	httpx.WriteJSON(w, http.StatusOK, types.SuccessEnvelope{
		Success: true,
		Message: "Agents retrieved successfully",
		Data:    data,
		Meta:    &meta,
		Links:   httpx.ListLinks(agentsPath, query.page, query.limit, total, query.fields),
	})
}

// Get returns a single agent owned by the authenticated user.
//
// Get godoc
// @Summary      Get Agent
// @Description  Returns a single agent owned by the authenticated user, including its attached knowledge base and tool ids.
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        agent_id  path      string  true  "Agent id"
// @Success      200       {object}  agents.agentEnvelope
// @Failure      400       {object}  models.APIResponse
// @Failure      401       {object}  models.APIResponse
// @Failure      404       {object}  models.APIResponse
// @Failure      500       {object}  models.APIResponse
// @Router       /v1/agents/{agent_id} [get]
func (c *Controller) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	agentID, ok := agentIDParam(w, r)
	if !ok {
		return
	}

	res, err := c.service.Get(r.Context(), user.ID, agentID)
	if err != nil {
		writeError(w, err, "failed to get agent")
		return
	}
	writeAgent(w, http.StatusOK, "Agent retrieved successfully", res)
}

// Update applies a partial update to the agent named by the agent_id path
// parameter. Only the fields present in the body are changed.
//
// Update godoc
// @Summary      Update Agent
// @Description  Applies a partial update to an outbound agent owned by the authenticated user. Only the supplied fields are changed; absent fields keep their stored value. Assigning agent.phone_number_id is rejected with 409 when another agent already uses that phone number. Sending knowledge_base.knowledge_base_ids replaces the agent's knowledge base attachments wholesale; omitting it leaves them untouched.
// @Tags         agents
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        agent_id  path      string                  true  "Agent id"
// @Param        body      body      map[string]interface{}  true  "Fields to change"
// @Success      200       {object}  agents.agentEnvelope
// @Failure      400       {object}  models.APIResponse
// @Failure      401       {object}  models.APIResponse
// @Failure      404       {object}  models.APIResponse
// @Failure      409       {object}  models.APIResponse
// @Failure      500       {object}  models.APIResponse
// @Router       /v1/agents/{agent_id} [patch]
func (c *Controller) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	agentID, ok := agentIDParam(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "request body is required")
		return
	}

	res, err := c.service.Update(r.Context(), user.ID, agentID, body)
	if err != nil {
		writeError(w, err, "failed to update agent")
		return
	}
	writeAgent(w, http.StatusOK, "Agent updated successfully", res)
}

// Delete removes the agent named by the agent_id path parameter.
//
// Delete godoc
// @Summary      Delete Agent
// @Description  Deletes an agent owned by the authenticated user. A knowledge base belongs to the agent that attached it, so this deletes those too — with their sources and every vector indexed under their namespaces. Detach any worth keeping first by removing its id from the agent's knowledge_base.knowledge_base_ids. Tools are shared rather than owned: they are detached and stay in the account for the agents that still use them. Any phone number assigned to the agent is released rather than deleted.
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        agent_id  path      string  true  "Agent id"
// @Success      200       {object}  types.SuccessEnvelope
// @Failure      400       {object}  models.APIResponse
// @Failure      401       {object}  models.APIResponse
// @Failure      404       {object}  models.APIResponse
// @Failure      500       {object}  models.APIResponse
// @Router       /v1/agents/{agent_id} [delete]
func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	agentID, ok := agentIDParam(w, r)
	if !ok {
		return
	}

	if err := c.service.Delete(r.Context(), user.ID, agentID); err != nil {
		writeError(w, err, "failed to delete agent")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, types.SuccessEnvelope{
		Success: true,
		Message: "Agent deleted successfully",
		Data:    types.DeleteData{ID: agentID, Deleted: true},
	})
}

// agentIDParam reads the agent_id path parameter, answering 400 when it is blank.
func agentIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	agentID := strings.TrimSpace(chi.URLParam(r, "agent_id"))
	if agentID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "agent_id path parameter is required")
		return "", false
	}
	return agentID, true
}

// writeError maps a service error onto its HTTP status. Domain errors carry the
// message the client reads (including the offending id for an attachment
// error); anything unexpected is logged and answered with the generic fallback.
func writeError(w http.ResponseWriter, err error, fallback string) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrAgentNotFound),
		errors.Is(err, ErrUserNotFound),
		errors.Is(err, ErrPhoneNumberNotFound),
		errors.Is(err, ErrKnowledgeBaseNotFound),
		errors.Is(err, ErrToolNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrAgentNameTaken),
		errors.Is(err, ErrPhoneNumberAssignmentConflict),
		errors.Is(err, ErrKnowledgeBaseAttachmentConflict):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	default:
		log.Printf("agents: %s: %v", fallback, err)
		httpx.WriteError(w, http.StatusInternalServerError, fallback)
	}
}
