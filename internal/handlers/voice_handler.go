package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/elevenlabs"
	"whatsapp-ai-caller-server/internal/models"
)

// VoiceHandler exposes the ElevenLabs voice catalogue to the dashboard voice
// picker. The workspace API key stays on the server; the browser only ever sees
// the normalised voice summaries below.
type VoiceHandler struct {
	elevenLabs *elevenlabs.Client
}

// NewVoiceHandler creates a voice handler backed by the configured ElevenLabs
// API key.
func NewVoiceHandler(elevenLabsAPIKey string) *VoiceHandler {
	return &VoiceHandler{elevenLabs: elevenlabs.New(elevenLabsAPIKey)}
}

// voiceSummary is the single shape both voice sources are mapped onto, so the
// picker renders library and workspace results with one component.
type voiceSummary struct {
	VoiceID       string   `json:"voice_id"`
	PublicOwnerID string   `json:"public_owner_id,omitempty"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Category      string   `json:"category,omitempty"`
	Gender        string   `json:"gender,omitempty"`
	Age           string   `json:"age,omitempty"`
	Accent        string   `json:"accent,omitempty"`
	Language      string   `json:"language,omitempty"`
	UseCase       string   `json:"use_case,omitempty"`
	Descriptive   string   `json:"descriptive,omitempty"`
	PreviewURL    string   `json:"preview_url,omitempty"`
	ImageURL      string   `json:"image_url,omitempty"`
	Languages     []string `json:"languages,omitempty"`
	ClonedByCount int      `json:"cloned_by_count,omitempty"`
	Featured      bool     `json:"featured,omitempty"`
	// Owned voices live in the workspace and can be saved on an agent as-is;
	// library voices must be added first (see AddLibraryVoice).
	Owned bool `json:"owned"`
	Added bool `json:"added,omitempty"`
}

type voiceListData struct {
	Voices        []voiceSummary `json:"voices"`
	HasMore       bool           `json:"has_more"`
	TotalCount    int            `json:"total_count,omitempty"`
	NextPage      *int           `json:"next_page,omitempty"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

type voiceListEnvelope struct {
	Success bool          `json:"success"`
	Message string        `json:"message"`
	Data    voiceListData `json:"data"`
}

type addedVoiceData struct {
	VoiceID string `json:"voice_id" example:"nPczCjzI2devNBz1zQrb"`
	Name    string `json:"name" example:"Brian"`
}

type addedVoiceEnvelope struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Data    addedVoiceData `json:"data"`
}

type addLibraryVoiceRequest struct {
	Name string `json:"name" example:"Brian"`
}

