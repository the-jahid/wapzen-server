package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/knowledgebases"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// Default and bound values for the list query parameters, mirroring the
// documented schema for GET /v1/knowledge-base (page ≥ 1 default 1; limit 1..100
// default 20). They match the agents collection's bounds today but are named
// separately so either collection can move without dragging the other.
const (
	defaultKnowledgeBasePage  = 1
	defaultKnowledgeBaseLimit = 20
	maxKnowledgeBaseLimit     = 100
)

// knowledgeBasesListPath is the collection URL rendered into the list links.
const knowledgeBasesListPath = "/v1/knowledge-base"

// KnowledgeBaseHandler serves the Knowledge Base endpoints.
type KnowledgeBaseHandler struct {
	repo *knowledgebases.Repository
	// indexer writes sources into the vector store. It may be present but
	// disabled when the embedding or vector-store credentials are missing, which
	// only the endpoints that index anything refuse over.
	indexer *knowledgebases.Indexer
}

// NewKnowledgeBaseHandler creates a knowledge base handler with its
// dependencies.
func NewKnowledgeBaseHandler(repo *knowledgebases.Repository, indexer *knowledgebases.Indexer) *KnowledgeBaseHandler {
	return &KnowledgeBaseHandler{repo: repo, indexer: indexer}
}

type knowledgeBaseEnvelope struct {
	Success bool                 `json:"success"`
	Message string               `json:"message"`
	Data    models.KnowledgeBase `json:"data"`
	Links   knowledgeBaseLinks   `json:"links"`
}

type knowledgeBaseListEnvelope struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message"`
	Data    []models.KnowledgeBase `json:"data"`
	Meta    *types.Meta            `json:"meta,omitempty"`
	Links   types.ListLinks        `json:"links"`
}

type knowledgeBaseLinks struct {
	Self string `json:"self"`
}

type knowledgeBaseDeleteEnvelope struct {
	Success bool                    `json:"success"`
	Message string                  `json:"message"`
	Data    knowledgeBaseDeleteData `json:"data"`
}

type knowledgeBaseDeleteData struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// knowledgeBaseText is one raw text entry to index.
type knowledgeBaseText struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// knowledgeBaseFile is one file to fetch and index.
type knowledgeBaseFile struct {
	Filename string `json:"filename"`
	FileURL  string `json:"file_url"`
}

// createKnowledgeBaseRequest is the Create Knowledge Base body. Only
// KnowledgeBaseName is required; the settings are pointers so an absent field
// falls back to the column default instead of being stored as a zero value.
//
// Texts supplied here are indexed as part of the create. The url and file
// fields are still accepted by the schema so a caller sending them gets a clear
// refusal rather than a silent no-op — see validateKnowledgeBaseSources.
type createKnowledgeBaseRequest struct {
	KnowledgeBaseName  string              `json:"knowledge_base_name"`
	KnowledgeBaseTexts []knowledgeBaseText `json:"knowledge_base_texts"`
	KnowledgeBaseURLs  []string            `json:"knowledge_base_urls"`
	KnowledgeBaseFiles []knowledgeBaseFile `json:"knowledge_base_files"`
	EnableAutoRefresh  *bool               `json:"enable_auto_refresh"`
	MaxChunkSize       *int                `json:"max_chunk_size"`
	MinChunkSize       *int                `json:"min_chunk_size"`
}

