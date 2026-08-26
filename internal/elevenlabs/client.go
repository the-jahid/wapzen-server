// Package elevenlabs is a thin client for the public ElevenLabs voice
// discovery APIs used by the dashboard voice picker: the shared voice library
// ("Explore"), the workspace's own voices ("My Voices"), and the call that
// copies a library voice into the workspace so it can be used for TTS.
//
// The live call pipeline talks to ElevenLabs separately (internal/voicecall);
// this package deliberately covers voice selection only.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.elevenlabs.io"
	requestTimeout = 20 * time.Second

	// DefaultPageSize matches the ElevenLabs library default; MaxPageSize is the
	// largest page either endpoint accepts.
	DefaultPageSize = 30
	MaxPageSize     = 100

	// placeholderAPIKey is the value shipped in .env.example. Treating it as
	// unset keeps a fresh checkout from making doomed upstream calls.
	placeholderAPIKey = "elevenlabs_replace_me"
)

// ErrNotConfigured is returned when no usable ELEVENLABS_API_KEY is present.
var ErrNotConfigured = errors.New("elevenlabs: api key is not configured")

// APIError carries an upstream failure so callers can mirror its status code.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("elevenlabs: %s (%d): %s", e.Code, e.Status, e.Message)
	}
	return fmt.Sprintf("elevenlabs: request failed (%d): %s", e.Status, e.Message)
}

// Client calls the ElevenLabs REST API with the workspace API key.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New builds a client for the given API key. A blank or placeholder key yields
// a client that reports Enabled() == false and fails every call with
// ErrNotConfigured, so callers can construct one unconditionally.
func New(apiKey string) *Client {
	return &Client{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: requestTimeout},
	}
}

// Enabled reports whether the client has a usable API key.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != "" && c.apiKey != placeholderAPIKey
}

// VerifiedLanguage is one language an ElevenLabs voice has been verified for.
type VerifiedLanguage struct {
	Language string `json:"language"`
	Accent   string `json:"accent"`
	Locale   string `json:"locale"`
}

// SharedVoice is one entry of the public voice library.
type SharedVoice struct {
	VoiceID           string             `json:"voice_id"`
	PublicOwnerID     string             `json:"public_owner_id"`
	Name              string             `json:"name"`
	Description       string             `json:"description"`
	Category          string             `json:"category"`
	Gender            string             `json:"gender"`
	Age               string             `json:"age"`
	Accent            string             `json:"accent"`
	Language          string             `json:"language"`
	Locale            string             `json:"locale"`
	Descriptive       string             `json:"descriptive"`
	UseCase           string             `json:"use_case"`
	PreviewURL        string             `json:"preview_url"`
	ImageURL          string             `json:"image_url"`
	ClonedByCount     int                `json:"cloned_by_count"`
	Featured          bool               `json:"featured"`
	FreeUsersAllowed  bool               `json:"free_users_allowed"`
	IsAddedByUser     bool               `json:"is_added_by_user"`
	VerifiedLanguages []VerifiedLanguage `json:"verified_languages"`
}

// SharedVoicesPage is one page of library results.
type SharedVoicesPage struct {
	Voices     []SharedVoice `json:"voices"`
	HasMore    bool          `json:"has_more"`
	TotalCount int           `json:"total_count"`
}