// Library lists voices from the public ElevenLabs voice library.
//
// Library godoc
// @Summary      Browse the ElevenLabs voice library
// @Description  Proxies the ElevenLabs shared-voices API using the server's workspace key. Supports the same filters as the ElevenLabs library UI (search, category, gender, age, accent, language, use cases) with page-number pagination.
// @Tags         voices
// @Produce      json
// @Security     BearerAuth
// @Param        search      query     string  false  "Free-text search"
// @Param        category    query     string  false  "Voice category (professional, famous, high_quality)"
// @Param        gender      query     string  false  "Voice gender"
// @Param        age         query     string  false  "Voice age band"
// @Param        accent      query     string  false  "Voice accent"
// @Param        language    query     string  false  "Voice language code"
// @Param        use_cases   query     string  false  "Comma-separated use cases"
// @Param        sort        query     string  false  "Sort order (trending, created_date, cloned_by_count, usage_character_count_1y)"
// @Param        page        query     int     false  "Zero-based page index"
// @Param        page_size   query     int     false  "Results per page (1-100)"
// @Success      200  {object}  handlers.voiceListEnvelope
// @Failure      401  {object}  models.APIResponse
// @Failure      502  {object}  models.APIResponse
// @Failure      503  {object}  models.APIResponse
// @Router       /v1/voices/elevenlabs/library [get]
func (h *VoiceHandler) Library(w http.ResponseWriter, r *http.Request) {
	if _, ok := currentUser(w, r); !ok {
		return
	}

	query := r.URL.Query()
	page := queryInt(query.Get("page"), 0)
	if page < 0 {
		page = 0
	}

	result, err := h.elevenLabs.SharedVoices(r.Context(), elevenlabs.SharedVoicesQuery{
		Search:               strings.TrimSpace(query.Get("search")),
		Category:             strings.TrimSpace(query.Get("category")),
		Gender:               strings.TrimSpace(query.Get("gender")),
		Age:                  strings.TrimSpace(query.Get("age")),
		Accent:               strings.TrimSpace(query.Get("accent")),
		Language:             strings.TrimSpace(query.Get("language")),
		UseCases:             splitCSV(query.Get("use_cases")),
		Sort:                 strings.TrimSpace(query.Get("sort")),
		Page:                 page,
		PageSize:             queryInt(query.Get("page_size"), elevenlabs.DefaultPageSize),
		Featured:             query.Get("featured") == "true",
		MinNoticePeriodDays:  queryInt(query.Get("min_notice_period_days"), 0),
		IncludeCustomRates:   queryBool(query.Get("include_custom_rates")),
		IncludeLiveModerated: queryBool(query.Get("include_live_moderated")),
	})
	if err != nil {
		writeVoiceError(w, err, "failed to load the ElevenLabs voice library")
		return
	}

	data := voiceListData{
		Voices:     make([]voiceSummary, 0, len(result.Voices)),
		HasMore:    result.HasMore,
		TotalCount: result.TotalCount,
	}
	for _, voice := range result.Voices {
		data.Voices = append(data.Voices, sharedVoiceSummary(voice))
	}
	if result.HasMore {
		next := page + 1
		data.NextPage = &next
	}

	writeJSON(w, http.StatusOK, voiceListEnvelope{
		Success: true,
		Message: "Voice library retrieved successfully",
		Data:    data,
	})
}

// Workspace lists the voices already saved in the ElevenLabs workspace.
//
// Workspace godoc
// @Summary      List workspace ElevenLabs voices
// @Description  Proxies the ElevenLabs voices API using the server's workspace key. These voices are usable for calls immediately, unlike library voices. Pagination uses the opaque next_page_token returned by ElevenLabs.
// @Tags         voices
// @Produce      json
// @Security     BearerAuth
// @Param        search           query     string  false  "Free-text search"
// @Param        category         query     string  false  "Voice category (premade, cloned, generated, professional)"
// @Param        voice_type       query     string  false  "Voice type filter"
// @Param        page_size        query     int     false  "Results per page (1-100)"
// @Param        next_page_token  query     string  false  "Pagination token from a previous response"
// @Success      200  {object}  handlers.voiceListEnvelope
// @Failure      401  {object}  models.APIResponse
// @Failure      502  {object}  models.APIResponse
// @Failure      503  {object}  models.APIResponse
// @Router       /v1/voices/elevenlabs [get]
func (h *VoiceHandler) Workspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := currentUser(w, r); !ok {
		return
	}

	query := r.URL.Query()
	result, err := h.elevenLabs.Voices(r.Context(), elevenlabs.VoicesQuery{
		Search:        strings.TrimSpace(query.Get("search")),
		Category:      strings.TrimSpace(query.Get("category")),
		VoiceType:     strings.TrimSpace(query.Get("voice_type")),
		Sort:          strings.TrimSpace(query.Get("sort")),
		SortDirection: strings.TrimSpace(query.Get("sort_direction")),
		PageSize:      queryInt(query.Get("page_size"), elevenlabs.DefaultPageSize),
		NextPageToken: strings.TrimSpace(query.Get("next_page_token")),
	})
	if err != nil {
		writeVoiceError(w, err, "failed to load ElevenLabs voices")
		return
	}

	data := voiceListData{
		Voices:        make([]voiceSummary, 0, len(result.Voices)),
		HasMore:       result.HasMore,
		TotalCount:    result.TotalCount,
		NextPageToken: result.NextPageToken,
	}
	for _, voice := range result.Voices {
		data.Voices = append(data.Voices, workspaceVoiceSummary(voice))
	}

	writeJSON(w, http.StatusOK, voiceListEnvelope{
		Success: true,
		Message: "Voices retrieved successfully",
		Data:    data,
	})
}

