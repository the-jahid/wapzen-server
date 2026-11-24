// Package calls persists the lifecycle of voice calls the server handles into
// the calls table: a call is inserted in the "received" state when it starts —
// an inbound call when it is first offered, an outbound one when its offer is
// placed — then advanced to "answered", "ended", "declined", or "failed" as the
// call progresses. It backs voicecall.CallStore.
package calls

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/db"
	"whatsapp-ai-caller-server/internal/models"
)

// ErrNotFound is returned when a call does not exist or is owned by another user.
var ErrNotFound = errors.New("call not found")

// peerColumn resolves the stored peer for display. Inbound offers frequently
// arrive carrying only the caller's LID — an opaque WhatsApp identifier — so peer
// is persisted as a "…@lid" JID. The LID→phone mapping (whatsmeow_lid_map) is
// often learned only after the call, so we resolve it at read time instead of
// freezing whatever was known when the row was written: when peer is a LID and a
// mapping now exists, return the caller's phone JID ("…@s.whatsapp.net");
// otherwise return the stored value unchanged. The "@lid" server and any device
// suffix (":NN") are stripped before the lookup, matching how the mapping keys on
// the bare user number.
const peerColumn = `
	CASE
		WHEN calls.peer LIKE '%@lid' THEN COALESCE(
			(SELECT m.pn || '@s.whatsapp.net'
			   FROM whatsmeow_lid_map m
			  WHERE m.lid = split_part(replace(calls.peer, '@lid', ''), ':', 1)),
			calls.peer
		)
		ELSE calls.peer
	END AS peer
`

// callColumns is the full column list of the calls table, in the order
// scanCall reads them. Shared by every read/return query so a schema change is
// made in one place. peer is a resolving expression (see peerColumn), so the
// value scanned back is the caller's phone JID when it can be recovered.
const callColumns = `
	id, call_id, user_id, phone_number_id, agent_id,` + peerColumn + `, call_type,
	status, end_reason, duration_seconds, answered_at, ended_at,
	created_at, updated_at, summary
`

// Repository owns persistence for the calls table.
type Repository struct {
	pool *pgxpool.Pool
	// onTranscript, when set, runs after a transcript is saved (used to write
	// the call summary). Set once at startup via OnTranscriptSaved.
	onTranscript func(callID string, messages []models.CallMessage)
}

// OnTranscriptSaved registers fn to run after every successful SaveTranscript.
// fn must not block: it is called on the call's teardown path.
func (r *Repository) OnTranscriptSaved(fn func(callID string, messages []models.CallMessage)) {
	r.onTranscript = fn
}

// SetSummary stores the call's summary. callID is the calls-table row id.
func (r *Repository) SetSummary(ctx context.Context, callID, summary string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE calls SET summary = $2, updated_at = now() WHERE id = $1`, callID, summary); err != nil {
		return fmt.Errorf("set call summary: %w", err)
	}
	return nil
}

// NewRepository creates a calls repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert records a call that has just started — an inbound offer being answered
// or an outbound offer just placed — in the "received" state, and returns its
// generated row id. "received" is the shared not-yet-connected state for both
// directions; the call advances to "answered" once media flows. PhoneNumberID
// and AgentID are stored as NULL when empty so call history survives the number
// or agent being deleted later,.
func (r *Repository) Insert(ctx context.Context, params models.NewCall) (string, error) {
	const q = `
		INSERT INTO calls (call_id, user_id, phone_number_id, agent_id, peer, call_type, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'received')
		RETURNING id`

	var id string
	err := r.pool.QueryRow(ctx, q,
		params.CallID,
		params.UserID,
		nullIfEmpty(params.PhoneNumberID),
		nullIfEmpty(params.AgentID),
		params.Peer,
		params.CallType,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert call: %w", err)
	}
	return id, nil
}

// MarkAnswered moves the call to "answered" and stamps answered_at.
func (r *Repository) MarkAnswered(ctx context.Context, id string) error {
	const q = `
		UPDATE calls
		SET status = 'answered', answered_at = now(), updated_at = now()
		WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("mark call answered: %w", err)
	}
	return nil
}