// Create stores a knowledge base owned by the authenticated user, with its
// vector-store namespace assigned by the insert, and indexes any texts supplied
// with it.
//
// The sources go in as part of the create rather than being dropped for a
// follow-up call: a knowledge base created with content that was silently
// discarded is indistinguishable, to its owner, from one whose indexing failed —
// and the agent that later answers from it quotes nothing at all.
//
// It is atomic. If the sources cannot be indexed the knowledge base itself is
// removed again, so a failed create leaves nothing behind and the same request
// can simply be retried — including under the same name, which a half-created
// knowledge base would otherwise have taken.
//
// Create godoc
// @Summary      Create Knowledge Base
// @Description  Creates a knowledge base owned by the authenticated API key owner. knowledge_base_name is the only required field; every other field is optional and omitted settings fall back to their defaults. The vector-store namespace_id is assigned automatically and returned with the new knowledge base. Texts supplied in knowledge_base_texts are chunked, embedded and indexed into that namespace as part of the request, so the response carries the knowledge base already at status complete with its sources attached; a knowledge base created without texts starts empty at status in_progress. The create is all-or-nothing — if indexing fails the knowledge base is removed again and the request can be retried unchanged. knowledge_base_urls and knowledge_base_files (file_url) are rejected: urls have no fetcher yet, and documents are uploaded rather than linked, via POST /v1/knowledge-base/{knowledge_base_id}/sources as multipart/form-data.
// @Tags         Knowledge Base
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.createKnowledgeBaseRequest  true  "The knowledge base configuration to create"
// @Success      201   {object}  handlers.knowledgeBaseEnvelope
// @Failure      400   {object}  types.ErrorEnvelope
// @Failure      401   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      422   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Failure      503   {object}  models.APIResponse
// @Router       /v1/knowledge-base [post]
func (h *KnowledgeBaseHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	req, ok := decodeCreateKnowledgeBaseRequest(w, r)
	if !ok {
		return
	}

	// Refuse before the insert rather than after it: creating the knowledge base
	// and only then discovering its content cannot be indexed is the exact
	// failure this endpoint is meant to stop producing.
	if len(req.KnowledgeBaseTexts) > 0 && !h.indexer.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{
			Success: false,
			Message: "knowledge base indexing is not configured, so sources cannot be indexed",
		})
		return
	}

	knowledgeBase, err := h.repo.Create(r.Context(), models.NewKnowledgeBase{
		UserID:            user.ID,
		Name:              req.KnowledgeBaseName,
		EnableAutoRefresh: req.EnableAutoRefresh,
		MaxChunkSize:      req.MaxChunkSize,
		MinChunkSize:      req.MinChunkSize,
	})
	if err != nil {
		if errors.Is(err, knowledgebases.ErrNameTaken) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a knowledge base with this name already exists",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to create knowledge base",
		})
		return
	}

	if len(req.KnowledgeBaseTexts) > 0 {
		if !h.indexCreatedSources(w, r, user.ID, &knowledgeBase, req.KnowledgeBaseTexts) {
			return
		}
	}

	writeJSON(w, http.StatusCreated, knowledgeBaseEnvelope{
		Success: true,
		Message: "Knowledge base created successfully",
		Data:    knowledgeBase,
		Links:   knowledgeBaseLinks{Self: knowledgeBasesListPath + "/" + knowledgeBase.ID},
	})
}

// indexCreatedSources stores and indexes the texts a create request carried,
// updating knowledgeBase in place with the finished row. It writes the failure
// response itself and returns false when the create must not be reported as a
// success.
//
// Every failure path takes the new knowledge base back out, which is what makes
// Create all-or-nothing. That differs from AddSources, which leaves an existing
// knowledge base in the error status: there, the knowledge base predates the
// request and is not the request's to delete.
func (h *KnowledgeBaseHandler) indexCreatedSources(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	knowledgeBase *models.KnowledgeBase,
	texts []knowledgeBaseText,
) bool {
	if knowledgeBase.NamespaceID == nil || *knowledgeBase.NamespaceID == "" {
		log.Printf("knowledge base %s: created without a vector-store namespace", knowledgeBase.ID)
		h.discardKnowledgeBase(r.Context(), userID, *knowledgeBase)
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "knowledge base has no vector-store namespace",
		})
		return false
	}

	entries := make([]models.NewKnowledgeBaseSource, 0, len(texts))
	for _, text := range texts {
		entries = append(entries, models.NewKnowledgeBaseSource{
			Type:  models.KnowledgeBaseSourceTypeText,
			Title: text.Title,
			Text:  text.Text,
		})
	}

	sources, err := h.repo.InsertSources(r.Context(), knowledgeBase.ID, entries)
	if err != nil {
		log.Printf("knowledge base %s: storing sources during create failed: %v", knowledgeBase.ID, err)
		h.discardKnowledgeBase(r.Context(), userID, *knowledgeBase)
		// Two texts in one request sharing a title is the caller's to fix; the
		// unique index is what catches it.
		if errors.Is(err, knowledgebases.ErrSourceTitleTaken) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "two sources in this request share a title; titles must be unique within a knowledge base",
			})
			return false
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to store knowledge base sources",
		})
		return false
	}

	if err := h.indexer.IndexSources(r.Context(), *knowledgeBase, sources); err != nil {
		log.Printf("knowledge base %s: indexing sources during create failed: %v", knowledgeBase.ID, err)
		h.discardKnowledgeBase(r.Context(), userID, *knowledgeBase)
		writeJSON(w, http.StatusUnprocessableEntity, models.APIResponse{
			Success: false,
			Message: "failed to index knowledge base sources; the knowledge base was not created",
		})
		return false
	}

	h.setKnowledgeBaseStatus(r.Context(), userID, knowledgeBase.ID, models.KnowledgeBaseStatusComplete)

	// Re-read so the response reports the stored row — the status just written,
	// and each source with the chunk count indexing gave it.
	updated, err := h.repo.GetByUser(r.Context(), userID, knowledgeBase.ID)
	if err != nil {
		log.Printf("knowledge base %s: reloading after create indexing failed: %v", knowledgeBase.ID, err)
		// The content is indexed and the knowledge base is sound, so this is not a
		// failed create — only a response that cannot carry the finished row.
		knowledgeBase.Status = models.KnowledgeBaseStatusComplete
		return true
	}
	h.attachSources(r.Context(), &updated)
	*knowledgeBase = updated
	return true
}