// AddLibraryVoice copies a library voice into the workspace so it can be used
// for calls.
//
// AddLibraryVoice godoc
// @Summary      Add a library voice to the workspace
// @Description  Library voice ids cannot be used for speech until the voice is added to the workspace. This copies the voice and returns the workspace voice id to persist on the agent. Adding a voice that is already present returns the existing voice.
// @Tags         voices
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        public_owner_id  path      string                          true   "Library voice owner id"
// @Param        voice_id         path      string                          true   "Library voice id"
// @Param        body             body      handlers.addLibraryVoiceRequest false  "Name to save the voice under"
// @Success      200  {object}  handlers.addedVoiceEnvelope
// @Failure      400  {object}  models.APIResponse
// @Failure      401  {object}  models.APIResponse
// @Failure      502  {object}  models.APIResponse
// @Failure      503  {object}  models.APIResponse
// @Router       /v1/voices/elevenlabs/library/{public_owner_id}/{voice_id} [post]
func (h *VoiceHandler) AddLibraryVoice(w http.ResponseWriter, r *http.Request) {
	if _, ok := currentUser(w, r); !ok {
		return
	}

	publicOwnerID := strings.TrimSpace(chi.URLParam(r, "public_owner_id"))
	voiceID := strings.TrimSpace(chi.URLParam(r, "voice_id"))
	if publicOwnerID == "" || voiceID == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "public_owner_id and voice_id path parameters are required",
		})
		return
	}

	req, ok := decodeAddLibraryVoiceRequest(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "name is required",
		})
		return
	}

	addedID, err := h.elevenLabs.AddSharedVoice(r.Context(), publicOwnerID, voiceID, name)
	if err != nil {
		// A duplicate add is not a failure for the picker: the workspace already
		// holds a copy, so resolve it by name and reuse it.
		if existing, ok := h.resolveExistingVoice(r, err, name); ok {
			writeJSON(w, http.StatusOK, addedVoiceEnvelope{
				Success: true,
				Message: "Voice already available in the workspace",
				Data:    addedVoiceData{VoiceID: existing.VoiceID, Name: existing.Name},
			})
			return
		}
		writeVoiceError(w, err, "failed to add the voice to the workspace")
		return
	}

	writeJSON(w, http.StatusOK, addedVoiceEnvelope{
		Success: true,
		Message: "Voice added successfully",
		Data:    addedVoiceData{VoiceID: addedID, Name: name},
	})
}

// resolveExistingVoice looks a voice up by name after a duplicate-add error.
// Only the ElevenLabs "voice_already_exists" status is treated this way; any
// other failure is reported to the caller.
func (h *VoiceHandler) resolveExistingVoice(
	r *http.Request,
	addErr error,
	name string,
) (elevenlabs.Voice, bool) {
	var apiErr *elevenlabs.APIError
	if !errors.As(addErr, &apiErr) || apiErr.Code != "voice_already_exists" {
		return elevenlabs.Voice{}, false
	}

	result, err := h.elevenLabs.Voices(r.Context(), elevenlabs.VoicesQuery{
		Search:   name,
		PageSize: elevenlabs.MaxPageSize,
	})
	if err != nil {
		return elevenlabs.Voice{}, false
	}
	for _, voice := range result.Voices {
		if strings.EqualFold(strings.TrimSpace(voice.Name), name) {
			return voice, true
		}
	}
	return elevenlabs.Voice{}, false
}

