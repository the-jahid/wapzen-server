package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
	"whatsapp-ai-caller-server/internal/tools"
)

// Default and bound values for the list query parameters, mirroring the
// documented schema for GET /v1/tools (page ≥ 1 default 1; limit 1..200 default
// 50).
const (
	defaultToolPage  = 1
	defaultToolLimit = 50
	maxToolLimit     = 200
)

// toolsListPath is the collection URL rendered into the list links.
const toolsListPath = "/v1/tools"

// ToolHandler serves the Tools endpoints — the CRUD over the actions an agent
// can take mid-call. It only defines them; running one is the voice-call
// runtime's job (internal/voicecall/tools.go).
type ToolHandler struct {
	repo *tools.Repository
}

// NewToolHandler creates a tool handler with its dependencies.
func NewToolHandler(repo *tools.Repository) *ToolHandler {
	return &ToolHandler{repo: repo}
}

type toolEnvelope struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    models.Tool `json:"data"`
	Links   toolLinks   `json:"links"`
}

type toolLinks struct {
	Self string `json:"self"`
}

type toolListEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    []models.Tool   `json:"data"`
	Meta    *types.Meta     `json:"meta,omitempty"`
	Links   types.ListLinks `json:"links"`
}

type toolDeleteEnvelope struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Data    toolDeleteData `json:"data"`
}

type toolDeleteData struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// toolRequest is the shared body shape for Create and Update. The pointers are
// what separates "absent" from "supplied but empty" on an update: a nil Name
// leaves the stored name alone, whereas a pointer to "" is a name the caller
// actually sent and is rejected as invalid.
type toolRequest struct {
	Type         *string                        `json:"type"`
	Name         *string                        `json:"name"`
	Description  *string                        `json:"description"`
	APIRequest   *models.ToolAPIRequestConfig   `json:"api_request"`
	TransferCall *models.ToolTransferCallConfig `json:"transfer_call"`
	SendText     *models.ToolSendTextConfig     `json:"send_text"`
	EndCall      *models.ToolEndCallConfig      `json:"end_call"`
}

// Create stores a tool owned by the authenticated user.
//
// The tool is inert until an agent is attached to it: creating it defines the
// action, it does not offer it on any call. Attach it through the agent's
// tools.tool_ids.
//
// Create godoc
// @Summary      Create Tool
// @Description  Creates a tool the authenticated user's agents can call mid-call. type, name and description are required, and the configuration block matching type must be supplied for api_request and transfer_call. name is the function name the model calls, so it must be lowercase letters, digits and underscores, and unique across the user's tools. The tool does nothing until it is attached to an agent via tools.tool_ids.
// @Tags         Tools
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.toolRequest  true  "The tool to create"
// @Success      201   {object}  handlers.toolEnvelope
// @Failure      400   {object}  types.ErrorEnvelope
// @Failure      401   {object}  models.APIResponse
// @Failure      409   {object}  models.APIResponse
// @Failure      500   {object}  models.APIResponse
// @Router       /v1/tools [post]
func (h *ToolHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	var req toolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	toolType := strings.TrimSpace(derefString(req.Type))
	name := strings.TrimSpace(derefString(req.Name))
	description := strings.TrimSpace(derefString(req.Description))

	if errs := validateCreateTool(toolType, name, description, req); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Validation failed",
			Errors:  errs,
		})
		return
	}

	tool, err := h.repo.Create(r.Context(), models.NewTool{
		UserID:       user.ID,
		Type:         toolType,
		Name:         name,
		Description:  description,
		APIRequest:   normalizedAPIRequest(req.APIRequest),
		TransferCall: req.TransferCall,
		SendText:     req.SendText,
		EndCall:      req.EndCall,
	})
	if err != nil {
		if errors.Is(err, tools.ErrNameTaken) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a tool with this name already exists",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to create tool",
		})
		return
	}

	writeJSON(w, http.StatusCreated, toolEnvelope{
		Success: true,
		Message: "Tool created successfully",
		Data:    tool,
		Links:   toolLinks{Self: toolsListPath + "/" + tool.ID},
	})
}

