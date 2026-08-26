package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/knowledgebases"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// addKnowledgeBaseSourcesRequest is the Add Knowledge Base Sources JSON body.
// The url and file fields are accepted so a caller sending them gets a clear
// answer: urls have no fetcher yet, and a file is uploaded as multipart rather
// than referenced by url, so both are errors rather than silent no-ops.
type addKnowledgeBaseSourcesRequest struct {
	KnowledgeBaseTexts []knowledgeBaseText `json:"knowledge_base_texts"`
	KnowledgeBaseURLs  []string            `json:"knowledge_base_urls"`
	KnowledgeBaseFiles []knowledgeBaseFile `json:"knowledge_base_files"`
}

// AddSources indexes new sources into an existing knowledge base, supplied
// either as raw texts in a JSON body or as uploaded files in a multipart one.
//
// A file is not treated as a different kind of source, only as a different way
// to deliver one: the server reads the upload, extracts its text, and from there
// the two paths are the same code. Extraction is deliberately the server's job
// and not the client's — the indexed text is then the text this server read,
// rather than whatever a client claimed the document said.
//
// The work runs inside the request: each source is stored, chunked with the
// knowledge base's own chunk sizes, embedded, and upserted into the namespace
// assigned when the knowledge base was created. The response therefore carries
// the finished result — status complete and the new sources with their chunk
// counts — rather than an in_progress row the caller has to poll.
//
// It is all-or-nothing. If any part of indexing fails, the source rows written
// for this request are deleted and their vectors purged, so a retry with the
// same body cannot leave a duplicate or half-indexed source behind; the
// knowledge base is left in the error status.
//
// AddSources godoc
// @Summary      Add Knowledge Base Sources
// @Description  Adds sources to an existing knowledge base owned by the authenticated API key owner and indexes them into its vector-store namespace. Sources arrive one of two ways. A JSON body carries raw texts in knowledge_base_texts. A multipart/form-data body uploads documents in repeated "files" parts, optionally named by repeated "titles" parts (the nth title belongs to the nth file, and a blank or missing one falls back to the filename); the server extracts the text from each upload — .txt, .md, .csv, .tsv, .json, .yaml, .xml, .log, .rst, .html, .docx and .pdf are read — so nothing is parsed client-side. Either way the resulting text is chunked using the knowledge base's own max_chunk_size and min_chunk_size, embedded, and written under its namespace_id with the source title as the record id; a source that splits into several chunks stores the first under the title and the rest under "<title>#2", "<title>#3" and so on. Titles must be printable ASCII, must not contain '#', and must be unique within the knowledge base. Indexing runs synchronously, so the knowledge base is returned already at status complete with the new sources attached. knowledge_base_urls and the JSON knowledge_base_files (file_url) form are still rejected: urls have no fetcher yet, and a file is uploaded rather than linked. The request is all-or-nothing — a failure to index leaves the knowledge base exactly as it was, at status error.
// @Tags         Knowledge Base
// @Accept       json
// @Accept       multipart/form-data
// @Produce      json
// @Security     BearerAuth
// @Param        knowledge_base_id  path      string                                   true   "Knowledge base id"
// @Param        body               body      handlers.addKnowledgeBaseSourcesRequest  false  "The raw texts to add (application/json)"
// @Param        files              formData  file                                     false  "Documents to extract and index (multipart/form-data; repeat the field for several files)"
// @Param        titles             formData  string                                   false  "Optional source titles, one per file in the same order (multipart/form-data)"
// @Success      201                {object}  handlers.knowledgeBaseEnvelope
// @Failure      400                {object}  types.ErrorEnvelope
// @Failure      401                {object}  models.APIResponse
// @Failure      404                {object}  models.APIResponse
// @Failure      409                {object}  models.APIResponse
// @Failure      413                {object}  models.APIResponse
// @Failure      422                {object}  models.APIResponse
// @Failure      500                {object}  models.APIResponse
// @Failure      503                {object}  models.APIResponse
// @Router       /v1/knowledge-base/{knowledge_base_id}/sources [post]
func (h *KnowledgeBaseHandler) AddSources(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	knowledgeBaseID, ok := knowledgeBaseIDParam(w, r)
	if !ok {
		return
	}

	// Refuse before reading the body: storing sources that cannot be indexed
	// would leave the knowledge base claiming content the vector store does not
	// have, and an upload should not be read at all if nothing can be done with
	// it.
	if !h.indexer.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{
			Success: false,
			Message: "knowledge base indexing is not configured",
		})
		return
	}

	// Doubles as the ownership check, and supplies the chunk sizes and the
	// namespace the vectors are written under.
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
			Message: "failed to load knowledge base",
		})
		return
	}
	if knowledgeBase.NamespaceID == nil || *knowledgeBase.NamespaceID == "" {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "knowledge base has no vector-store namespace",
		})
		return
	}

	entries, ok := readAddSourcesEntries(w, r)
	if !ok {
		return
	}

	// The status is moved first so a client polling mid-request sees indexing in
	// progress rather than a knowledge base that looks finished.
	h.setKnowledgeBaseStatus(r.Context(), user.ID, knowledgeBase.ID, models.KnowledgeBaseStatusInProgress)

	sources, err := h.repo.InsertSources(r.Context(), knowledgeBase.ID, entries)
	if err != nil {
		log.Printf("knowledge base %s: storing sources failed: %v", knowledgeBase.ID, err)
		// A duplicate title is the caller's to fix, and nothing was written, so
		// the knowledge base keeps the status it had rather than going to error.
		if errors.Is(err, knowledgebases.ErrSourceTitleTaken) {
			h.setKnowledgeBaseStatus(r.Context(), user.ID, knowledgeBase.ID, knowledgeBase.Status)
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a source with this title already exists in this knowledge base",
			})
			return
		}
		h.setKnowledgeBaseStatus(r.Context(), user.ID, knowledgeBase.ID, models.KnowledgeBaseStatusError)
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to store knowledge base sources",
		})
		return
	}

	if err := h.indexer.IndexSources(r.Context(), knowledgeBase, sources); err != nil {
		log.Printf("knowledge base %s: indexing sources failed: %v", knowledgeBase.ID, err)
		h.rollbackSources(r.Context(), knowledgeBase, sources)
		h.setKnowledgeBaseStatus(r.Context(), user.ID, knowledgeBase.ID, models.KnowledgeBaseStatusError)
		writeJSON(w, http.StatusUnprocessableEntity, models.APIResponse{
			Success: false,
			Message: "failed to index knowledge base sources",
		})
		return
	}

	h.setKnowledgeBaseStatus(r.Context(), user.ID, knowledgeBase.ID, models.KnowledgeBaseStatusComplete)

	// Re-read so the response reports the stored row rather than the in-memory
	// one, including the status just written and every source, not only the new
	// ones.
	updated, err := h.repo.GetByUser(r.Context(), user.ID, knowledgeBase.ID)
	if err != nil {
		log.Printf("knowledge base %s: reloading after indexing failed: %v", knowledgeBase.ID, err)
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "sources were indexed but the knowledge base could not be reloaded",
		})
		return
	}
	h.attachSources(r.Context(), &updated)

	writeJSON(w, http.StatusCreated, knowledgeBaseEnvelope{
		Success: true,
		Message: "Knowledge base sources added successfully",
		Data:    updated,
		Links:   knowledgeBaseLinks{Self: knowledgeBasesListPath + "/" + updated.ID},
	})
}