func sharedVoiceSummary(voice elevenlabs.SharedVoice) voiceSummary {
	languages := make([]string, 0, len(voice.VerifiedLanguages))
	seen := make(map[string]struct{}, len(voice.VerifiedLanguages))
	for _, verified := range voice.VerifiedLanguages {
		language := strings.TrimSpace(verified.Language)
		if language == "" {
			continue
		}
		if _, duplicate := seen[language]; duplicate {
			continue
		}
		seen[language] = struct{}{}
		languages = append(languages, language)
	}

	return voiceSummary{
		VoiceID:       voice.VoiceID,
		PublicOwnerID: voice.PublicOwnerID,
		Name:          voice.Name,
		Description:   voice.Description,
		Category:      voice.Category,
		Gender:        voice.Gender,
		Age:           voice.Age,
		Accent:        voice.Accent,
		Language:      voice.Language,
		UseCase:       voice.UseCase,
		Descriptive:   voice.Descriptive,
		PreviewURL:    voice.PreviewURL,
		ImageURL:      voice.ImageURL,
		Languages:     languages,
		ClonedByCount: voice.ClonedByCount,
		Featured:      voice.Featured,
		Owned:         false,
		Added:         voice.IsAddedByUser,
	}
}

// workspaceVoiceSummary reads the descriptive fields out of the free-form
// labels map ElevenLabs attaches to workspace voices.
func workspaceVoiceSummary(voice elevenlabs.Voice) voiceSummary {
	label := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(voice.Labels[key]); value != "" {
				return value
			}
		}
		return ""
	}

	return voiceSummary{
		VoiceID:     voice.VoiceID,
		Name:        voice.Name,
		Description: voice.Description,
		Category:    voice.Category,
		Gender:      label("gender"),
		Age:         label("age"),
		Accent:      label("accent"),
		Language:    label("language"),
		UseCase:     label("use_case", "use case"),
		Descriptive: label("descriptive", "description"),
		PreviewURL:  voice.PreviewURL,
		Owned:       true,
		Added:       true,
	}
}

// writeVoiceError maps upstream failures onto our envelope: a missing API key
// is a server configuration problem (503), an upstream rejection is surfaced
// with its own message so filter mistakes are visible, and anything else is a
// bad gateway.
func writeVoiceError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, elevenlabs.ErrNotConfigured) {
		writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{
			Success: false,
			Message: "ElevenLabs is not configured on this server",
		})
		return
	}

	var apiErr *elevenlabs.APIError
	if errors.As(err, &apiErr) {
		status := http.StatusBadGateway
		if apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden {
			status = http.StatusServiceUnavailable
		}
		if apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnprocessableEntity {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, models.APIResponse{Success: false, Message: apiErr.Message})
		return
	}

	writeJSON(w, http.StatusBadGateway, models.APIResponse{Success: false, Message: fallback})
}

// decodeAddLibraryVoiceRequest reads the optional JSON body. An absent body is
// accepted here and rejected by the empty-name check in the handler, which
// gives a clearer message than a decode error would.
func decodeAddLibraryVoiceRequest(w http.ResponseWriter, r *http.Request) (addLibraryVoiceRequest, bool) {
	var req addLibraryVoiceRequest
	if r.Body == nil {
		return req, true
	}

	err := json.NewDecoder(r.Body).Decode(&req)
	if err == nil || errors.Is(err, io.EOF) {
		return req, true
	}

	writeJSON(w, http.StatusBadRequest, models.APIResponse{
		Success: false,
		Message: "Invalid request body",
	})
	return addLibraryVoiceRequest{}, false
}

func queryInt(raw string, fallback int) int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return parsed
	}
	return fallback
}

func queryBool(raw string) *bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		value := true
		return &value
	case "false", "0":
		value := false
		return &value
	default:
		return nil
	}
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