// discardKnowledgeBase removes a knowledge base whose create failed, together
// with anything already written into its namespace. Both steps are best effort:
// the request has already failed, and a cleanup failure must not replace the
// error the caller needs to see.
//
// The whole namespace is purged rather than the individual chunk ids because
// the knowledge base is brand new — everything in it was written by this
// request, so there is nothing else there to preserve.
func (h *KnowledgeBaseHandler) discardKnowledgeBase(ctx context.Context, userID string, knowledgeBase models.KnowledgeBase) {
	if h.indexer.Enabled() {
		if err := h.indexer.PurgeNamespace(ctx, knowledgeBase); err != nil {
			log.Printf("knowledge base %s: purging the namespace of a failed create failed: %v", knowledgeBase.ID, err)
		}
	}
	if err := h.repo.DeleteByUser(ctx, userID, knowledgeBase.ID); err != nil {
		log.Printf("knowledge base %s: removing it after a failed create failed: %v", knowledgeBase.ID, err)
	}
}

// List returns a page of the authenticated user's knowledge bases, newest first.
//
// List godoc
// @Summary      List Knowledge Bases
// @Description  Returns a paginated list of the knowledge bases owned by the authenticated API key owner, newest first, each with its indexed sources.
// @Tags         Knowledge Base
// @Produce      json
// @Security     BearerAuth
// @Param        page   query     int  false  "Page number (1-based)"
// @Param        limit  query     int  false  "Items per page (1-100)"
// @Success      200    {object}  handlers.knowledgeBaseListEnvelope
// @Failure      400    {object}  types.ErrorEnvelope
// @Failure      401    {object}  models.APIResponse
// @Failure      500    {object}  models.APIResponse
// @Router       /v1/knowledge-base [get]
func (h *KnowledgeBaseHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	page, limit, fieldErrs := parseKnowledgeBaseListQuery(r.URL.Query())
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
			Message: "failed to list knowledge bases",
		})
		return
	}

	h.attachSourcesToList(r.Context(), list)
	meta := buildPaginationMeta(page, limit, total)

	writeJSON(w, http.StatusOK, knowledgeBaseListEnvelope{
		Success: true,
		Message: "Knowledge bases retrieved successfully",
		Data:    list,
		Meta:    &meta,
		Links:   buildListLinks(knowledgeBasesListPath, page, limit, total, nil),
	})
}

// parseKnowledgeBaseListQuery reads and validates the page/limit query
// parameters, returning the effective values plus any per-field validation
// errors. Absent parameters fall back to their documented defaults.
func parseKnowledgeBaseListQuery(q url.Values) (page, limit int, errs []types.FieldError) {
	page, limit = defaultKnowledgeBasePage, defaultKnowledgeBaseLimit

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err != nil || p < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			page = p
		}
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if l, err := strconv.Atoi(raw); err != nil || l < 1 || l > maxKnowledgeBaseLimit {
			errs = append(errs, types.FieldError{Field: "limit", Message: fmt.Sprintf("limit must be between 1 and %d", maxKnowledgeBaseLimit)})
		} else {
			limit = l
		}
	}

	return page, limit, errs
}

