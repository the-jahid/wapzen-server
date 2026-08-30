package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/chatconversations"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

// Default and bound values for the conversation list query, mirroring the
// documented schema for GET /v1/chat-conversations. The page size matches the
// calls collection: a chat agent accumulates threads at the same rate a number
// accumulates calls.
const (
	defaultChatConversationsPage  = 1
	defaultChatConversationsLimit = 50
	maxChatConversationsLimit     = 200
)

// maxChatMessageLength bounds one dashboard-sent message. WhatsApp itself
// accepts far more, but a body this long is a paste accident rather than a
// reply, and the whole thing is stored in the transcript either way.
const maxChatMessageLength = 4096

// chatConversationsListPath is the collection URL rendered into the list links.
const chatConversationsListPath = "/v1/chat-conversations"

// ChatConversationHandler serves the saved WhatsApp text threads a chat agent
// handled. Threads are written by the message runtime as it answers; this side
// reads them, closes or deletes one, and can send a message on one — which is
// why it also needs the login manager that owns the live WhatsApp session.
type ChatConversationHandler struct {
	repo         *chatconversations.Repository
	loginManager *whatsapplogin.Manager
}

// NewChatConversationHandler creates a chat conversation handler.
func NewChatConversationHandler(
	repo *chatconversations.Repository,
	loginManager *whatsapplogin.Manager,
) *ChatConversationHandler {
	return &ChatConversationHandler{repo: repo, loginManager: loginManager}
}

type chatConversationEnvelope struct {
	Success bool                    `json:"success"`
	Message string                  `json:"message"`
	Data    models.ChatConversation `json:"data"`
	Links   chatConversationLinks   `json:"links"`
}

type chatConversationLinks struct {
	Self string `json:"self"`
}

type chatConversationListEnvelope struct {
	Success bool                      `json:"success"`
	Message string                    `json:"message"`
	Data    []models.ChatConversation `json:"data"`
	Meta    *types.Meta               `json:"meta,omitempty"`
	Links   types.ListLinks           `json:"links"`
}

type updateChatConversationRequest struct {
	Status *string `json:"status"`
}

type sendChatMessageRequest struct {
	Content string `json:"content"`
}

type chatMessageEnvelope struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    models.ChatMessage    `json:"data"`
	Links   chatConversationLinks `json:"links"`
}

// List returns a page of the authenticated user's chat threads, most recently
// active first. chat_agent_id, phone_number_id, status and search narrow it, so
// one agent's conversation tab reads the same collection everything else does.
func (h *ChatConversationHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	page, limit, filter, fieldErrs := parseChatConversationListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	h.writeList(w, r, user.ID, filter, page, limit, chatConversationsListPath)
}

// ListByChatAgent is the same listing narrowed to one chat agent, addressed by
// the agent rather than by a query parameter. It is what the dashboard's
// Conversation tab reads; a chat agent id belonging to somebody else simply
// matches nothing, because the listing is scoped to the authenticated user too.
func (h *ChatConversationHandler) ListByChatAgent(w http.ResponseWriter, r *http.Request) {
	user, chatAgentID, ok := chatAgentRequestIdentity(w, r)
	if !ok {
		return
	}

	page, limit, filter, fieldErrs := parseChatConversationListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}
	filter.ChatAgentID = chatAgentID

	basePath := "/v1/chat-agents/" + chatAgentID + "/conversations"
	h.writeList(w, r, user.ID, filter, page, limit, basePath)
}

// writeList is the shared tail of both listings: the same page, meta and links
// whichever URL asked for it.
func (h *ChatConversationHandler) writeList(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	filter chatconversations.Filter,
	page, limit int,
	basePath string,
) {
	list, total, err := h.repo.ListByUser(r.Context(), userID, filter, limit, (page-1)*limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list conversations",
		})
		return
	}

	meta := buildPaginationMeta(page, limit, total)

	writeJSON(w, http.StatusOK, chatConversationListEnvelope{
		Success: true,
		Message: "Conversations retrieved successfully",
		Data:    list,
		Meta:    &meta,
		Links:   buildListLinks(basePath, page, limit, total, nil),
	})
}

// Get returns one thread with its full transcript.
func (h *ChatConversationHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatConversationRequestIdentity(w, r)
	if !ok {
		return
	}

	conversation, err := h.repo.GetByUser(r.Context(), user.ID, id)
	if err != nil {
		h.writeError(w, err, "failed to get conversation")
		return
	}

	messages, err := h.repo.ListMessages(r.Context(), conversation.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to load conversation transcript",
		})
		return
	}
	conversation.Messages = messages

	writeJSON(w, http.StatusOK, chatConversationEnvelope{
		Success: true,
		Message: "Conversation retrieved successfully",
		Data:    conversation,
		Links:   chatConversationLinks{Self: chatConversationsListPath + "/" + conversation.ID},
	})
}

// Update closes or reopens a thread. Only status may be changed: the messages
// are what happened, so nothing else about a saved thread is editable.
func (h *ChatConversationHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatConversationRequestIdentity(w, r)
	if !ok {
		return
	}

	var req updateChatConversationRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body: " + err.Error(),
		})
		return
	}
	if req.Status == nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "status is required",
		})
		return
	}
	status := strings.TrimSpace(*req.Status)
	if status != models.ChatConversationStatusOpen && status != models.ChatConversationStatusClosed {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "status must be one of open, closed",
		})
		return
	}

	conversation, err := h.repo.UpdateStatusByUser(r.Context(), user.ID, id, status)
	if err != nil {
		h.writeError(w, err, "failed to update conversation")
		return
	}

	writeJSON(w, http.StatusOK, chatConversationEnvelope{
		Success: true,
		Message: "Conversation updated successfully",
		Data:    conversation,
		Links:   chatConversationLinks{Self: chatConversationsListPath + "/" + conversation.ID},
	})
}