// MarkEnded moves the call to "ended", stamps ended_at, records the connected
// duration in whole seconds (answered_at → now), and the hangup reason (NULL when
// empty). duration_seconds stays NULL when the call was never answered. ended_at
// and duration_seconds share the same now() so they can never disagree.
//
// The same statement adds the duration to the owner's voice minutes
// (voice_usage.used_seconds, see the voiceusage package), so a call can never be
// ended without being counted, and a call already ended is not counted twice.
func (r *Repository) MarkEnded(ctx context.Context, id, reason string) error {
	const q = `
		WITH ended AS (
			UPDATE calls
			SET status = 'ended',
				ended_at = now(),
				duration_seconds = CASE
					WHEN answered_at IS NULL THEN NULL
					ELSE GREATEST(0, EXTRACT(EPOCH FROM (now() - answered_at))::int)
				END,
				end_reason = $2,
				updated_at = now()
			WHERE id = $1 AND status <> 'ended'
			RETURNING user_id, duration_seconds
		)
		INSERT INTO voice_usage (user_id, used_seconds)
		SELECT user_id, duration_seconds FROM ended WHERE duration_seconds > 0
		ON CONFLICT (user_id) DO UPDATE
		SET used_seconds = voice_usage.used_seconds + EXCLUDED.used_seconds,
			updated_at = now()`
	if _, err := r.pool.Exec(ctx, q, id, nullIfEmpty(reason)); err != nil {
		return fmt.Errorf("mark call ended: %w", err)
	}
	return nil
}

// MarkDeclined moves the call to "declined" — the callee cut it while it was
// still ringing, so it never connected — stamps ended_at, and records the reason
// (NULL when empty). Unlike MarkEnded there is no duration to compute: a declined
// call has no answered_at, so duration_seconds stays NULL.
func (r *Repository) MarkDeclined(ctx context.Context, id, reason string) error {
	const q = `
		UPDATE calls
		SET status = 'declined', ended_at = now(), end_reason = $2, updated_at = now()
		WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, id, nullIfEmpty(reason)); err != nil {
		return fmt.Errorf("mark call declined: %w", err)
	}
	return nil
}

// MarkFailed moves the call to "failed" (it could not be answered), stamps
// ended_at, and records the reason (NULL when empty).
func (r *Repository) MarkFailed(ctx context.Context, id, reason string) error {
	const q = `
		UPDATE calls
		SET status = 'failed', ended_at = now(), end_reason = $2, updated_at = now()
		WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, id, nullIfEmpty(reason)); err != nil {
		return fmt.Errorf("mark call failed: %w", err)
	}
	return nil
}