// Get returns a single knowledge base owned by the authenticated user.
//
// Get godoc
// @Summary      Get Knowledge Base
// @Description  Returns a single knowledge base by id, scoped to the authenticated API key owner, including its indexed sources.
// @Tags         Knowledge Base
// @Produce      json
// @Security     BearerAuth
// @Param        knowledge_base_id  path      string  true  "Knowledge base id"
// @Success      200                {object}  handlers.knowledgeBaseEnvelope
// @Failure      400                {object}  models.APIResponse
// @Failure      401                {object}  models.APIResponse
// @Failure      404                {object}  models.APIResponse
// @Failure      500                {object}  models.APIResponse
// @Router       /v1/knowledge-base/{knowledge_base_id} [get]
func (h *KnowledgeBaseHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	knowledgeBaseID, ok := knowledgeBaseIDParam(w, r)
	if !ok {
		return
	}

	knowledgeBase, err := h.repo.GetByUser(r.Context(), user.ID, knowledgeBaseID)
	if err != nil {
		if errors.Is(err, knowledgebases.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "knowledge base not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to get knowledge base",
		})
		return
	}
	h.attachSources(r.Context(), &knowledgeBase)

	writeJSON(w, http.StatusOK, knowledgeBaseEnvelope{
		Success: true,
		Message: "Knowledge base retrieved successfully",
		Data:    knowledgeBase,
		Links:   knowledgeBaseLinks{Self: knowledgeBasesListPath + "/" + knowledgeBase.ID},
	})
}

// updateKnowledgeBaseRequest is the partial-update body. Every field is an
// optional pointer so an absent one leaves the stored value untouched; at least
// one must be supplied.
type updateKnowledgeBaseRequest struct {
	KnowledgeBaseName *string `json:"knowledge_base_name"`
	EnableAutoRefresh *bool   `json:"enable_auto_refresh"`
	MaxChunkSize      *int    `json:"max_chunk_size"`
	MinChunkSize      *int    `json:"min_chunk_size"`
}

// Update changes the name and indexing configuration of a knowledge base owned
// by the authenticated user.
//
// The chunk sizes must stay consistent with each other, and the body may carry
// only one of them, so the stored row is loaded first and the supplied values
// are checked against it before the update runs.
//
// Update godoc
// @Summary      Update Knowledge Base
// @Description  Applies a partial update to a knowledge base owned by the authenticated API key owner. Only knowledge_base_name and the indexing settings (enable_auto_refresh, max_chunk_size, min_chunk_size) may be changed; at least one must be supplied, and omitted fields keep their stored value. Chunk sizes are validated against each other using the stored values, so min_chunk_size may never end up above max_chunk_size. Changing the chunk sizes does not re-index existing sources.
// @Tags         Knowledge Base
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        knowledge_base_id  path      string                              true  "Knowledge base id"
// @Param        body               body      handlers.updateKnowledgeBaseRequest  true  "Fields to change"
// @Success      200                {object}  handlers.knowledgeBaseEnvelope
// @Failure      400                {object}  types.ErrorEnvelope
// @Failure      401                {object}  models.APIResponse
// @Failure      404                {object}  models.APIResponse
// @Failure      409                {object}  models.APIResponse
// @Failure      500                {object}  models.APIResponse
// @Router       /v1/knowledge-base/{knowledge_base_id} [patch]
func (h *KnowledgeBaseHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	knowledgeBaseID, ok := knowledgeBaseIDParam(w, r)
	if !ok {
		return
	}

	req, ok := decodeUpdateKnowledgeBaseRequest(w, r)
	if !ok {
		return
	}

	// The stored row supplies whichever chunk size the body left out, and doubles
	// as the ownership check, so a knowledge base that is not the caller's reads
	// as missing before anything is written.
	current, err := h.repo.GetByUser(r.Context(), user.ID, knowledgeBaseID)
	if err != nil {
		if errors.Is(err, knowledgebases.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "knowledge base not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to load knowledge base",
		})
		return
	}

	maxChunkSize, minChunkSize := current.MaxChunkSize, current.MinChunkSize
	if req.MaxChunkSize != nil {
		maxChunkSize = *req.MaxChunkSize
	}
	if req.MinChunkSize != nil {
		minChunkSize = *req.MinChunkSize
	}
	if minChunkSize > maxChunkSize {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid request body",
			Errors: []types.FieldError{{
				Field:   "min_chunk_size",
				Message: "min_chunk_size must not be greater than max_chunk_size",
			}},
		})
		return
	}

	knowledgeBase, err := h.repo.UpdateByUser(r.Context(), user.ID, knowledgeBaseID, models.KnowledgeBaseUpdate{
		Name:              req.KnowledgeBaseName,
		EnableAutoRefresh: req.EnableAutoRefresh,
		MaxChunkSize:      req.MaxChunkSize,
		MinChunkSize:      req.MinChunkSize,
	})
	if err != nil {
		switch {
		case errors.Is(err, knowledgebases.ErrNotFound):
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "knowledge base not found",
			})
		case errors.Is(err, knowledgebases.ErrNameTaken):
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a knowledge base with this name already exists",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Message: "failed to update knowledge base",
			})
		}
		return
	}
	h.attachSources(r.Context(), &knowledgeBase)

	writeJSON(w, http.StatusOK, knowledgeBaseEnvelope{
		Success: true,
		Message: "Knowledge base updated successfully",
		Data:    knowledgeBase,
		Links:   knowledgeBaseLinks{Self: knowledgeBasesListPath + "/" + knowledgeBase.ID},
	})
}

