package broadcasts

// Broadcast contacts (the broadcast_contacts table, whiskey migration
// 20261123000002_broadcast_contacts): the people a broadcast calls. The phone
// number is required; every other field is optional. The same number may be
// added any number of times: each one is its own contact and gets its own
// call. Every contact added is queued for the auto-dialer (dialer.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/httpx"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// Field limits for the optional contact fields.
const (
	maxContactNameLength    = 80
	maxContactEmailLength   = 200
	maxContactAddressLength = 300
)

// Contact is one broadcast contact as the API returns it. PhoneNumber is
// E.164 ("+" followed by 8-15 digits).
type Contact struct {
	ID          string  `json:"id" example:"7d2c1b0a-1e2f-4a3b-8c4d-5e6f7a8b9c0d"`
	BroadcastID string  `json:"broadcast_id" example:"5c1f6f1e-8f0e-4a51-9b7e-2a3b4c5d6e7f"`
	PhoneNumber string  `json:"phone_number" example:"+8801712345678"`
	FirstName   *string `json:"first_name" example:"Nadia"`
	LastName    *string `json:"last_name" example:"Rahman"`
	Email       *string `json:"email" example:"nadia@example.com"`
	Address     *string `json:"address" example:"House 12, Road 7, Dhanmondi, Dhaka"`
	// CallStatus is the auto-dialer state: idle (not queued), queued, calling,
	// answered, no_answer (rang, never picked up) or failed (could not be
	// placed; LastError says why). LastCallID is the most recent call.
	CallStatus string    `json:"call_status" example:"queued"`
	LastCallID *string   `json:"last_call_id" example:"9f45d8b4-f76f-43f4-8e69-96c7f9eac7f4"`
	LastError  *string   `json:"last_error" example:"could not place the call"`
	CreatedAt  time.Time `json:"created_at" example:"2026-11-23T10:00:00Z"`
}

// ContactInput is a validated contact ready to insert.
type ContactInput struct {
	PhoneNumber string
	FirstName   *string
	LastName    *string
	Email       *string
	Address     *string
}

// ErrContactNotFound is returned when the contact does not exist in that
// broadcast or belongs to someone else.
var ErrContactNotFound = errors.New("contact not found")

const contactColumns = `id, broadcast_id, phone_number, first_name, last_name, email, address, call_status, last_call_id, last_error, created_at`

func scanContact(row pgx.Row) (Contact, error) {
	var c Contact
	err := row.Scan(&c.ID, &c.BroadcastID, &c.PhoneNumber, &c.FirstName, &c.LastName, &c.Email, &c.Address, &c.CallStatus, &c.LastCallID, &c.LastError, &c.CreatedAt)
	return c, err
}

// Paging bounds of the contact list.
const (
	defaultContactsLimit   = 10
	maxContactsLimit       = 100
	maxContactSearchLength = 200
)

// contactSorts maps the API's sort keys to what they order by.
var contactSorts = map[string]string{
	"created": "created_at",
	"name":    "lower(COALESCE(NULLIF(concat_ws(' ', first_name, last_name), ''), phone_number))",
	"first":   "lower(COALESCE(first_name, ''))",
	"last":    "lower(COALESCE(last_name, ''))",
}

// ContactQuery is one validated page request.
type ContactQuery struct {
	Page   int
	Limit  int
	Search string
	Sort   string // a contactSorts key
	Desc   bool
}

// ContactStats counts the whole broadcast, whatever the page or search, so the
// dashboard can show dialer progress without loading every contact. Calling
// names the contact being called now, if any.
type ContactStats struct {
	Total       int     `json:"total" example:"120"`
	Idle        int     `json:"idle" example:"0"`
	Queued      int     `json:"queued" example:"40"`
	InCall      int     `json:"calling" example:"1"`
	Answered    int     `json:"answered" example:"60"`
	NoAnswer    int     `json:"no_answer" example:"15"`
	Failed      int     `json:"failed" example:"4"`
	WithEmail   int     `json:"with_email" example:"80"`
	WithAddress int     `json:"with_address" example:"75"`
	CallingName *string `json:"calling_name" example:"Nadia Rahman"`
}

// ContactPage is one page of contacts. Total counts the contacts matching the
// search; Stats covers the whole broadcast.
type ContactPage struct {
	Contacts []Contact    `json:"contacts"`
	Stats    ContactStats `json:"stats"`
	Total    int          `json:"-"`
}