// knowledgeBaseSourceDeleteEnvelope is the Delete Knowledge Base Source
// response. It names the knowledge base as well as the source, since a source id
// alone does not say what it was removed from.
type knowledgeBaseSourceDeleteEnvelope struct {
	Success bool                          `json:"success"`
	Message string                        `json:"message"`
	Data    knowledgeBaseSourceDeleteData `json:"data"`
}

type knowledgeBaseSourceDeleteData struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Deleted         bool   `json:"deleted"`
}

// DeleteSource removes one source from a knowledge base, together with the
// vectors it produced.
//
// The vectors go first and the row second, which is the opposite order from
// deleting a whole knowledge base. It is the order that fails safely here: the
// row is what the vector ids are rebuilt from, so dropping it first and then
// failing to delete the vectors would leave chunks that no request can ever
// address again — and that agents would go on quoting from a source the caller
// was told had been deleted. This way a failure leaves the source listed and
// intact, and the same request can simply be retried.
//
// DeleteSource godoc
// @Summary      Delete Knowledge Base Source
// @Description  Removes a single source from a knowledge base owned by the authenticated API key owner, together with every vector it wrote into the knowledge base's namespace. The knowledge base itself and its other sources are left untouched, and its chunking configuration is not changed. The vectors are removed before the source row, so a failure to reach the vector store leaves the source in place to be retried rather than orphaning chunks that agents would keep retrieving. Deleting a source that does not exist, or one belonging to another knowledge base, returns 404.
// @Tags         Knowledge Base
// @Produce      json
// @Security     BearerAuth
// @Param        knowledge_base_id  path      string  true  "Knowledge base id"
// @Param        source_id          path      string  true  "Knowledge base source id"
// @Success      200                {object}  handlers.knowledgeBaseSourceDeleteEnvelope
// @Failure      400                {object}  models.APIResponse
// @Failure      401                {object}  models.APIResponse
// @Failure      404                {object}  models.APIResponse
// @Failure      500                {object}  models.APIResponse
// @Failure      502                {object}  models.APIResponse
// @Failure      503                {object}  models.APIResponse
// @Router       /v1/knowledge-base/{knowledge_base_id}/sources/{source_id} [delete]
func (h *KnowledgeBaseHandler) DeleteSource(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	knowledgeBaseID, ok := knowledgeBaseIDParam(w, r)
	if !ok {
		return
	}

	sourceID, ok := sourceIDParam(w, r)
	if !ok {
		return
	}

	// Doubles as the ownership check, and supplies the namespace the source's
	// vectors live in.
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
			Message: "failed to load knowledge base",
		})
		return
	}

	// The row carries the title and chunk count the vector ids are rebuilt from,
	// so it is read before anything is deleted.
	source, err := h.repo.GetSource(r.Context(), knowledgeBase.ID, sourceID)
	if err != nil {
		if errors.Is(err, knowledgebases.ErrSourceNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "knowledge base source not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to load knowledge base source",
		})
		return
	}

	if err := h.indexer.DeleteSourceVectors(r.Context(), knowledgeBase, source); err != nil {
		log.Printf("knowledge base %s: deleting vectors of source %s failed: %v", knowledgeBase.ID, sourceID, err)
		if errors.Is(err, knowledgebases.ErrIndexingNotConfigured) {
			writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{
				Success: false,
				Message: "knowledge base indexing is not configured, so this source's vectors cannot be removed",
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, models.APIResponse{
			Success: false,
			Message: "failed to remove the source's vectors; the source was left in place",
		})
		return
	}

	if err := h.repo.DeleteSource(r.Context(), knowledgeBase.ID, sourceID); err != nil {
		if errors.Is(err, knowledgebases.ErrSourceNotFound) {
			writeJSON(w, http.StatusNotFound, models.APIResponse{
				Success: false,
				Message: "knowledge base source not found",
			})
			return
		}
		// The vectors are already gone, so the source is left indexing nothing.
		// Reporting the failure is what lets the caller retry and clear the row.
		log.Printf("knowledge base %s: deleting source %s failed after its vectors were removed: %v", knowledgeBase.ID, sourceID, err)
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to delete knowledge base source",
		})
		return
	}

	writeJSON(w, http.StatusOK, knowledgeBaseSourceDeleteEnvelope{
		Success: true,
		Message: "Knowledge base source deleted successfully",
		Data: knowledgeBaseSourceDeleteData{
			ID:              sourceID,
			KnowledgeBaseID: knowledgeBase.ID,
			Deleted:         true,
		},
	})
}