// List returns a page of the authenticated user's tools, newest first.
//
// List godoc
// @Summary      List Tools
// @Description  Returns a paginated list of the tools owned by the authenticated user, newest first. Pass type to narrow the page to one tool type.
// @Tags         Tools
// @Produce      json
// @Security     BearerAuth
// @Param        page   query     int     false  "Page number (1-based)"
// @Param        limit  query     int     false  "Tools per page (1-200)"
// @Param        type   query     string  false  "Return only tools of this type"
// @Success      200    {object}  handlers.toolListEnvelope
// @Failure      400    {object}  types.ErrorEnvelope
// @Failure      401    {object}  models.APIResponse
// @Failure      500    {object}  models.APIResponse
// @Router       /v1/tools [get]
func (h *ToolHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	page, limit, toolType, fieldErrs := parseToolListQuery(r.URL.Query())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Invalid query parameters",
			Errors:  fieldErrs,
		})
		return
	}

	list, total, err := h.repo.ListByUser(r.Context(), user.ID, toolType, limit, (page-1)*limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "failed to list tools",
		})
		return
	}

	meta := buildPaginationMeta(page, limit, total)
	writeJSON(w, http.StatusOK, toolListEnvelope{
		Success: true,
		Message: "Tools retrieved successfully",
		Data:    list,
		Meta:    &meta,
		Links:   buildToolListLinks(page, limit, total, toolType),
	})
}

// Get returns a single tool owned by the authenticated user.
//
// Get godoc
// @Summary      Get Tool
// @Description  Returns a single tool owned by the authenticated user, including its configuration block.
// @Tags         Tools
// @Produce      json
// @Security     BearerAuth
// @Param        tool_id  path      string  true  "Tool id"
// @Success      200      {object}  handlers.toolEnvelope
// @Failure      401      {object}  models.APIResponse
// @Failure      404      {object}  models.APIResponse
// @Failure      500      {object}  models.APIResponse
// @Router       /v1/tools/{tool_id} [get]
func (h *ToolHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	toolID := strings.TrimSpace(chi.URLParam(r, "tool_id"))
	tool, err := h.repo.GetByUser(r.Context(), user.ID, toolID)
	if err != nil {
		h.writeLookupError(w, err, "failed to get tool")
		return
	}

	writeJSON(w, http.StatusOK, toolEnvelope{
		Success: true,
		Message: "Tool retrieved successfully",
		Data:    tool,
		Links:   toolLinks{Self: toolsListPath + "/" + tool.ID},
	})
}

// Update applies a partial update to a tool owned by the authenticated user.
//
// Update godoc
// @Summary      Update Tool
// @Description  Applies a partial update to a tool; at least one field must be supplied. A configuration block replaces the stored one wholesale rather than merging into it. type is fixed at creation — delete the tool and create it again to change it. Changes take effect on the next call: a call already in progress keeps the tool definitions it started with.
// @Tags         Tools
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        tool_id  path      string                true  "Tool id"
// @Param        body     body      handlers.toolRequest  true  "The tool fields to change"
// @Success      200      {object}  handlers.toolEnvelope
// @Failure      400      {object}  types.ErrorEnvelope
// @Failure      401      {object}  models.APIResponse
// @Failure      404      {object}  models.APIResponse
// @Failure      409      {object}  models.APIResponse
// @Failure      500      {object}  models.APIResponse
// @Router       /v1/tools/{tool_id} [patch]
func (h *ToolHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	toolID := strings.TrimSpace(chi.URLParam(r, "tool_id"))

	var req toolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	// The stored type decides which configuration block this update may carry, so
	// the tool is read before anything is validated. It also gives the 404 its own
	// query rather than making it depend on which fields the body happened to set.
	current, err := h.repo.GetByUser(r.Context(), user.ID, toolID)
	if err != nil {
		h.writeLookupError(w, err, "failed to update tool")
		return
	}

	var errs []types.FieldError
	if req.Type != nil && strings.TrimSpace(*req.Type) != current.Type {
		errs = append(errs, types.FieldError{
			Field:   "type",
			Message: "type cannot be changed after creation; delete the tool and create it again",
		})
	}

	update := models.ToolUpdate{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		errs = append(errs, validateToolName(name)...)
		update.Name = &name
	}
	if req.Description != nil {
		description := strings.TrimSpace(*req.Description)
		errs = append(errs, validateToolDescription(description)...)
		update.Description = &description
	}

	// A block for a type this tool is not would silently do nothing, so it is
	// refused rather than dropped: a caller who sent it believes it took effect.
	errs = append(errs, rejectForeignToolConfig(current.Type, req)...)
	if suppliedToolConfig(current.Type, req) {
		errs = append(errs, validateToolConfig(current.Type, req, false)...)
		update.APIRequest = normalizedAPIRequest(req.APIRequest)
		update.TransferCall = req.TransferCall
		update.SendText = req.SendText
		update.EndCall = req.EndCall
	}

	if len(errs) == 0 && update.IsEmpty() {
		errs = append(errs, types.FieldError{
			Field:   "body",
			Message: "supply at least one field to update",
		})
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, types.ErrorEnvelope{
			Success: false,
			Message: "Validation failed",
			Errors:  errs,
		})
		return
	}

	tool, err := h.repo.UpdateByUser(r.Context(), user.ID, toolID, update)
	if err != nil {
		if errors.Is(err, tools.ErrNameTaken) {
			writeJSON(w, http.StatusConflict, models.APIResponse{
				Success: false,
				Message: "a tool with this name already exists",
			})
			return
		}
		h.writeLookupError(w, err, "failed to update tool")
		return
	}

	writeJSON(w, http.StatusOK, toolEnvelope{
		Success: true,
		Message: "Tool updated successfully",
		Data:    tool,
		Links:   toolLinks{Self: toolsListPath + "/" + tool.ID},
	})
}