// contactSearchSQL matches $3 (lower-cased, "" = everything) anywhere in the
// name, email, number or address. strpos, not LIKE, so % and _ are literal.
const contactSearchSQL = `($3 = '' OR strpos(lower(concat_ws(' ', first_name, last_name)), $3) > 0
	OR strpos(lower(COALESCE(email, '')), $3) > 0
	OR strpos(phone_number, $3) > 0
	OR strpos(lower(COALESCE(address, '')), $3) > 0)`

// ListContacts returns one page of a broadcast's contacts. The stats, the count
// and the page go to Postgres as one batch. ErrNotFound when the broadcast is
// not the user's.
func (r *Repository) ListContacts(ctx context.Context, userID, broadcastID string, q ContactQuery) (ContactPage, error) {
	page := ContactPage{Contacts: make([]Contact, 0, q.Limit)}
	sortBy, ok := contactSorts[q.Sort]
	if !ok {
		return page, fmt.Errorf("list broadcast contacts: unknown sort %q", q.Sort)
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	search := strings.ToLower(q.Search)

	batch := &pgx.Batch{}
	batch.Queue(`
		SELECT EXISTS (SELECT 1 FROM broadcasts WHERE id = $2 AND user_id = $1),
			count(*),
			count(*) FILTER (WHERE call_status = 'idle'),
			count(*) FILTER (WHERE call_status = 'queued'),
			count(*) FILTER (WHERE call_status = 'calling'),
			count(*) FILTER (WHERE call_status = 'answered'),
			count(*) FILTER (WHERE call_status = 'no_answer'),
			count(*) FILTER (WHERE call_status = 'failed'),
			count(*) FILTER (WHERE email <> ''),
			count(*) FILTER (WHERE address <> ''),
			min(COALESCE(NULLIF(concat_ws(' ', first_name, last_name), ''), phone_number)) FILTER (WHERE call_status = 'calling')
		FROM broadcast_contacts
		WHERE user_id = $1 AND broadcast_id = $2`, userID, broadcastID)
	batch.Queue(`
		SELECT count(*) FROM broadcast_contacts
		WHERE user_id = $1 AND broadcast_id = $2 AND `+contactSearchSQL, userID, broadcastID, search)
	batch.Queue(`
		SELECT `+contactColumns+`
		FROM broadcast_contacts
		WHERE user_id = $1 AND broadcast_id = $2 AND `+contactSearchSQL+`
		ORDER BY `+sortBy+` `+dir+`, queued_at `+dir+`, id `+dir+`
		LIMIT $4 OFFSET $5`, userID, broadcastID, search, q.Limit, (q.Page-1)*q.Limit)
	results := r.pool.SendBatch(ctx, batch)
	defer results.Close()

	var owned bool
	st := &page.Stats
	if err := results.QueryRow().Scan(&owned, &st.Total, &st.Idle, &st.Queued, &st.InCall, &st.Answered,
		&st.NoAnswer, &st.Failed, &st.WithEmail, &st.WithAddress, &st.CallingName); err != nil {
		return page, fmt.Errorf("broadcast contact stats: %w", err)
	}
	if !owned {
		return page, ErrNotFound
	}
	if err := results.QueryRow().Scan(&page.Total); err != nil {
		return page, fmt.Errorf("count broadcast contacts: %w", err)
	}
	rows, err := results.Query()
	if err != nil {
		return page, fmt.Errorf("list broadcast contacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return page, fmt.Errorf("scan broadcast contact: %w", err)
		}
		page.Contacts = append(page.Contacts, c)
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("list broadcast contacts: %w", err)
	}
	return page, nil
}

// CreateContact adds a contact to one of the user's broadcasts. The ownership
// check and the insert are one statement: a broadcast of someone else's
// inserts nothing and reads as ErrNotFound.
func (r *Repository) CreateContact(ctx context.Context, userID, broadcastID string, in ContactInput) (Contact, error) {
	c, err := scanContact(r.pool.QueryRow(ctx, `
		INSERT INTO broadcast_contacts (broadcast_id, user_id, phone_number, first_name, last_name, email, address)
		SELECT b.id, b.user_id, $3, $4, $5, $6, $7 FROM broadcasts b WHERE b.id = $2 AND b.user_id = $1
		RETURNING `+contactColumns,
		userID, broadcastID, in.PhoneNumber, in.FirstName, in.LastName, in.Email, in.Address))
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("create broadcast contact: %w", err)
	}
	return c, nil
}