// sourceIDParam reads and validates the {source_id} path parameter, writing a
// 400 and returning false when it is missing.
func sourceIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "source_id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "source_id path parameter is required",
		})
		return "", false
	}
	return id, true
}

// rollbackSources undoes a failed add: the vectors of the new sources are
// purged and their rows dropped, so the knowledge base is left holding exactly
// what it held before the request. Both steps are best effort — the request has
// already failed, and a rollback failure must not replace the real error.
func (h *KnowledgeBaseHandler) rollbackSources(ctx context.Context, knowledgeBase models.KnowledgeBase, sources []models.KnowledgeBaseSourceWithContent) {
	if len(sources) == 0 {
		return
	}
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.SourceID)
	}
	if err := h.repo.DeleteSources(ctx, ids); err != nil {
		log.Printf("knowledge base %s: rolling back %d source rows failed: %v", knowledgeBase.ID, len(ids), err)
	}
}

// setKnowledgeBaseStatus records an indexing status transition. A failure to
// write it is logged rather than returned: the status is reporting, and losing
// it must not fail a request whose actual work succeeded.
func (h *KnowledgeBaseHandler) setKnowledgeBaseStatus(ctx context.Context, userID, knowledgeBaseID, status string) {
	if err := h.repo.SetStatus(ctx, userID, knowledgeBaseID, status); err != nil {
		log.Printf("knowledge base %s: setting status %q failed: %v", knowledgeBaseID, status, err)
	}
}