// decodeUpdateKnowledgeBaseRequest reads and validates the Update Knowledge Base
// body, writing the 400 itself and returning false when it is unusable. The
// cross-field chunk-size comparison is left to the caller, which has the stored
// row the body may be updating only half of.
func decodeUpdateKnowledgeBaseRequest(w http.ResponseWriter, r *http.Request) (updateKnowledgeBaseRequest, bool) {
	var req updateKnowledgeBaseRequest

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

	if req.KnowledgeBaseName != nil {
		trimmed := strings.TrimSpace(*req.KnowledgeBaseName)
		req.KnowledgeBaseName = &trimmed
	}

	fieldErrs := validateUpdateKnowledgeBaseRequest(req)
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

// validateUpdateKnowledgeBaseRequest checks each supplied field against the
// bounds the knowledge_bases CHECK constraints enforce, and rejects a body that
// would change nothing.
func validateUpdateKnowledgeBaseRequest(req updateKnowledgeBaseRequest) []types.FieldError {
	var errs []types.FieldError

	if req.KnowledgeBaseName == nil && req.EnableAutoRefresh == nil &&
		req.MaxChunkSize == nil && req.MinChunkSize == nil {
		return []types.FieldError{{
			Field:   "body",
			Message: "supply at least one of knowledge_base_name, enable_auto_refresh, max_chunk_size or min_chunk_size",
		}}
	}

	if req.KnowledgeBaseName != nil {
		switch {
		case *req.KnowledgeBaseName == "":
			errs = append(errs, types.FieldError{
				Field:   "knowledge_base_name",
				Message: "knowledge_base_name must not be empty",
			})
		case len(*req.KnowledgeBaseName) > models.KnowledgeBaseMaxNameLength:
			errs = append(errs, types.FieldError{
				Field:   "knowledge_base_name",
				Message: fmt.Sprintf("knowledge_base_name must be at most %d characters", models.KnowledgeBaseMaxNameLength),
			})
		}
	}

	if req.MaxChunkSize != nil && (*req.MaxChunkSize < models.KnowledgeBaseMinMaxChunkSize || *req.MaxChunkSize > models.KnowledgeBaseMaxMaxChunkSize) {
		errs = append(errs, types.FieldError{
			Field:   "max_chunk_size",
			Message: fmt.Sprintf("max_chunk_size must be between %d and %d", models.KnowledgeBaseMinMaxChunkSize, models.KnowledgeBaseMaxMaxChunkSize),
		})
	}
	if req.MinChunkSize != nil && (*req.MinChunkSize < models.KnowledgeBaseMinMinChunkSize || *req.MinChunkSize > models.KnowledgeBaseMaxMinChunkSize) {
		errs = append(errs, types.FieldError{
			Field:   "min_chunk_size",
			Message: fmt.Sprintf("min_chunk_size must be between %d and %d", models.KnowledgeBaseMinMinChunkSize, models.KnowledgeBaseMaxMinChunkSize),
		})
	}

	return errs
}

// Delete removes a knowledge base owned by the authenticated user, together
// with its sources.
//
// The source rows cascade with the container row, but the vector store has no
// such link, so its namespace is purged explicitly. The purge runs after the
// row is gone: an orphaned namespace is recoverable, whereas vectors deleted
// under a knowledge base that then failed to delete are not.
//
// Delete godoc
// @Summary      Delete Knowledge Base
// @Description  Deletes a knowledge base by id, scoped to the authenticated API key owner, together with its sources and every vector indexed under its namespace. Deleting one that does not exist, or that belongs to somebody else, returns 404.
// @Tags         Knowledge Base
// @Produce      json
// @Security     BearerAuth
// @Param        knowledge_base_id  path      string  true  "Knowledge base id"
// @Success      200                {object}  handlers.knowledgeBaseDeleteEnvelope
// @Failure      400                {object}  models.APIResponse
// @Failure      401                {object}  models.APIResponse
// @Failure      404                {object}  models.APIResponse
// @Failure      500                {object}  models.APIResponse
// @Router       /v1/knowledge-base/{knowledge_base_id} [delete]
func (h *KnowledgeBaseHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	knowledgeBaseID, ok := knowledgeBaseIDParam(w, r)
	if !ok {
		return
	}

	// Read first so the namespace is known once the row is gone. A knowledge base
	// that cannot be loaded is left to the delete below to report.
	knowledgeBase, loadErr := h.repo.GetByUser(r.Context(), user.ID, knowledgeBaseID)

	if err := h.repo.DeleteByUser(r.Context(), user.ID, knowledgeBaseID); err != nil {
		if errors.Is(err, knowledgebases.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "knowledge base not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to delete knowledge base",
		})
		return
	}

	// Best effort: the knowledge base is already gone, so a namespace that could
	// not be purged is logged rather than reported as a failed delete.
	if loadErr == nil && h.indexer.Enabled() {
		if err := h.indexer.PurgeNamespace(r.Context(), knowledgeBase); err != nil {
			log.Printf("knowledge base %s: purging vector namespace failed: %v", knowledgeBaseID, err)
		}
	}

	writeJSON(w, http.StatusOK, knowledgeBaseDeleteEnvelope{
		Success: true,
		Message: "Knowledge base deleted successfully",
		Data:    knowledgeBaseDeleteData{ID: knowledgeBaseID, Deleted: true},
	})
}