// DeleteContact removes one contact from one of the user's broadcasts.
func (r *Repository) DeleteContact(ctx context.Context, userID, broadcastID, contactID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM broadcast_contacts WHERE user_id = $1 AND broadcast_id = $2 AND id = $3`,
		userID, broadcastID, contactID)
	if err != nil {
		return fmt.Errorf("delete broadcast contact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrContactNotFound
	}
	return nil
}

// NormalizePhoneNumber turns what a person typed into E.164. Spaces, dashes,
// dots and brackets are dropped, a leading "00" counts as "+", and a number
// typed without "+" is taken as already international. ok is false unless
// 8-15 digits remain.
func NormalizePhoneNumber(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', '(', ')', ' ':
			return -1
		}
		return r
	}, s)
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	} else {
		s = strings.TrimPrefix(s, "00")
	}
	if len(s) < 8 || len(s) > 15 || s[0] == '0' {
		return "", false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return "+" + s, true
}

type createContactRequest struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	Email       string `json:"email"`
	Address     string `json:"address"`
}

// optional trims a field and returns nil when it is empty, or a message when it
// is longer than max.
func optional(field, raw string, max int) (*string, string) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, ""
	}
	if utf8.RuneCountInString(v) > max {
		return nil, fmt.Sprintf("%s must be at most %d characters", field, max)
	}
	return &v, ""
}

func validateContact(req createContactRequest) (ContactInput, string) {
	var in ContactInput
	if strings.TrimSpace(req.PhoneNumber) == "" {
		return in, "phone_number is required"
	}
	phone, ok := NormalizePhoneNumber(req.PhoneNumber)
	if !ok {
		return in, "phone_number must be an international number with country code, for example +8801712345678"
	}
	in.PhoneNumber = phone
	var msg string
	if in.FirstName, msg = optional("first_name", req.FirstName, maxContactNameLength); msg != "" {
		return in, msg
	}
	if in.LastName, msg = optional("last_name", req.LastName, maxContactNameLength); msg != "" {
		return in, msg
	}
	if in.Email, msg = optional("email", req.Email, maxContactEmailLength); msg != "" {
		return in, msg
	}
	if in.Email != nil {
		at := strings.Index(*in.Email, "@")
		if at < 1 || at == len(*in.Email)-1 || strings.ContainsAny(*in.Email, " \t") {
			return in, "email is not valid"
		}
	}
	if in.Address, msg = optional("address", req.Address, maxContactAddressLength); msg != "" {
		return in, msg
	}
	return in, ""
}

func (c *Controller) writeContactError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "broadcast not found")
	case errors.Is(err, ErrContactNotFound):
		httpx.WriteError(w, http.StatusNotFound, "contact not found")
	default:
		httpx.WriteError(w, http.StatusInternalServerError, fallback)
	}
}

// parseContactQuery reads page, limit, search, sort and order. Absent parameters
// mean page 1, 10 per page, newest first; a bad one returns a message.
func parseContactQuery(values url.Values) (ContactQuery, string) {
	q := ContactQuery{Page: 1, Limit: defaultContactsLimit, Sort: "created", Desc: true}
	if raw := strings.TrimSpace(values.Get("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		// The upper bound keeps the offset far from overflowing.
		if err != nil || n < 1 || n > 1_000_000 {
			return q, "page must be a positive integer"
		}
		q.Page = n
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxContactsLimit {
			return q, fmt.Sprintf("limit must be between 1 and %d", maxContactsLimit)
		}
		q.Limit = n
	}
	q.Search = strings.TrimSpace(values.Get("search"))
	if utf8.RuneCountInString(q.Search) > maxContactSearchLength {
		return q, fmt.Sprintf("search must be at most %d characters", maxContactSearchLength)
	}
	if raw := values.Get("sort"); raw != "" {
		if _, ok := contactSorts[raw]; !ok {
			return q, "sort must be one of: created, name, first, last"
		}
		q.Sort = raw
	}
	switch values.Get("order") {
	case "", "desc":
	case "asc":
		q.Desc = false
	default:
		return q, "order must be asc or desc"
	}
	return q, ""
}

// contactListResponse is the list envelope: one page plus its pagination block.
type contactListResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    ContactPage `json:"data"`
	Meta    types.Meta  `json:"meta"`
}

// ListContacts handles GET /v1/dashboard/voice/broadcasts/{broadcast_id}/contacts
// ?page=1&limit=10&search=&sort=created|name|first|last&order=asc|desc.
func (c *Controller) ListContacts(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	q, msg := parseContactQuery(r.URL.Query())
	if msg != "" {
		httpx.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	page, err := c.repo.ListContacts(r.Context(), user.ID, chi.URLParam(r, "broadcast_id"), q)
	if err != nil {
		c.writeContactError(w, err, "failed to list contacts")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, contactListResponse{
		Success: true,
		Message: "Contacts retrieved successfully",
		Data:    page,
		Meta:    httpx.PaginationMeta(q.Page, q.Limit, page.Total),
	})
}

// CreateContact handles POST /v1/dashboard/voice/broadcasts/{broadcast_id}/contacts.
func (c *Controller) CreateContact(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	var req createContactRequest
	if !decode(w, r, &req) {
		return
	}
	in, msg := validateContact(req)
	if msg != "" {
		httpx.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	contact, err := c.repo.CreateContact(r.Context(), user.ID, chi.URLParam(r, "broadcast_id"), in)
	if err != nil {
		c.writeContactError(w, err, "failed to add contact")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, models.APIResponse{Success: true, Message: "Contact added successfully", Data: contact})
}

// DeleteContact handles DELETE /v1/dashboard/voice/broadcasts/{broadcast_id}/contacts/{contact_id}.
func (c *Controller) DeleteContact(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	err := c.repo.DeleteContact(r.Context(), user.ID, chi.URLParam(r, "broadcast_id"), chi.URLParam(r, "contact_id"))
	if err != nil {
		c.writeContactError(w, err, "failed to delete contact")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Contact deleted successfully"})
}

// MaxImportRows bounds one CSV import.
const MaxImportRows = 5000

// ImportProblem is one row of an import that was not added.
type ImportProblem struct {
	Row         int    `json:"row" example:"3"`
	PhoneNumber string `json:"phone_number" example:"12345"`
	Reason      string `json:"reason" example:"phone_number must be an international number with country code"`
}

// ImportResult reports what an import did.
type ImportResult struct {
	Added   int             `json:"added" example:"98"`
	Invalid []ImportProblem `json:"invalid"`
}

// ImportContacts adds many contacts at once, all queued for the dialer in the
// order given. A number already in the broadcast, or repeated in the file, is
// added again. ErrNotFound when the broadcast is not the user's.
func (r *Repository) ImportContacts(ctx context.Context, userID, broadcastID string, rows []ContactInput) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("import contacts: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	var owned bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM broadcasts WHERE id = $1 AND user_id = $2)`,
		broadcastID, userID).Scan(&owned); err != nil {
		return 0, fmt.Errorf("check broadcast: %w", err)
	}
	if !owned {
		return 0, ErrNotFound
	}

	batch := &pgx.Batch{}
	for _, in := range rows {
		// clock_timestamp() advances per statement, so the file order is the
		// call order.
		batch.Queue(`
			INSERT INTO broadcast_contacts (broadcast_id, user_id, phone_number, first_name, last_name, email, address, call_status, queued_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'queued', clock_timestamp())`,
			broadcastID, userID, in.PhoneNumber, in.FirstName, in.LastName, in.Email, in.Address)
	}
	results := tx.SendBatch(ctx, batch)
	added := 0
	for range rows {
		tag, err := results.Exec()
		if err != nil {
			results.Close()
			return 0, fmt.Errorf("import contacts: %w", err)
		}
		added += int(tag.RowsAffected())
	}
	if err := results.Close(); err != nil {
		return 0, fmt.Errorf("import contacts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("import contacts: %w", err)
	}
	return added, nil
}