// attachSources fills in a knowledge base's indexed sources. A failure is
// logged and leaves the field absent rather than failing the response, which
// keeps a sources read from breaking the knowledge base endpoints.
func (h *KnowledgeBaseHandler) attachSources(ctx context.Context, knowledgeBase *models.KnowledgeBase) {
	sources, err := h.repo.ListSources(ctx, knowledgeBase.ID)
	if err != nil {
		log.Printf("knowledge base %s: loading sources failed: %v", knowledgeBase.ID, err)
		return
	}
	knowledgeBase.Sources = sources
}

// attachSourcesToList fills in the sources of a page of knowledge bases with a
// single query, so listing does not fan out into one read per row.
func (h *KnowledgeBaseHandler) attachSourcesToList(ctx context.Context, knowledgeBases []models.KnowledgeBase) {
	if len(knowledgeBases) == 0 {
		return
	}
	ids := make([]string, 0, len(knowledgeBases))
	for _, knowledgeBase := range knowledgeBases {
		ids = append(ids, knowledgeBase.ID)
	}
	byKnowledgeBase, err := h.repo.SourcesByKnowledgeBase(ctx, ids)
	if err != nil {
		log.Printf("loading sources for %d knowledge bases failed: %v", len(ids), err)
		return
	}
	for i := range knowledgeBases {
		knowledgeBases[i].Sources = byKnowledgeBase[knowledgeBases[i].ID]
	}
}

// Multipart bounds. The body limit is the per-file cap times the file limit
// plus slack for the part headers and boundaries, so a request that would be
// rejected file by file anyway is stopped at the socket instead of being
// buffered first. memoryBytes is only where the parser stops using RAM and
// starts using temp files — it is not a limit on the upload.
const (
	uploadMultipartMemoryBytes = 8 << 20
	uploadMultipartSlackBytes  = 1 << 20
)

// readAddSourcesEntries reads the sources to index out of the request, whichever
// way they were supplied: raw texts in a JSON body, or uploaded documents in a
// multipart one. Both produce the same entries, which is what lets one indexing
// path serve both. It writes the failure response itself and returns false when
// the request is unusable.
func readAddSourcesEntries(w http.ResponseWriter, r *http.Request) ([]models.NewKnowledgeBaseSource, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mediaType == "multipart/form-data" {
		return decodeUploadedSources(w, r)
	}

	req, ok := decodeAddKnowledgeBaseSourcesRequest(w, r)
	if !ok {
		return nil, false
	}

	entries := make([]models.NewKnowledgeBaseSource, 0, len(req.KnowledgeBaseTexts))
	for _, text := range req.KnowledgeBaseTexts {
		entries = append(entries, models.NewKnowledgeBaseSource{
			Type:  models.KnowledgeBaseSourceTypeText,
			Title: text.Title,
			Text:  text.Text,
		})
	}
	return entries, true
}