// knowledgeBaseIDParam reads and validates the {knowledge_base_id} path
// parameter, writing a 400 and returning false when it is missing.
func knowledgeBaseIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "knowledge_base_id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "knowledge_base_id path parameter is required",
		})
		return "", false
	}
	return id, true
}

// decodeCreateKnowledgeBaseRequest reads and validates the Create Knowledge
// Base body, writing the 400 itself and returning false when it is unusable.
func decodeCreateKnowledgeBaseRequest(w http.ResponseWriter, r *http.Request) (createKnowledgeBaseRequest, bool) {
	var req createKnowledgeBaseRequest

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

	req.KnowledgeBaseName = strings.TrimSpace(req.KnowledgeBaseName)

	fieldErrs := validateCreateKnowledgeBaseRequest(&req)
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

// validateCreateKnowledgeBaseRequest checks the create body against the same
// bounds the knowledge_bases CHECK constraints enforce, so a bad value reads as
// a field error rather than a 500 from the insert. It trims the source fields
// in place as it goes.
func validateCreateKnowledgeBaseRequest(req *createKnowledgeBaseRequest) []types.FieldError {
	var errs []types.FieldError

	switch {
	case req.KnowledgeBaseName == "":
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_name",
			Message: "knowledge_base_name is required",
		})
	case len(req.KnowledgeBaseName) > models.KnowledgeBaseMaxNameLength:
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_name",
			Message: fmt.Sprintf("knowledge_base_name must be at most %d characters", models.KnowledgeBaseMaxNameLength),
		})
	}

	if req.MaxChunkSize != nil && (*req.MaxChunkSize < models.KnowledgeBaseMinMaxChunkSize || *req.MaxChunkSize > models.KnowledgeBaseMaxMaxChunkSize) {
		errs = append(errs, types.FieldError{
			Field:   "max_chunk_size",
			Message: fmt.Sprintf("max_chunk_size must be between %d and %d", models.KnowledgeBaseMinMaxChunkSize, models.KnowledgeBaseMaxMaxChunkSize),
		})
	}
	if req.MinChunkSize != nil && (*req.MinChunkSize < models.KnowledgeBaseMinMinChunkSize || *req.MinChunkSize > models.KnowledgeBaseMaxMinChunkSize) {
		errs = append(errs, types.FieldError{
			Field:   "min_chunk_size",
			Message: fmt.Sprintf("min_chunk_size must be between %d and %d", models.KnowledgeBaseMinMinChunkSize, models.KnowledgeBaseMaxMinChunkSize),
		})
	}
	// Only compare the two once both are individually in range, so an
	// out-of-range value reports its own bound rather than this ordering error.
	if len(errs) == 0 {
		if max, min := effectiveMaxChunkSize(req), effectiveMinChunkSize(req); min > max {
			errs = append(errs, types.FieldError{
				Field:   "min_chunk_size",
				Message: "min_chunk_size must not be greater than max_chunk_size",
			})
		}
	}

	errs = append(errs, validateKnowledgeBaseSources(req)...)
	return errs
}

