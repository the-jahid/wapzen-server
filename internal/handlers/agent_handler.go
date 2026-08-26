package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// inboundCallRefresher reconnects the WhatsApp session for a phone number so its
// inbound-call handling matches the number's current agent assignment. Satisfied
// by *whatsapplogin.Manager; kept as a small interface so the handler does not
// depend on the manager's concrete type. May be nil, in which case assignment
// changes take effect on the number's next reconnect instead of immediately.
type inboundCallRefresher interface {
	RefreshInboundCallHandling(phoneNumberID string)
}

// namespacePurger deletes a knowledge base's vectors from the vector store.
// Satisfied by *knowledgebases.Indexer. Deleting an agent now cascades to the
// knowledge bases it owns, and the rows going away does not take their vectors
// with them — this is how the handler cleans up after the cascade. May be nil,
// and reports Enabled() false when indexing is not configured; in either case
// the delete still succeeds and the namespaces are simply left in place.
type namespacePurger interface {
	Enabled() bool
	PurgeNamespace(ctx context.Context, kb models.KnowledgeBase) error
}

// AgentHandler handles agent CRUD endpoints.
type AgentHandler struct {
	repo    *agents.Repository
	inbound inboundCallRefresher
	indexer namespacePurger
}

// NewAgentHandler creates an agent handler with its dependencies. inbound may be
// nil to disable the auto-reconnect that applies phone-number assignment changes
// to a running WhatsApp session; indexer may be nil to skip purging the vector
// namespaces of the knowledge bases a deleted agent takes with it.
func NewAgentHandler(repo *agents.Repository, inbound inboundCallRefresher, indexer namespacePurger) *AgentHandler {
	return &AgentHandler{repo: repo, inbound: inbound, indexer: indexer}
}

// purgeKnowledgeBaseNamespaces deletes the vectors of knowledge bases that have
// just been removed by an agent delete.
//
// Best effort, like the knowledge-base delete endpoint's own purge: the rows are
// already gone, so a namespace that could not be dropped is logged rather than
// turned into a failed delete. It leaves vectors nothing can reach, not a
// half-deleted agent.
func (h *AgentHandler) purgeKnowledgeBaseNamespaces(ctx context.Context, bases []models.AgentKnowledgeBase) {
	if h.indexer == nil || !h.indexer.Enabled() {
		return
	}
	for _, base := range bases {
		namespace := base.Namespace
		if namespace == "" {
			continue
		}
		kb := models.KnowledgeBase{ID: base.ID, NamespaceID: &namespace}
		if err := h.indexer.PurgeNamespace(ctx, kb); err != nil {
			log.Printf("agent delete: purging vector namespace for knowledge base %s failed: %v", base.ID, err)
		}
	}
}

// refreshInbound reconnects each distinct, non-blank phone number so its
// inbound-call handling is re-evaluated against the current agent assignment.
// Nil entries, blanks, and duplicates are skipped; a nil refresher is a no-op.
func (h *AgentHandler) refreshInbound(phoneNumberIDs ...*string) {
	if h.inbound == nil {
		return
	}
	seen := make(map[string]struct{}, len(phoneNumberIDs))
	for _, p := range phoneNumberIDs {
		if p == nil {
			continue
		}
		id := strings.TrimSpace(*p)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		h.inbound.RefreshInboundCallHandling(id)
	}
}

// samePhoneNumber reports whether two optional phone-number assignments are
// equal after trimming (both unset counts as equal), so an update that leaves the
// assignment untouched triggers no reconnect.
func samePhoneNumber(a, b *string) bool {
	norm := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	return norm(a) == norm(b)
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
// @Success      201   {object}  handlers.agentEnvelope
// @Failure      400   {object}  models.APIResponse
// @Failure      401   {object}  models.APIResponse
// @Failure      404   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/agents [post]
func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	var req types.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	if req.Agent == nil || strings.TrimSpace(req.Agent.Name) == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "agent.name is required",
		})
		return
	}

	res, err := h.repo.Create(r.Context(), user.ID, req)
	if err != nil {
		if writeAgentAssignmentError(w, err) {
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23503": // foreign_key_violation: user row deleted mid-request
				if pgErr.ConstraintName == "agents_phone_number_id_fkey" {
					writeJSON(w, http.StatusNotFound, models.APIResponse{
						Success: false,
						Message: "phone number not found",
					})
					return
				}
				writeJSON(w, http.StatusNotFound, models.APIResponse{
					Success: false,
					Message: "user not found",
				})
				return
			case "23505": // unique_violation: agent name already taken for this user
				writeJSON(w, http.StatusConflict, models.APIResponse{
					Success: false,
					Message: "an agent with this name already exists",
				})
				return
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
			Message: "failed to create agent",
		})
		return
	}

	// A new agent can only add an assignment, so reconnect the assigned number (if
	// any) to install its inbound-call handler.
	h.refreshInbound(res.Agent.PhoneNumberID)

	writeJSON(w, http.StatusCreated, agentEnvelope{
		Success: true,
		Message: "Agent created successfully",
		Data:    res,
		Links:   agentLinks{Self: "/v1/agents/" + res.ID},
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
// @Success      200       {object}  handlers.agentEnvelope
// @Failure      400       {object}  models.APIResponse
// @Failure      401       {object}  models.APIResponse
// @Failure      404       {object}  models.APIResponse
// @Failure      500       {object}  models.APIResponse
// @Router       /v1/agents/{agent_id} [get]
func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	agentID := strings.TrimSpace(chi.URLParam(r, "agent_id"))
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "agent_id path parameter is required",
		})
		return
	}

	res, err := h.repo.GetByID(r.Context(), user.ID, agentID)
	if err != nil {
		if errors.Is(err, agents.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "agent not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to get agent",
		})
		return
	}

	writeJSON(w, http.StatusOK, agentEnvelope{
		Success: true,
		Message: "Agent retrieved successfully",
		Data:    res,
		Links:   agentLinks{Self: "/v1/agents/" + res.ID},
	})
}