// Delete removes a tool owned by the authenticated user, detaching it from every
// agent using it.
//
// Delete godoc
// @Summary      Delete Tool
// @Description  Deletes a tool owned by the authenticated user and detaches it from every agent using it. Calls already in progress keep the tool for the rest of the call.
// @Tags         Tools
// @Produce      json
// @Security     BearerAuth
// @Param        tool_id  path      string  true  "Tool id"
// @Success      200      {object}  handlers.toolDeleteEnvelope
// @Failure      401      {object}  models.APIResponse
// @Failure      404      {object}  models.APIResponse
// @Failure      500      {object}  models.APIResponse
// @Router       /v1/tools/{tool_id} [delete]
func (h *ToolHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(w, r)
	if !ok {
		return
	}

	toolID := strings.TrimSpace(chi.URLParam(r, "tool_id"))
	if err := h.repo.DeleteByUser(r.Context(), user.ID, toolID); err != nil {
		h.writeLookupError(w, err, "failed to delete tool")
		return
	}

	writeJSON(w, http.StatusOK, toolDeleteEnvelope{
		Success: true,
		Message: "Tool deleted successfully",
		Data:    toolDeleteData{ID: toolID, Deleted: true},
	})
}

// writeLookupError turns a repository error into the response for it: a missing
// (or foreign) tool is a 404, everything else a 500 under the supplied message.
func (h *ToolHandler) writeLookupError(w http.ResponseWriter, err error, failureMessage string) {
	if errors.Is(err, tools.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, models.APIResponse{
			Success: false,
			Message: "tool not found",
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, models.APIResponse{
		Success: false,
		Message: failureMessage,
	})
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// validateCreateTool checks everything a create request has to get right. It is
// one function rather than a run of calls inside the handler so the whole rule
// set is testable without a repository behind it.
func validateCreateTool(toolType, name, description string, req toolRequest) []types.FieldError {
	var errs []types.FieldError
	if !models.IsValidToolType(toolType) {
		errs = append(errs, types.FieldError{
			Field:   "type",
			Message: "type must be one of " + strings.Join(models.ToolTypes(), ", "),
		})
	}
	errs = append(errs, validateToolName(name)...)
	errs = append(errs, validateToolDescription(description)...)

	// The configuration blocks are only judged once the type is known: which one
	// is required, and which ones this type would never read, both follow from it.
	// A foreign block is refused here for the same reason Update refuses one —
	// accepting the request and quietly dropping half of it is how a tool ends up
	// looking configured and doing nothing.
	if models.IsValidToolType(toolType) {
		errs = append(errs, rejectForeignToolConfig(toolType, req)...)
		errs = append(errs, validateToolConfig(toolType, req, true)...)
	}
	return errs
}

func validateToolName(name string) []types.FieldError {
	switch {
	case name == "":
		return []types.FieldError{{Field: "name", Message: "name is required"}}
	case !models.IsValidToolName(name):
		return []types.FieldError{{
			Field: "name",
			Message: fmt.Sprintf(
				"name must start with a lowercase letter and contain only lowercase letters, digits and underscores (at most %d characters)",
				models.ToolMaxNameLength,
			),
		}}
	}
	return nil
}

func validateToolDescription(description string) []types.FieldError {
	switch {
	case description == "":
		return []types.FieldError{{
			Field:   "description",
			Message: "description is required — it is what the model reads when deciding whether to call this tool",
		}}
	case len(description) > models.ToolMaxDescriptionLength:
		return []types.FieldError{{
			Field:   "description",
			Message: fmt.Sprintf("description must be at most %d characters", models.ToolMaxDescriptionLength),
		}}
	}
	return nil
}

// suppliedToolConfig reports whether the request carries the configuration block
// matching toolType.
func suppliedToolConfig(toolType string, req toolRequest) bool {
	switch toolType {
	case models.ToolTypeAPIRequest:
		return req.APIRequest != nil
	case models.ToolTypeTransferCall:
		return req.TransferCall != nil
	case models.ToolTypeSendText:
		return req.SendText != nil
	case models.ToolTypeEndCall:
		return req.EndCall != nil
	}
	return false
}

// rejectForeignToolConfig reports the configuration blocks the request supplied
// that this tool's type does not read. They are refused rather than ignored:
// storing the request and quietly dropping half of it is how a tool ends up
// looking configured and doing nothing.
func rejectForeignToolConfig(toolType string, req toolRequest) []types.FieldError {
	var errs []types.FieldError
	check := func(field string, supplied bool) {
		if supplied {
			errs = append(errs, types.FieldError{
				Field:   field,
				Message: fmt.Sprintf("%s is not read by a %s tool", field, toolType),
			})
		}
	}
	check("api_request", req.APIRequest != nil && toolType != models.ToolTypeAPIRequest)
	check("transfer_call", req.TransferCall != nil && toolType != models.ToolTypeTransferCall)
	check("send_text", req.SendText != nil && toolType != models.ToolTypeSendText)
	check("end_call", req.EndCall != nil && toolType != models.ToolTypeEndCall)
	return errs
}

// validateToolConfig checks the configuration block matching toolType. required
// says whether the block has to be present at all: it does on create for the
// types that cannot act without one, while an update may leave the stored block
// untouched.
func validateToolConfig(toolType string, req toolRequest, required bool) []types.FieldError {
	switch toolType {
	case models.ToolTypeAPIRequest:
		if req.APIRequest == nil {
			if required {
				return []types.FieldError{{
					Field:   "api_request",
					Message: "api_request is required when type is api_request",
				}}
			}
			return nil
		}
		return validateAPIRequestConfig(*req.APIRequest)

	case models.ToolTypeTransferCall:
		if req.TransferCall == nil {
			if required {
				return []types.FieldError{{
					Field:   "transfer_call",
					Message: "transfer_call is required when type is transfer_call",
				}}
			}
			return nil
		}
		return validateTransferCallConfig(*req.TransferCall)

	case models.ToolTypeSendText:
		if req.SendText == nil {
			return nil
		}
		if len(req.SendText.Body) > models.ToolMaxMessageLength {
			return []types.FieldError{{
				Field:   "send_text.body",
				Message: fmt.Sprintf("body must be at most %d characters", models.ToolMaxMessageLength),
			}}
		}

	case models.ToolTypeEndCall:
		if req.EndCall == nil {
			return nil
		}
		if len(req.EndCall.Message) > models.ToolMaxMessageLength {
			return []types.FieldError{{
				Field:   "end_call.message",
				Message: fmt.Sprintf("message must be at most %d characters", models.ToolMaxMessageLength),
			}}
		}
	}
	return nil
}

func validateAPIRequestConfig(cfg models.ToolAPIRequestConfig) []types.FieldError {
	var errs []types.FieldError

	if method := models.NormalizeToolMethod(cfg.Method); !models.IsValidToolHTTPMethod(method) {
		errs = append(errs, types.FieldError{
			Field:   "api_request.method",
			Message: "method must be one of " + strings.Join(models.ToolHTTPMethods(), ", "),
		})
	}

	errs = append(errs, validateToolURL(cfg.URL)...)

	if cfg.TimeoutSeconds != 0 &&
		(cfg.TimeoutSeconds < models.ToolMinTimeoutSeconds || cfg.TimeoutSeconds > models.ToolMaxTimeoutSeconds) {
		errs = append(errs, types.FieldError{
			Field: "api_request.timeout_seconds",
			Message: fmt.Sprintf("timeout_seconds must be between %d and %d",
				models.ToolMinTimeoutSeconds, models.ToolMaxTimeoutSeconds),
		})
	}

	if len(cfg.Headers) > models.ToolMaxHeaders {
		errs = append(errs, types.FieldError{
			Field:   "api_request.headers",
			Message: fmt.Sprintf("at most %d headers are allowed", models.ToolMaxHeaders),
		})
	}
	for i, header := range cfg.Headers {
		if strings.TrimSpace(header.Key) == "" {
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("api_request.headers[%d].key", i),
				Message: "header key is required",
			})
		}
	}

	if len(cfg.Parameters) > models.ToolMaxParameters {
		errs = append(errs, types.FieldError{
			Field:   "api_request.parameters",
			Message: fmt.Sprintf("at most %d parameters are allowed", models.ToolMaxParameters),
		})
	}
	seen := make(map[string]struct{}, len(cfg.Parameters))
	for i, param := range cfg.Parameters {
		name := strings.TrimSpace(param.Name)
		switch {
		case name == "":
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("api_request.parameters[%d].name", i),
				Message: "parameter name is required",
			})
		case len(name) > models.ToolMaxParameterNameLength:
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("api_request.parameters[%d].name", i),
				Message: fmt.Sprintf("parameter name must be at most %d characters", models.ToolMaxParameterNameLength),
			})
		default:
			if _, duplicate := seen[name]; duplicate {
				errs = append(errs, types.FieldError{
					Field:   fmt.Sprintf("api_request.parameters[%d].name", i),
					Message: "parameter names must be unique within a tool",
				})
			}
			seen[name] = struct{}{}
		}
		// An empty type is filled in as "string" by normalizedAPIRequest, matching
		// the documented default; anything else has to be one the model can fill.
		if paramType := strings.TrimSpace(param.Type); paramType != "" && !models.IsValidToolParameterType(paramType) {
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("api_request.parameters[%d].type", i),
				Message: "type must be one of " + strings.Join(models.ToolParameterTypes(), ", "),
			})
		}
		if strings.TrimSpace(param.Description) == "" {
			errs = append(errs, types.FieldError{
				Field:   fmt.Sprintf("api_request.parameters[%d].description", i),
				Message: "description is required — it is the only thing telling the model what to put here",
			})
		}
	}
	return errs
}

