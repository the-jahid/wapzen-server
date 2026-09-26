package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/chatagents"
	"whatsapp-ai-caller-server/internal/models"
)

// ChatRuntimeReporter reports what the running server knows about serving a
// chat agent. Optional: without one, responses carry no runtime section.
type ChatRuntimeReporter interface {
	ChatAgentRuntime(agentID, provider string) *chatagents.RuntimeSection
}

// namespacePurger deletes a knowledge base's vectors from the vector store.
// Satisfied by *knowledgebases.Indexer. Deleting a chat agent cascades to the
// knowledge bases it owns, and the rows going away does not take their vectors
// with them — this is how the handler cleans up after the cascade. May be nil,
// and reports Enabled() false when indexing is not configured.
type namespacePurger interface {
	Enabled() bool
	PurgeNamespace(ctx context.Context, kb models.KnowledgeBase) error
}

type ChatAgentHandler struct {
	repo    *chatagents.Repository
	indexer namespacePurger
	runtime ChatRuntimeReporter
}

func NewChatAgentHandler(repo *chatagents.Repository, indexer namespacePurger, runtime ChatRuntimeReporter) *ChatAgentHandler {
	return &ChatAgentHandler{repo: repo, indexer: indexer, runtime: runtime}
}

// withRuntime attaches the running server's view of each agent.
func (h *ChatAgentHandler) withRuntime(resources ...*chatagents.Resource) {
	if h.runtime == nil {
		return
	}
	for _, resource := range resources {
		resource.Runtime = h.runtime.ChatAgentRuntime(resource.ID, resource.Model.Provider)
	}
}

type chatAgentEnvelope struct {
	Success bool                `json:"success"`
	Message string              `json:"message"`
	Data    chatagents.Resource `json:"data"`
}

type chatAgentListEnvelope struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    []chatagents.Resource `json:"data"`
	Meta    chatAgentListMeta     `json:"meta"`
}

type chatAgentListMeta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	TotalItems int `json:"total_items"`
}

func (h *ChatAgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	var req chatagents.CreateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body: " + err.Error()})
		return
	}
	if err := chatagents.ValidateCreate(req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: err.Error()})
		return
	}
	resource, err := h.repo.Create(r.Context(), user.ID, req)
	if err != nil {
		h.writeError(w, err, "failed to create chat agent")
		return
	}
	h.withRuntime(&resource)
	writeJSON(w, http.StatusCreated, chatAgentEnvelope{true, "Chat agent created successfully", resource})
}

func (h *ChatAgentHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}
	page, limit, err := parseChatPagination(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: err.Error()})
		return
	}
	resources, total, err := h.repo.List(r.Context(), user.ID, page, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "failed to list chat agents"})
		return
	}
	for i := range resources {
		h.withRuntime(&resources[i])
	}
	writeJSON(w, http.StatusOK, chatAgentListEnvelope{true, "Chat agents retrieved successfully", resources, chatAgentListMeta{page, limit, total}})
}

func (h *ChatAgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatAgentRequestIdentity(w, r)
	if !ok {
		return
	}
	resource, err := h.repo.GetByID(r.Context(), user.ID, id)
	if err != nil {
		h.writeError(w, err, "failed to get chat agent")
		return
	}
	h.withRuntime(&resource)
	writeJSON(w, http.StatusOK, chatAgentEnvelope{true, "Chat agent retrieved successfully", resource})
}

func (h *ChatAgentHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatAgentRequestIdentity(w, r)
	if !ok {
		return
	}
	var req chatagents.UpdateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body: " + err.Error()})
		return
	}
	if err := chatagents.ValidateUpdate(req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: err.Error()})
		return
	}
	resource, err := h.repo.Update(r.Context(), user.ID, id, req)
	if err != nil {
		h.writeError(w, err, "failed to update chat agent")
		return
	}
	h.withRuntime(&resource)
	writeJSON(w, http.StatusOK, chatAgentEnvelope{true, "Chat agent updated successfully", resource})
}

func (h *ChatAgentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatAgentRequestIdentity(w, r)
	if !ok {
		return
	}
	bases, _ := h.repo.KnowledgeBasesForChatAgent(r.Context(), id)
	if err := h.repo.Delete(r.Context(), user.ID, id); err != nil {
		h.writeError(w, err, "failed to delete chat agent")
		return
	}
	if h.indexer != nil && h.indexer.Enabled() {
		for _, base := range bases {
			namespace := base.Namespace
			if namespace == "" {
				continue
			}
			if err := h.indexer.PurgeNamespace(r.Context(), models.KnowledgeBase{ID: base.ID, NamespaceID: &namespace}); err != nil {
				log.Printf("chat agent delete: purge knowledge namespace %s: %v", base.ID, err)
			}
		}
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Chat agent deleted successfully", Data: map[string]any{"id": id, "deleted": true}})
}

func (h *ChatAgentHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, chatagents.ErrNotFound):
		writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "chat agent not found"})
		return
	case errors.Is(err, chatagents.ErrPhoneNumberNotFound):
		writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "phone number not found"})
		return
	case errors.Is(err, chatagents.ErrPhoneNumberRequired):
		writeJSON(w, http.StatusConflict, models.APIResponse{Success: false, Message: err.Error()})
		return
	case errors.Is(err, chatagents.ErrKnowledgeBaseNotFound), errors.Is(err, chatagents.ErrToolNotFound):
		writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: err.Error()})
		return
	case errors.Is(err, chatagents.ErrKnowledgeBaseConflict):
		writeJSON(w, http.StatusConflict, models.APIResponse{Success: false, Message: err.Error()})
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			message := "a chat agent with this name already exists"
			if strings.Contains(pgErr.ConstraintName, "phone_number") {
				message = "phone number is already assigned to another chat agent"
			}
			writeJSON(w, http.StatusConflict, models.APIResponse{Success: false, Message: message})
			return
		case "23503":
			writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "referenced resource not found"})
			return
		case "23514", "23502", "22P02":
			writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: pgErr.Message})
			return
		}
	}
	log.Printf("chat agent API: %s: %v", fallback, err)
	writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: fallback})
}

func chatAgentRequestIdentity(w http.ResponseWriter, r *http.Request) (models.User, string, bool) {
	user, ok := currentUser(w, r)
	if !ok {
		return models.User{}, "", false
	}
	id := strings.TrimSpace(chi.URLParam(r, "chat_agent_id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "chat_agent_id path parameter is required"})
		return models.User{}, "", false
	}
	return user, id, true
}

func parseChatPagination(r *http.Request) (int, int, error) {
	page, limit := 1, 20
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return 0, 0, errors.New("page must be a positive integer")
		}
		page = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, 0, errors.New("limit must be between 1 and 100")
		}
		limit = value
	}
	return page, limit, nil
}