// decodeUploadedSources reads an upload and turns each file into a source whose
// text this server extracted.
//
// Every file is extracted before anything is stored, so a batch that contains
// one unreadable document is refused whole, with a field error naming it. That
// matches what indexing does afterwards: the caller fixes the request and
// retries it, rather than discovering afterwards that four of five files landed.
func decodeUploadedSources(w http.ResponseWriter, r *http.Request) ([]models.NewKnowledgeBaseSource, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, models.KnowledgeBaseMaxUploadTotalBytes+uploadMultipartSlackBytes)

	if err := r.ParseMultipartForm(uploadMultipartMemoryBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, models.APIResponse{
				Success: false,
				Message: fmt.Sprintf("the upload is too large; at most %d MB may be sent in one request", models.KnowledgeBaseMaxUploadTotalBytes>>20),
			})
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "the upload could not be read as multipart/form-data",
		})
		return nil, false
	}
	// The parser spills anything above its memory bound into temp files, which
	// are this handler's to remove.
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	files := r.MultipartForm.File[models.KnowledgeBaseUploadFilesFormField]
	titles := r.MultipartForm.Value[models.KnowledgeBaseUploadTitlesFormField]

	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid request body",
			Errors: []types.FieldError{{
				Field:   models.KnowledgeBaseUploadFilesFormField,
				Message: fmt.Sprintf("attach at least one file in a %q part", models.KnowledgeBaseUploadFilesFormField),
			}},
		})
		return nil, false
	}
	if len(files) > models.KnowledgeBaseMaxFilesPerRequest {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid request body",
			Errors: []types.FieldError{{
				Field:   models.KnowledgeBaseUploadFilesFormField,
				Message: fmt.Sprintf("at most %d files may be uploaded in one request", models.KnowledgeBaseMaxFilesPerRequest),
			}},
		})
		return nil, false
	}

	var (
		entries   = make([]models.NewKnowledgeBaseSource, 0, len(files))
		fieldErrs []types.FieldError
	)
	for i, header := range files {
		field := fmt.Sprintf("%s[%d]", models.KnowledgeBaseUploadFilesFormField, i)

		// A supplied title wins over the filename, so an upload can name its
		// source; the nth title belongs to the nth file, and a blank one falls
		// back rather than failing.
		title := ""
		if i < len(titles) {
			title = strings.TrimSpace(titles[i])
		}
		if title == "" {
			title = sourceTitleFromFilename(header.Filename)
		}
		if message := describeInvalidSourceTitle(title); message != "" {
			fieldErrs = append(fieldErrs, types.FieldError{Field: field, Message: fmt.Sprintf("%s: %s", header.Filename, message)})
			continue
		}

		if header.Size > models.KnowledgeBaseMaxFileBytes {
			fieldErrs = append(fieldErrs, types.FieldError{
				Field:   field,
				Message: fmt.Sprintf("%s is larger than the %d MB limit", header.Filename, models.KnowledgeBaseMaxFileBytes>>20),
			})
			continue
		}

		data, err := readUploadedFile(header)
		if err != nil {
			if errors.Is(err, errUploadTooLarge) {
				fieldErrs = append(fieldErrs, types.FieldError{
					Field:   field,
					Message: fmt.Sprintf("%s is larger than the %d MB limit", header.Filename, models.KnowledgeBaseMaxFileBytes>>20),
				})
				continue
			}
			log.Printf("add knowledge base sources: reading upload %q failed: %v", header.Filename, err)
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Message: "failed to read the uploaded file",
			})
			return nil, false
		}

		text, err := knowledgebases.ExtractText(header.Filename, data)
		if err != nil {
			// A file this server cannot read is the caller's to fix — a wrong
			// format, an empty document, a scan with no text layer — so it reads
			// as a field error naming the file rather than as a server failure.
			if errors.Is(err, knowledgebases.ErrUnsupportedFileType) ||
				errors.Is(err, knowledgebases.ErrNoExtractableText) ||
				errors.Is(err, knowledgebases.ErrExtractedTextTooLong) {
				fieldErrs = append(fieldErrs, types.FieldError{
					Field:   field,
					Message: fmt.Sprintf("%s: %s", header.Filename, extractionMessage(err)),
				})
				continue
			}
			log.Printf("add knowledge base sources: extracting %q failed: %v", header.Filename, err)
			writeJSON(w, http.StatusInternalServerError, models.APIResponse{
				Success: false,
				Message: "failed to extract text from the uploaded file",
			})
			return nil, false
		}

		entries = append(entries, models.NewKnowledgeBaseSource{
			Type:  models.KnowledgeBaseSourceTypeFile,
			Title: title,
			Text:  text,
		})
	}

	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid request body",
			Errors:  fieldErrs,
		})
		return nil, false
	}
	return entries, true
}

// errUploadTooLarge marks a part that outran the per-file cap while it was being
// read, which is how a file whose declared size cannot be trusted is caught.
var errUploadTooLarge = errors.New("uploaded file exceeds the per-file limit")