// validateToolURL checks the endpoint an api_request tool reaches out to. Only
// absolute http/https URLs with a host are accepted; whether that host is one
// the server may actually reach is decided at call time, where DNS has resolved
// and the answer cannot be changed after the check (see voicecall).
func validateToolURL(raw string) []types.FieldError {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []types.FieldError{{Field: "api_request.url", Message: "url is required"}}
	}
	if len(trimmed) > models.ToolMaxURLLength {
		return []types.FieldError{{
			Field:   "api_request.url",
			Message: fmt.Sprintf("url must be at most %d characters", models.ToolMaxURLLength),
		}}
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return []types.FieldError{{Field: "api_request.url", Message: "url must be an absolute URL"}}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return []types.FieldError{{Field: "api_request.url", Message: "url must use http or https"}}
	}
	return nil
}

func validateTransferCallConfig(cfg models.ToolTransferCallConfig) []types.FieldError {
	var errs []types.FieldError
	destination := strings.TrimSpace(cfg.Destination)
	if destination == "" {
		errs = append(errs, types.FieldError{
			Field:   "transfer_call.destination",
			Message: "destination is required",
		})
	} else if !isE164Like(destination) {
		errs = append(errs, types.FieldError{
			Field:   "transfer_call.destination",
			Message: "destination must be a phone number in E.164 form, e.g. +8801639726992",
		})
	}
	if len(cfg.Message) > models.ToolMaxMessageLength {
		errs = append(errs, types.FieldError{
			Field:   "transfer_call.message",
			Message: fmt.Sprintf("message must be at most %d characters", models.ToolMaxMessageLength),
		})
	}
	return errs
}