// validateKnowledgeBaseSources checks the supplied sources against the same
// rules Add Sources applies, because Create now indexes them through the same
// pipeline: a title becomes a vector store record id verbatim, so it has to be
// usable as one before anything is written.
//
// Urls and file_url references are refused rather than validated and dropped.
// There is no fetcher for either, so accepting them would go back to promising
// content that never gets indexed — the behaviour this endpoint just stopped.
func validateKnowledgeBaseSources(req *createKnowledgeBaseRequest) []types.FieldError {
	var errs []types.FieldError

	if len(req.KnowledgeBaseTexts) > models.KnowledgeBaseMaxTextsPerRequest {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_texts",
			Message: fmt.Sprintf("at most %d text entries may be supplied", models.KnowledgeBaseMaxTextsPerRequest),
		})
	}

	// Titles must be unique within a knowledge base — the vector ids are built
	// from them — so a request that repeats one is rejected here rather than by
	// the unique index after the knowledge base row already exists.
	seenTitles := make(map[string]int, len(req.KnowledgeBaseTexts))

	for i := range req.KnowledgeBaseTexts {
		text := &req.KnowledgeBaseTexts[i]
		text.Title = strings.TrimSpace(text.Title)
		text.Text = strings.TrimSpace(text.Text)

		switch {
		case text.Title == "" || text.Text == "":
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("knowledge_base_texts[%d]", i),
				Message: "title and text are both required",
			})
		case len([]rune(text.Title)) > models.KnowledgeBaseMaxSourceTitleLength:
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("knowledge_base_texts[%d].title", i),
				Message: fmt.Sprintf("title must be at most %d characters", models.KnowledgeBaseMaxSourceTitleLength),
			})
		case !isVectorIDSafe(text.Title):
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("knowledge_base_texts[%d].title", i),
				Message: fmt.Sprintf("title is used verbatim as the vector store record id, so it must be printable ASCII and must not contain %q", knowledgebases.ChunkIDSeparator),
			})
		default:
			if first, repeated := seenTitles[text.Title]; repeated {
				errs = append(errs, types.FieldError{
					Field:   fmt.Sprintf("knowledge_base_texts[%d].title", i),
					Message: fmt.Sprintf("duplicate title; it is already used by knowledge_base_texts[%d]", first),
				})
			} else {
				seenTitles[text.Title] = i
			}
		}
	}

	if len(req.KnowledgeBaseURLs) > 0 {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_urls",
			Message: "url sources are not supported yet; supply knowledge_base_texts",
		})
	}
	if len(req.KnowledgeBaseFiles) > 0 {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_files",
			Message: fmt.Sprintf("files are uploaded, not linked; create the knowledge base first, then send the documents as multipart/form-data %q parts to its sources endpoint", models.KnowledgeBaseUploadFilesFormField),
		})
	}

	return errs
}

// effectiveMaxChunkSize / effectiveMinChunkSize resolve the value the insert
// will end up with: the supplied one, or the column default when absent.
func effectiveMaxChunkSize(req *createKnowledgeBaseRequest) int {
	if req.MaxChunkSize != nil {
		return *req.MaxChunkSize
	}
	return models.KnowledgeBaseDefaultMaxChunkSize
}

func effectiveMinChunkSize(req *createKnowledgeBaseRequest) int {
	if req.MinChunkSize != nil {
		return *req.MinChunkSize
	}
	return models.KnowledgeBaseDefaultMinChunkSize
}