// SendMessage sends a message on one thread from the dashboard — a person
// taking the conversation over from the agent — and appends it to the saved
// transcript. It goes out on the number the thread runs on, so that number has
// to still be assigned and connected.
//
// The message is stored with the "assistant" role: to the contact it is the same
// party speaking, and storing it any other way would make the thread read as if
// the contact had said it.
func (h *ChatConversationHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatConversationRequestIdentity(w, r)
	if !ok {
		return
	}

	var req sendChatMessageRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body: " + err.Error(),
		})
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "content is required"})
		return
	}
	if len(content) > maxChatMessageLength {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "content must be at most " + strconv.Itoa(maxChatMessageLength) + " characters",
		})
		return
	}

	conversation, err := h.repo.GetByUser(r.Context(), user.ID, id)
	if err != nil {
		h.writeError(w, err, "failed to get conversation")
		return
	}
	// A thread whose number was unpaired keeps its history but has nothing left
	// to send from, which is a conflict rather than a bad request.
	if conversation.PhoneNumberID == nil || strings.TrimSpace(*conversation.PhoneNumberID) == "" {
		writeJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: "this conversation's phone number is no longer connected to the account",
		})
		return
	}
	if h.loginManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{
			Success: false,
			Message: "WhatsApp messaging is not available on this server",
		})
		return
	}

	chatAgentID := ""
	if conversation.ChatAgentID != nil {
		chatAgentID = *conversation.ChatAgentID
	}
	if err := h.loginManager.SendChatMessage(
		r.Context(), user.ID, *conversation.PhoneNumberID, chatAgentID, conversation.PeerJID, content,
	); err != nil {
		if errors.Is(err, whatsapplogin.ErrNumberNotConnected) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "phone number is not connected; reconnect it to send messages",
			})
			return
		}
		log.Printf("chat conversation API: send message: %v", err)
		writeJSON(w, http.StatusBadGateway, models.APIResponse{
			Success: false,
			Message: "WhatsApp refused the message",
		})
		return
	}

	// Sent is what matters to the person on WhatsApp; failing to record it must
	// not read as a failure to send, so the error is logged and the message is
	// still reported as delivered.
	message, err := h.repo.AppendTurn(r.Context(), conversation.ID, models.ChatRoleAssistant, content)
	if err != nil {
		log.Printf("chat conversation API: save sent message conversation_id=%s: %v", conversation.ID, err)
		message = models.ChatMessage{Role: models.ChatRoleAssistant, Content: content, CreatedAt: time.Now().UTC(), Seq: conversation.MessageCount}
	}

	writeJSON(w, http.StatusCreated, chatMessageEnvelope{
		Success: true,
		Message: "Message sent successfully",
		Data:    message,
		Links:   chatConversationLinks{Self: chatConversationsListPath + "/" + conversation.ID},
	})
}

// Delete removes one thread and its transcript.
func (h *ChatConversationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, id, ok := chatConversationRequestIdentity(w, r)
	if !ok {
		return
	}

	if err := h.repo.DeleteByUser(r.Context(), user.ID, id); err != nil {
		h.writeError(w, err, "failed to delete conversation")
		return
	}

	writeJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "Conversation deleted successfully",
		Data:    map[string]any{"id": id, "deleted": true},
	})
}

func (h *ChatConversationHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, chatconversations.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: "conversation not found",
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: fallback})
}

func chatConversationRequestIdentity(w http.ResponseWriter, r *http.Request) (models.User, string, bool) {
	user, ok := currentUser(w, r)
	if !ok {
		return models.User{}, "", false
	}
	id := strings.TrimSpace(chi.URLParam(r, "conversation_id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "conversation_id path parameter is required",
		})
		return models.User{}, "", false
	}
	return user, id, true
}

// parseChatConversationListQuery validates paging and the filters in one pass,
// so a request with two bad parameters is told about both.
func parseChatConversationListQuery(q url.Values) (int, int, chatconversations.Filter, []types.FieldError) {
	page, limit := defaultChatConversationsPage, defaultChatConversationsLimit
	var filter chatconversations.Filter
	var errs []types.FieldError

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be an integer greater than or equal to 1"})
		} else {
			page = value
		}
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxChatConversationsLimit {
			errs = append(errs, types.FieldError{
				Field:   "limit",
				Message: "limit must be an integer between 1 and " + strconv.Itoa(maxChatConversationsLimit),
			})
		} else {
			limit = value
		}
	}
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		if raw != models.ChatConversationStatusOpen && raw != models.ChatConversationStatusClosed {
			errs = append(errs, types.FieldError{Field: "status", Message: "status must be one of open, closed"})
		} else {
			filter.Status = raw
		}
	}

	filter.ChatAgentID = strings.TrimSpace(q.Get("chat_agent_id"))
	filter.PhoneNumberID = strings.TrimSpace(q.Get("phone_number_id"))
	filter.Search = strings.TrimSpace(q.Get("search"))

	return page, limit, filter, errs
}