// isE164Like accepts a leading + followed by 7 to 15 digits. It is deliberately
// permissive about which ranges exist: the point is to catch a name or a URL
// typed into the destination field, not to validate numbering plans.
func isE164Like(value string) bool {
	if !strings.HasPrefix(value, "+") {
		return false
	}
	digits := value[1:]
	if len(digits) < 7 || len(digits) > 15 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normalizedAPIRequest fills in the documented defaults so the stored block is
// complete: whatever is read back later runs the same request, without the
// runtime having to re-apply defaults the caller never saw.
func normalizedAPIRequest(cfg *models.ToolAPIRequestConfig) *models.ToolAPIRequestConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Method = models.NormalizeToolMethod(out.Method)
	out.URL = strings.TrimSpace(out.URL)
	if out.TimeoutSeconds <= 0 {
		out.TimeoutSeconds = models.ToolDefaultTimeoutSeconds
	}

	headers := make([]models.ToolHeader, 0, len(cfg.Headers))
	for _, header := range cfg.Headers {
		key := strings.TrimSpace(header.Key)
		if key == "" {
			continue
		}
		headers = append(headers, models.ToolHeader{Key: key, Value: strings.TrimSpace(header.Value)})
	}
	out.Headers = headers

	parameters := make([]models.ToolParameter, 0, len(cfg.Parameters))
	for _, param := range cfg.Parameters {
		paramType := strings.TrimSpace(param.Type)
		if paramType == "" {
			paramType = "string"
		}
		parameters = append(parameters, models.ToolParameter{
			Name:        strings.TrimSpace(param.Name),
			Type:        paramType,
			Description: strings.TrimSpace(param.Description),
			Required:    param.Required,
		})
	}
	out.Parameters = parameters
	return &out
}