// readUploadedFile reads one part into memory. The whole file is needed at once:
// a PDF is read through an io.ReaderAt and a .docx is a zip archive, and neither
// can be extracted from a stream.
func readUploadedFile(header *multipart.FileHeader) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// One byte past the cap, so an oversized part is recognised as such instead
	// of being silently truncated to the limit and indexed as a short document.
	data, err := io.ReadAll(io.LimitReader(file, models.KnowledgeBaseMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > models.KnowledgeBaseMaxFileBytes {
		return nil, errUploadTooLarge
	}
	return data, nil
}

// extractionMessage renders an extraction failure for a caller: the sentinel's
// own wording is what explains the problem, and the wrapper adds the detail.
func extractionMessage(err error) string {
	message := err.Error()
	if errors.Is(err, knowledgebases.ErrUnsupportedFileType) {
		return message + fmt.Sprintf(" (supported: %s)", strings.Join(knowledgebases.SupportedFileExtensions(), ", "))
	}
	return message
}

// repeatedDashes collapses the runs sourceTitleFromFilename leaves behind when a
// filename holds several characters a record id cannot carry.
var repeatedDashes = regexp.MustCompile(`-{2,}`)

// sourceTitleFromFilename derives a usable source title from an uploaded
// filename. The extension is kept: it says what the source is, and it keeps a
// document from colliding with a pasted text of the same name.
//
// The result still has to pass describeInvalidSourceTitle — this only removes
// what a vector store record id cannot carry, so a filename that is entirely
// unusable produces a title the caller is asked to supply instead.
func sourceTitleFromFilename(filename string) string {
	name := strings.TrimSpace(filename)
	// Browsers send a bare filename, but a client may send a path; only the last
	// element names the source.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case strings.ContainsRune(knowledgebases.ChunkIDSeparator, r), r < ' ', r > '~':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}

	title := strings.Join(strings.Fields(b.String()), " ")
	title = strings.Trim(repeatedDashes.ReplaceAllString(title, "-"), "- ")
	if runes := []rune(title); len(runes) > models.KnowledgeBaseMaxSourceTitleLength {
		title = strings.TrimSpace(string(runes[:models.KnowledgeBaseMaxSourceTitleLength]))
	}
	return title
}

// describeInvalidSourceTitle returns why a title cannot be stored, or "" when it
// is fine. Uniqueness is not checked here: it is per knowledge base, and the
// unique index is what decides it.
func describeInvalidSourceTitle(title string) string {
	switch {
	case title == "":
		return "a title is required, and none could be derived from the filename"
	case len([]rune(title)) > models.KnowledgeBaseMaxSourceTitleLength:
		return fmt.Sprintf("title must be at most %d characters", models.KnowledgeBaseMaxSourceTitleLength)
	case !isVectorIDSafe(title):
		return fmt.Sprintf("title is used verbatim as the vector store record id, so it must be printable ASCII and must not contain %q", knowledgebases.ChunkIDSeparator)
	}
	return ""
}

// decodeAddKnowledgeBaseSourcesRequest reads and validates the Add Knowledge
// Base Sources JSON body, writing the 400 itself and returning false when it is
// unusable.
func decodeAddKnowledgeBaseSourcesRequest(w http.ResponseWriter, r *http.Request) (addKnowledgeBaseSourcesRequest, bool) {
	var req addKnowledgeBaseSourcesRequest

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

	fieldErrs := validateAddKnowledgeBaseSourcesRequest(&req)
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

// validateAddKnowledgeBaseSourcesRequest checks the supplied texts against the
// bounds the knowledge_base_sources CHECK constraints enforce, trimming them in
// place, and rejects the source types that cannot be indexed yet.
func validateAddKnowledgeBaseSourcesRequest(req *addKnowledgeBaseSourcesRequest) []types.FieldError {
	var errs []types.FieldError

	if len(req.KnowledgeBaseURLs) > 0 {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_urls",
			Message: "url sources are not supported yet; supply knowledge_base_texts",
		})
	}
	if len(req.KnowledgeBaseFiles) > 0 {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_files",
			Message: fmt.Sprintf("files are uploaded, not linked; send them as multipart/form-data %q parts instead of file_url references", models.KnowledgeBaseUploadFilesFormField),
		})
	}

	if len(req.KnowledgeBaseTexts) == 0 {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_texts",
			Message: "supply at least one text entry",
		})
		return errs
	}
	if len(req.KnowledgeBaseTexts) > models.KnowledgeBaseMaxTextsPerRequest {
		errs = append(errs, types.FieldError{
			Field:   "knowledge_base_texts",
			Message: fmt.Sprintf("at most %d text entries may be supplied", models.KnowledgeBaseMaxTextsPerRequest),
		})
	}

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
		}
	}

	return errs
}

// isVectorIDSafe reports whether a title can be a vector store record id. The
// store requires ids to be ASCII and rejects the write outright otherwise, so
// this is checked here to answer with a field error instead of a failed index.
// The chunk separator is excluded as well: a title containing it could collide
// with the id of another source's second chunk.
func isVectorIDSafe(title string) bool {
	if strings.Contains(title, knowledgebases.ChunkIDSeparator) {
		return false
	}
	for _, r := range title {
		if r < ' ' || r > '~' {
			return false
		}
	}
	return true
}