// Delete removes the agent named by the agent_id path parameter, together with
// the knowledge bases and tools it owns (cascaded by the schema) and the vectors
// those knowledge bases had indexed (purged here, since no cascade reaches the
// vector store).
//
// Delete godoc
// @Summary      Delete Agent
// @Description  Deletes an agent owned by the authenticated user. A knowledge base or tool belongs to the agent that attached it, so this deletes them too — the knowledge bases with their sources and every vector indexed under their namespaces, and the tools with their configuration. Detach anything worth keeping first by removing its id from the agent's knowledge_base.knowledge_base_ids or tools.tool_ids. Any phone number assigned to the agent is released rather than deleted.
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
func (h *AgentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	agentID := strings.TrimSpace(chi.URLParam(r, "agent_id"))
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "agent_id path parameter is required",
		})
		return
	}

	// Capture the assigned number before deletion so its inbound handler can be
	// removed afterward (deleting the agent leaves the number unassigned).
	assignedPhone, _ := h.repo.PhoneNumberIDForAgent(r.Context(), user.ID, agentID)

	// And the namespaces of the knowledge bases the delete is about to cascade
	// to, which are unreachable once their rows are gone. Read here rather than
	// after the delete for that reason; nothing is purged unless the delete
	// below actually succeeds, so a failed or unauthorized delete touches no
	// vectors.
	doomedBases, _ := h.repo.KnowledgeBasesForAgent(r.Context(), agentID)

	if err := h.repo.Delete(r.Context(), user.ID, agentID); err != nil {
		if errors.Is(err, agents.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "agent not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to delete agent",
		})
		return
	}

	h.refreshInbound(assignedPhone)
	h.purgeKnowledgeBaseNamespaces(r.Context(), doomedBases)

	writeJSON(w, http.StatusOK, types.SuccessEnvelope{
		Success: true,
		Message: "Agent deleted successfully",
		Data:    types.DeleteData{ID: agentID, Deleted: true},
	})
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
// @Success      200       {object}  handlers.agentEnvelope
// @Failure      400       {object}  models.APIResponse
// @Failure      401       {object}  models.APIResponse
// @Failure      404       {object}  models.APIResponse
// @Failure      409       {object}  models.APIResponse
// @Failure      500       {object}  models.APIResponse
// @Router       /v1/agents/{agent_id} [patch]
func (h *AgentHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	agentID := strings.TrimSpace(chi.URLParam(r, "agent_id"))
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "agent_id path parameter is required",
		})
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

	// Capture the assignment before the update so a change of assigned number can
	// reconnect both the old and the new number afterward. Best-effort: on error
	// oldPhone is nil and only the new number (if any) is reconnected.
	oldPhone, _ := h.repo.PhoneNumberIDForAgent(r.Context(), user.ID, agentID)

	res, err := h.repo.Update(r.Context(), user.ID, agentID, body)
	if err != nil {
		if errors.Is(err, agents.ErrAgentNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "agent not found",
			})
			return
		}
		if writeAgentAssignmentError(w, err) {
			return
		}
		var invalid *agents.InvalidRequestError
		if errors.As(err, &invalid) {
			writeJSON(w, http.StatusBadRequest, models.APIResponse{
				Success: false,
				Message: invalid.Error(),
			})
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23503":
				if pgErr.ConstraintName == "agents_phone_number_id_fkey" {
					writeJSON(w, http.StatusNotFound, models.APIResponse{
						Success: false,
						Message: "phone number not found",
					})
					return
				}
			case "23505": // unique_violation: agent name already taken for this user
				writeJSON(w, http.StatusConflict, models.APIResponse{
					Success: false,
					Message: "an agent with this name already exists",
				})
				return
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
			Message: "failed to update agent",
		})
		return
	}

	// Only reconnect when the assigned number actually changed, so unrelated edits
	// (prompt, voice, status, …) never trigger a needless WhatsApp reconnect.
	if !samePhoneNumber(oldPhone, res.Agent.PhoneNumberID) {
		h.refreshInbound(oldPhone, res.Agent.PhoneNumberID)
	}

	writeJSON(w, http.StatusOK, agentEnvelope{
		Success: true,
		Message: "Agent updated successfully",
		Data:    res,
		Links:   agentLinks{Self: "/v1/agents/" + res.ID},
	})
}

func writeAgentAssignmentError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, agents.ErrPhoneNumberNotFound):
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: "phone number not found",
		})
		return true
	case errors.Is(err, agents.ErrPhoneNumberAssignmentConflict):
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: "phone number is already assigned to another agent for this call direction",
		})
		return true
	case errors.Is(err, agents.ErrKnowledgeBaseNotFound), errors.Is(err, agents.ErrToolNotFound):
		// The error carries the offending id, which matters when the request
		// attached several knowledge bases or tools at once.
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: err.Error(),
		})
		return true
	case errors.Is(err, agents.ErrKnowledgeBaseAttachmentConflict), errors.Is(err, agents.ErrToolAttachmentConflict):
		// A knowledge base or tool belongs to one agent, so taking one that is
		// already another agent's is refused rather than silently detaching it
		// there — the same 409 a phone number already assigned elsewhere gets.
		// The message carries the offending id.
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: err.Error(),
		})
		return true
	default:
		return false
	}
}