// Voice is one voice already in the workspace ("My Voices"). Unlike library
// entries these are usable for TTS as-is.
type Voice struct {
	VoiceID     string            `json:"voice_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	PreviewURL  string            `json:"preview_url"`
	Labels      map[string]string `json:"labels"`
}

// VoicesPage is one page of workspace voices. ElevenLabs paginates these with
// an opaque token instead of a page number.
type VoicesPage struct {
	Voices        []Voice `json:"voices"`
	HasMore       bool    `json:"has_more"`
	TotalCount    int     `json:"total_count"`
	NextPageToken string  `json:"next_page_token"`
}

// SharedVoicesQuery mirrors the filters of the ElevenLabs library UI. Empty
// fields are omitted, which upstream reads as "no filter".
type SharedVoicesQuery struct {
	Search              string
	Category            string
	Gender              string
	Age                 string
	Accent              string
	Language            string
	UseCases            []string
	Sort                string
	Page                int
	PageSize            int
	Featured            bool
	MinNoticePeriodDays int
	// Nil keeps the upstream default; a value forces the filter either way.
	IncludeCustomRates   *bool
	IncludeLiveModerated *bool
}

// VoicesQuery filters the workspace voice list.
type VoicesQuery struct {
	Search        string
	Category      string
	VoiceType     string
	Sort          string
	SortDirection string
	PageSize      int
	NextPageToken string
}

// SharedVoices lists voices from the public library (GET /v1/shared-voices).
func (c *Client) SharedVoices(ctx context.Context, q SharedVoicesQuery) (SharedVoicesPage, error) {
	values := url.Values{}
	setString(values, "search", q.Search)
	setString(values, "category", q.Category)
	setString(values, "gender", q.Gender)
	setString(values, "age", q.Age)
	setString(values, "accent", q.Accent)
	setString(values, "language", q.Language)
	setString(values, "sort", q.Sort)
	// Array filters are repeated params, not a joined list.
	for _, useCase := range q.UseCases {
		if trimmed := strings.TrimSpace(useCase); trimmed != "" {
			values.Add("use_cases", trimmed)
		}
	}
	values.Set("page", strconv.Itoa(max(q.Page, 0)))
	values.Set("page_size", strconv.Itoa(clampPageSize(q.PageSize)))
	if q.Featured {
		values.Set("featured", "true")
	}
	if q.MinNoticePeriodDays > 0 {
		values.Set("min_notice_period_days", strconv.Itoa(q.MinNoticePeriodDays))
	}
	setBool(values, "include_custom_rates", q.IncludeCustomRates)
	setBool(values, "include_live_moderated", q.IncludeLiveModerated)

	var page SharedVoicesPage
	err := c.do(ctx, http.MethodGet, "/v1/shared-voices", values, nil, &page)
	return page, err
}

// Voices lists the workspace's own voices (GET /v2/voices).
func (c *Client) Voices(ctx context.Context, q VoicesQuery) (VoicesPage, error) {
	values := url.Values{}
	setString(values, "search", q.Search)
	setString(values, "category", q.Category)
	setString(values, "voice_type", q.VoiceType)
	setString(values, "sort", q.Sort)
	setString(values, "sort_direction", q.SortDirection)
	setString(values, "next_page_token", q.NextPageToken)
	values.Set("page_size", strconv.Itoa(clampPageSize(q.PageSize)))

	var page VoicesPage
	err := c.do(ctx, http.MethodGet, "/v2/voices", values, nil, &page)
	return page, err
}

// AddSharedVoice copies a library voice into the workspace and returns the new
// workspace voice id. Library voice ids are not usable for TTS until they are
// added this way, so the picker calls this before persisting a selection.
func (c *Client) AddSharedVoice(ctx context.Context, publicOwnerID, voiceID, name string) (string, error) {
	publicOwnerID = strings.TrimSpace(publicOwnerID)
	voiceID = strings.TrimSpace(voiceID)
	name = strings.TrimSpace(name)
	if publicOwnerID == "" || voiceID == "" || name == "" {
		return "", errors.New("elevenlabs: public owner id, voice id and name are required")
	}

	body := map[string]any{"new_name": name}
	path := fmt.Sprintf("/v1/voices/add/%s/%s", url.PathEscape(publicOwnerID), url.PathEscape(voiceID))

	var added struct {
		VoiceID string `json:"voice_id"`
	}
	if err := c.do(ctx, http.MethodPost, path, nil, body, &added); err != nil {
		return "", err
	}
	if strings.TrimSpace(added.VoiceID) == "" {
		return "", errors.New("elevenlabs: add shared voice returned no voice id")
	}
	return added.VoiceID, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("elevenlabs: encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("elevenlabs: build request: %w", err)
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("elevenlabs: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Cap the read so a malformed upstream response cannot exhaust memory; a
	// full page of 100 voices is well under this.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("elevenlabs: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp.StatusCode, raw)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("elevenlabs: decode response: %w", err)
	}
	return nil
}

// newAPIError unpacks the two error envelopes ElevenLabs uses: a structured
// {"detail":{"status":...,"message":...}} object and a plain string detail.
func newAPIError(status int, raw []byte) *APIError {
	apiErr := &APIError{Status: status, Message: strings.TrimSpace(string(raw))}

	var structured struct {
		Detail struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(raw, &structured); err == nil && structured.Detail.Message != "" {
		apiErr.Code = structured.Detail.Status
		apiErr.Message = structured.Detail.Message
		return apiErr
	}

	var plain struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &plain); err == nil && plain.Detail != "" {
		apiErr.Message = plain.Detail
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(status)
	}
	return apiErr
}

func setString(values url.Values, key, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		values.Set(key, trimmed)
	}
}

func setBool(values url.Values, key string, value *bool) {
	if value != nil {
		values.Set(key, strconv.FormatBool(*value))
	}
}

func clampPageSize(size int) int {
	if size <= 0 {
		return DefaultPageSize
	}
	if size > MaxPageSize {
		return MaxPageSize
	}
	return size
}