func parseToolListQuery(q url.Values) (page, limit int, toolType string, errs []types.FieldError) {
	page, limit = defaultToolPage, defaultToolLimit

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		if p, err := strconv.Atoi(raw); err != nil || p < 1 {
			errs = append(errs, types.FieldError{Field: "page", Message: "page must be a positive integer"})
		} else {
			page = p
		}
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if l, err := strconv.Atoi(raw); err != nil || l < 1 || l > maxToolLimit {
			errs = append(errs, types.FieldError{
				Field:   "limit",
				Message: fmt.Sprintf("limit must be between 1 and %d", maxToolLimit),
			})
		} else {
			limit = l
		}
	}

	if raw := strings.TrimSpace(q.Get("type")); raw != "" {
		if !models.IsValidToolType(raw) {
			errs = append(errs, types.FieldError{
				Field:   "type",
				Message: "type must be one of " + strings.Join(models.ToolTypes(), ", "),
			})
		} else {
			toolType = raw
		}
	}

	return page, limit, toolType, errs
}

// buildToolListLinks renders the collection links, carrying the type filter
// through so paging a filtered collection stays filtered.
func buildToolListLinks(page, limit, total int, toolType string) types.ListLinks {
	links := buildListLinks(toolsListPath, page, limit, total, nil)
	if toolType == "" {
		return links
	}
	suffix := "&type=" + url.QueryEscape(toolType)
	appendFilter := func(link string) string {
		if link == "" {
			return link
		}
		return link + suffix
	}
	links.Self = appendFilter(links.Self)
	links.First = appendFilter(links.First)
	links.Last = appendFilter(links.Last)
	if links.Previous != nil {
		previous := appendFilter(*links.Previous)
		links.Previous = &previous
	}
	if links.Next != nil {
		next := appendFilter(*links.Next)
		links.Next = &next
	}
	return links
}