type importRequest struct {
	Contacts []createContactRequest `json:"contacts"`
}

// ImportContacts handles POST /v1/dashboard/voice/broadcasts/{broadcast_id}/contacts/import.
// Rows are validated one by one: bad rows are reported and skipped, the rest
// are added and queued for calling.
func (c *Controller) ImportContacts(w http.ResponseWriter, r *http.Request) {
	user, ok := httpx.CurrentUser(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Contacts) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "contacts is empty")
		return
	}
	if len(req.Contacts) > MaxImportRows {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("at most %d contacts per import", MaxImportRows))
		return
	}

	result := ImportResult{Invalid: make([]ImportProblem, 0)}
	valid := make([]ContactInput, 0, len(req.Contacts))
	for i, row := range req.Contacts {
		in, msg := validateContact(row)
		if msg != "" {
			result.Invalid = append(result.Invalid, ImportProblem{Row: i + 1, PhoneNumber: strings.TrimSpace(row.PhoneNumber), Reason: msg})
			continue
		}
		valid = append(valid, in)
	}
	if len(valid) > 0 {
		added, err := c.repo.ImportContacts(r.Context(), user.ID, chi.URLParam(r, "broadcast_id"), valid)
		if err != nil {
			c.writeContactError(w, err, "failed to import contacts")
			return
		}
		result.Added = added
	}
	httpx.WriteJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Contacts imported successfully", Data: result})
}