// ListByUser returns one page of the authenticated user's calls, newest first,
// together with the total number of calls that user has so the caller can build
// pagination metadata. limit and offset page the result; callers are expected to
// clamp them to sane bounds. The returned slice is always non-nil so an empty
// page serializes as [] rather than null.
//
// search, when not empty, narrows the list the way the dashboard's search box
// reads: part of the peer (its digits alone also match, so "01701 750469" finds
// 8801701750469@s.whatsapp.net), or the start of the status or call type.
func (r *Repository) ListByUser(ctx context.Context, userID, search string, limit, offset int) ([]models.Call, int, error) {
	where := "user_id = $1"
	args := []any{userID}
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + escapeLike(search) + "%"
		prefix := escapeLike(strings.ToLower(search)) + "%"
		digits := onlyDigits(search)
		args = append(args, like, prefix)
		cond := "peer ILIKE $2 OR lower(status) LIKE $3 OR lower(call_type) LIKE $3"
		if digits != "" {
			args = append(args, "%"+digits+"%")
			cond += " OR regexp_replace(split_part(peer, '@', 1), '[^0-9]', '', 'g') LIKE $4"
		}
		where += " AND (" + cond + ")"
	}

	n := len(args)
	query := fmt.Sprintf(`
		SELECT %s
		FROM calls
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, callColumns, where, n+1, n+2)

	out := make([]models.Call, 0)
	total, err := db.ListPage(ctx, r.pool,
		"SELECT count(*) FROM calls WHERE "+where, args,
		query, append(append([]any{}, args...), limit, offset),
		func(rows pgx.Rows) error {
			call, err := scanCall(rows)
			if err != nil {
				return fmt.Errorf("scan call: %w", err)
			}
			out = append(out, call)
			return nil
		},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list calls: %w", err)
	}
	return out, total, nil
}

// GetByUser loads one call by id, scoped to the authenticated user. It returns
// ErrNotFound when no such call exists for that user.
func (r *Repository) GetByUser(ctx context.Context, userID, id string) (models.Call, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM calls
		WHERE id = $1 AND user_id = $2
	`, callColumns)

	call, err := scanCall(r.pool.QueryRow(ctx, query, id, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Call{}, ErrNotFound
		}
		return models.Call{}, fmt.Errorf("get call: %w", err)
	}
	return call, nil
}

// ListMessages returns a call's saved conversation transcript, ordered by turn.
// callID is the calls-table row id; ownership is expected to have been verified
// (e.g. via GetByUser) before calling this.
func (r *Repository) ListMessages(ctx context.Context, callID string) ([]models.CallMessage, error) {
	const query = `
		SELECT role, content
		FROM call_messages
		WHERE call_id = $1
		ORDER BY seq ASC
	`

	rows, err := r.pool.Query(ctx, query, callID)
	if err != nil {
		return nil, fmt.Errorf("list call messages: %w", err)
	}
	defer rows.Close()

	out := make([]models.CallMessage, 0)
	for rows.Next() {
		var m models.CallMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, fmt.Errorf("scan call message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate call messages: %w", err)
	}
	return out, nil
}

// DeleteByUser permanently removes one call owned by the authenticated user. Its
// transcript rows are removed with it (call_messages cascades on delete). It
// returns ErrNotFound when no such call exists for that user.
func (r *Repository) DeleteByUser(ctx context.Context, userID, id string) error {
	const query = `DELETE FROM calls WHERE id = $1 AND user_id = $2`
	tag, err := r.pool.Exec(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("delete call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateByUser applies a partial update to one call owned by the authenticated
// user. Only non-nil fields are changed: status is written as given (validate it
// against models.IsValidCallStatus first), and endReason is stored as NULL when
// blank. At least one field must be supplied. It returns ErrNotFound when no such
// call exists for that user.
func (r *Repository) UpdateByUser(ctx context.Context, userID, id string, status, endReason *string) (models.Call, error) {
	set := []string{"updated_at = now()"}
	args := []any{id, userID}
	next := 3
	if status != nil {
		set = append(set, fmt.Sprintf("status = $%d", next))
		args = append(args, *status)
		next++
	}
	if endReason != nil {
		set = append(set, fmt.Sprintf("end_reason = $%d", next))
		args = append(args, nullIfEmpty(*endReason))
		next++
	}

	query := fmt.Sprintf(`
		UPDATE calls
		SET %s
		WHERE id = $1 AND user_id = $2
		RETURNING %s
	`, strings.Join(set, ", "), callColumns)

	call, err := scanCall(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Call{}, ErrNotFound
		}
		return models.Call{}, fmt.Errorf("update call: %w", err)
	}
	return call, nil
}

// SaveTranscript persists a call's conversation into call_messages, one row per
// turn. The message's index in the slice becomes its seq, so retrieving the rows
// ordered by seq replays the conversation exactly as it happened. Rows are bulk
// inserted with COPY in a single round trip; id and created_at fall to their
// column defaults. It returns the number of rows written. An empty transcript
// writes nothing and is not an error.
func (r *Repository) SaveTranscript(ctx context.Context, callID string, messages []models.CallMessage) (int, error) {
	if len(messages) == 0 {
		return 0, nil
	}

	rows := make([][]any, 0, len(messages))
	for seq, m := range messages {
		rows = append(rows, []any{callID, seq, m.Role, m.Content})
	}

	inserted, err := r.pool.CopyFrom(ctx,
		pgx.Identifier{"call_messages"},
		[]string{"call_id", "seq", "role", "content"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return 0, fmt.Errorf("save call transcript: %w", err)
	}
	if r.onTranscript != nil {
		r.onTranscript(callID, messages)
	}
	return int(inserted), nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanCall reads one calls row in the column order of callColumns.
func scanCall(row rowScanner) (models.Call, error) {
	var c models.Call
	if err := row.Scan(
		&c.ID,
		&c.CallID,
		&c.UserID,
		&c.PhoneNumberID,
		&c.AgentID,
		&c.Peer,
		&c.CallType,
		&c.Status,
		&c.EndReason,
		&c.DurationSeconds,
		&c.AnsweredAt,
		&c.EndedAt,
		&c.CreatedAt,
		&c.UpdatedAt,
		&c.Summary,
	); err != nil {
		return models.Call{}, err
	}
	return c, nil
}

// nullIfEmpty maps a blank string to a SQL NULL so optional columns stay unset
// rather than storing an empty string.
func nullIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// escapeLike makes a search term match literally inside LIKE, where % and _
// are wildcards and \ is the default escape character.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// onlyDigits keeps the digits of a search term, so a number typed with spaces,
// dashes or a leading + still matches a stored peer.
func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// BroadcastContactOwned reports whether contactID is one of the user's
// broadcast contacts, and returns its E.164 phone number.
func (r *Repository) BroadcastContactOwned(ctx context.Context, userID, contactID string) (string, bool, error) {
	var phone string
	err := r.pool.QueryRow(ctx,
		`SELECT phone_number FROM broadcast_contacts WHERE id = $1 AND user_id = $2`,
		contactID, userID).Scan(&phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("check broadcast contact: %w", err)
	}
	return phone, true, nil
}

// LinkBroadcastContact records that the call was placed from a broadcast
// contact, so the contact's panel lists it, and marks the contact as being
// called (taking it out of the auto-dialer's queue; the dialer settles it to
// answered / no_answer when the call ends). Both must be the user's.
func (r *Repository) LinkBroadcastContact(ctx context.Context, userID, callID, contactID string) error {
	_, err := r.pool.Exec(ctx, `
		WITH linked AS (
			UPDATE calls SET broadcast_contact_id = $3
			WHERE id = $1 AND user_id = $2
			  AND EXISTS (SELECT 1 FROM broadcast_contacts WHERE id = $3 AND user_id = $2)
			RETURNING id
		)
		UPDATE broadcast_contacts
		SET call_status = 'calling', last_call_id = (SELECT id FROM linked), last_error = NULL, updated_at = now()
		WHERE id = $3 AND user_id = $2 AND EXISTS (SELECT 1 FROM linked)`,
		callID, userID, contactID)
	if err != nil {
		return fmt.Errorf("link call to broadcast contact: %w", err)
	}
	return nil
}

// ListByBroadcastContact returns the calls placed from one broadcast contact,
// newest first, with the total.
func (r *Repository) ListByBroadcastContact(ctx context.Context, userID, contactID string, limit, offset int) ([]models.Call, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM calls WHERE user_id = $1 AND broadcast_contact_id = $2`,
		userID, contactID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count broadcast contact calls: %w", err)
	}
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM calls
		WHERE user_id = $1 AND broadcast_contact_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3 OFFSET $4`, callColumns), userID, contactID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list broadcast contact calls: %w", err)
	}
	defer rows.Close()
	out := make([]models.Call, 0)
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan call: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list broadcast contact calls: %w", err)
	}
	return out, total, nil
}
