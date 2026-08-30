// Package chatconversations persists the WhatsApp text threads a chat agent
// handles into the chat_conversations and chat_messages tables: a thread is
// opened the first time a contact writes to a number, and every exchange after
// that is appended to it as two turns — the message that arrived and the reply
// that was sent.
//
// It is the readable counterpart of the in-process history the chat runtime
// keeps for model context: that history is a window, this is the record.
package chatconversations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrNotFound is returned when a conversation does not exist or belongs to
// another user.
var ErrNotFound = errors.New("conversation not found")

// peerPhoneColumn resolves the contact's number for display. A message often
// arrives carrying only the sender's LID — an opaque WhatsApp identifier — so
// the thread is opened with a "…@lid" peer and no phone at all. The LID→phone
// mapping (whatsmeow_lid_map) is frequently learned later, so it is applied at
// read time rather than frozen into the row when the thread was written: when
// the stored phone is missing and a mapping now exists, the number is recovered.
// The "@lid" server and any device suffix (":NN") are stripped before the
// lookup, matching how the mapping keys on the bare user number.
//
// This is the same treatment calls.peer gets, for the same reason.
const peerPhoneColumn = `
	COALESCE(
		chat_conversations.peer_phone,
		CASE WHEN chat_conversations.peer_jid LIKE '%@lid' THEN (
			SELECT '+' || m.pn
			  FROM whatsmeow_lid_map m
			 WHERE m.lid = split_part(replace(chat_conversations.peer_jid, '@lid', ''), ':', 1)
		) END
	) AS peer_phone
`

// conversationColumns is the full column list of the chat_conversations table,
// in the order scanConversation reads them. Shared by every read query so a
// schema change is made in one place. peer_phone is a resolving expression (see
// peerPhoneColumn), so the value scanned back is the contact's number whenever
// it can be recovered.
const conversationColumns = `
	chat_conversations.id, chat_conversations.user_id, chat_conversations.phone_number_id, chat_conversations.chat_agent_id,
	chat_conversations.peer_jid,` + peerPhoneColumn + `, chat_conversations.peer_name, chat_conversations.status,
	chat_conversations.message_count, chat_conversations.last_message_role, chat_conversations.last_message_at,
	chat_conversations.created_at, chat_conversations.updated_at
`

// Repository owns persistence for the chat_conversations and chat_messages
// tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a chat conversations repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Filter narrows a conversation listing. Every field is optional; an empty
// filter lists all of the user's threads.
type Filter struct {
	ChatAgentID   string
	PhoneNumberID string
	Status        string
	// Search matches the contact's JID, phone or name, case-insensitively.
	Search string
}

// RecordTurn saves one exchange, opening the thread if this is the first
// message on it. The conversation is found by (phone_number_id, peer_jid), so a
// contact keeps one thread with a number however often the agent answering it
// changes; chat_agent_id is refreshed to whichever agent replied.
//
// A turn whose WAMessageID is already on the thread is ignored, which is what
// keeps a WhatsApp redelivery out of the transcript. An empty AssistantMessage
// records only the inbound turn: the message did arrive, and the fact that
// nothing was said back is part of the history.
//
// Both messages are written in one transaction, so a thread can never hold a
// reply without the message it answers.
func (r *Repository) RecordTurn(ctx context.Context, turn models.NewChatTurn) error {
	if strings.TrimSpace(turn.PhoneNumberID) == "" {
		return fmt.Errorf("record chat turn: phone number id is required")
	}
	if strings.TrimSpace(turn.PeerJID) == "" {
		return fmt.Errorf("record chat turn: peer jid is required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin record chat turn: %w", err)
	}
	defer tx.Rollback(ctx)

	const upsert = `
		INSERT INTO chat_conversations (user_id, phone_number_id, chat_agent_id, peer_jid, peer_phone, peer_name)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (phone_number_id, peer_jid) WHERE phone_number_id IS NOT NULL
		DO UPDATE SET
			chat_agent_id = COALESCE(EXCLUDED.chat_agent_id, chat_conversations.chat_agent_id),
			peer_phone = COALESCE(EXCLUDED.peer_phone, chat_conversations.peer_phone),
			peer_name = COALESCE(EXCLUDED.peer_name, chat_conversations.peer_name),
			updated_at = now()
		RETURNING id`

	var conversationID string
	err = tx.QueryRow(ctx, upsert,
		turn.UserID,
		turn.PhoneNumberID,
		nullIfEmpty(turn.ChatAgentID),
		turn.PeerJID,
		nullIfEmpty(turn.PeerPhone),
		nullIfEmpty(turn.PeerName),
	).Scan(&conversationID)
	if err != nil {
		return fmt.Errorf("upsert chat conversation: %w", err)
	}

	// A redelivered message is answered from the same history and would be
	// saved as a second identical turn, so the whole exchange is skipped once
	// its inbound message is already on the thread.
	if id := strings.TrimSpace(turn.WAMessageID); id != "" {
		const seen = `SELECT EXISTS (SELECT 1 FROM chat_messages WHERE conversation_id = $1 AND wa_message_id = $2)`
		var exists bool
		if err := tx.QueryRow(ctx, seen, conversationID, id).Scan(&exists); err != nil {
			return fmt.Errorf("check recorded chat message: %w", err)
		}
		if exists {
			return nil
		}
	}

	if err := appendMessage(ctx, tx, conversationID, models.ChatRoleUser, turn.UserMessage, turn.WAMessageID); err != nil {
		return err
	}
	if strings.TrimSpace(turn.AssistantMessage) != "" {
		if err := appendMessage(ctx, tx, conversationID, models.ChatRoleAssistant, turn.AssistantMessage, ""); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit record chat turn: %w", err)
	}
	return nil
}

// appendMessage writes one turn at the end of the thread. The sequence number
// is derived inside the statement rather than read out first, so two goroutines
// appending to one thread cannot quietly pick the same seq — the unique
// (conversation_id, seq) index rejects the loser instead.
func appendMessage(ctx context.Context, tx pgx.Tx, conversationID, role, content, waMessageID string) error {
	const q = `
		INSERT INTO chat_messages (conversation_id, seq, role, content, wa_message_id)
		SELECT $1, COALESCE(MAX(seq) + 1, 0), $2, $3, $4
		FROM chat_messages
		WHERE conversation_id = $1`
	if _, err := tx.Exec(ctx, q, conversationID, role, content, nullIfEmpty(waMessageID)); err != nil {
		return fmt.Errorf("append chat message: %w", err)
	}
	return nil
}

// AppendTurn adds one message to the end of an existing thread and returns it.
// It is how a message sent from the dashboard joins the transcript: the person
// answering by hand is speaking for the agent, so it is stored with the same
// "assistant" role the agent's own replies use and reads back in order beside
// them.
//
// conversationID is the chat_conversations row id; ownership is expected to have
// been verified (via GetByUser) before calling this. The thread's counters and
// last-message columns follow from the insert's trigger, as they do for a turn
// the runtime saved.
func (r *Repository) AppendTurn(ctx context.Context, conversationID, role, content string) (models.ChatMessage, error) {
	const q = `
		INSERT INTO chat_messages (conversation_id, seq, role, content)
		SELECT $1, COALESCE(MAX(seq) + 1, 0), $2, $3
		FROM chat_messages
		WHERE conversation_id = $1
		RETURNING seq, role, content, created_at`

	var message models.ChatMessage
	err := r.pool.QueryRow(ctx, q, conversationID, role, content).
		Scan(&message.Seq, &message.Role, &message.Content, &message.CreatedAt)
	if err != nil {
		return models.ChatMessage{}, fmt.Errorf("append chat turn: %w", err)
	}
	return message, nil
}

// ListByUser returns one page of the authenticated user's threads, most
// recently active first, together with the total number matching the filter so
// the caller can build pagination metadata. A thread that has somehow never
// been written to sorts last rather than first, which is what NULLS LAST buys.
//
// The returned slice is always non-nil so an empty page serializes as [] rather
// than null.
func (r *Repository) ListByUser(ctx context.Context, userID string, filter Filter, limit, offset int) ([]models.ChatConversation, int, error) {
	where, args := filterClause(userID, filter)

	var total int
	countQuery := "SELECT count(*) FROM chat_conversations WHERE " + where
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count chat conversations: %w", err)
	}

	out := make([]models.ChatConversation, 0)
	if total == 0 {
		return out, 0, nil
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM chat_conversations
		WHERE %s
		ORDER BY last_message_at DESC NULLS LAST, created_at DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, conversationColumns, where, len(args)+1, len(args)+2)

	rows, err := r.pool.Query(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list chat conversations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan chat conversation: %w", err)
		}
		out = append(out, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate chat conversations: %w", err)
	}
	return out, total, nil
}

// filterClause builds the shared WHERE for the listing and its count, so the
// page and the total can never be computed over different rows.
func filterClause(userID string, filter Filter) (string, []any) {
	clauses := []string{"user_id = $1"}
	args := []any{userID}

	if id := strings.TrimSpace(filter.ChatAgentID); id != "" {
		args = append(args, id)
		clauses = append(clauses, fmt.Sprintf("chat_agent_id = $%d", len(args)))
	}
	if id := strings.TrimSpace(filter.PhoneNumberID); id != "" {
		args = append(args, id)
		clauses = append(clauses, fmt.Sprintf("phone_number_id = $%d", len(args)))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	// One placeholder answers for all three columns: the contact is searched by
	// whichever of them the person typing happens to know.
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+search+"%")
		n := len(args)
		clauses = append(clauses, fmt.Sprintf(
			"(peer_jid ILIKE $%d OR coalesce(peer_phone, '') ILIKE $%d OR coalesce(peer_name, '') ILIKE $%d)",
			n, n, n,
		))
	}
	return strings.Join(clauses, " AND "), args
}

// GetByUser loads one thread by id, scoped to the authenticated user. It
// returns ErrNotFound when no such thread exists for that user.
func (r *Repository) GetByUser(ctx context.Context, userID, id string) (models.ChatConversation, error) {
	query := fmt.Sprintf(`SELECT %s FROM chat_conversations WHERE id = $1 AND user_id = $2`, conversationColumns)

	conversation, err := scanConversation(r.pool.QueryRow(ctx, query, id, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.ChatConversation{}, ErrNotFound
		}
		return models.ChatConversation{}, fmt.Errorf("get chat conversation: %w", err)
	}
	return conversation, nil
}

// ListMessages returns a thread's saved transcript, ordered by turn.
// conversationID is the chat_conversations row id; ownership is expected to have
// been verified (via GetByUser) before calling this.
func (r *Repository) ListMessages(ctx context.Context, conversationID string) ([]models.ChatMessage, error) {
	const query = `
		SELECT seq, role, content, created_at
		FROM chat_messages
		WHERE conversation_id = $1
		ORDER BY seq ASC`

	rows, err := r.pool.Query(ctx, query, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list chat messages: %w", err)
	}
	defer rows.Close()

	out := make([]models.ChatMessage, 0)
	for rows.Next() {
		var m models.ChatMessage
		if err := rows.Scan(&m.Seq, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chat message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat messages: %w", err)
	}
	return out, nil
}

// UpdateStatusByUser moves one thread between "open" and "closed". Closing is
// bookkeeping for the person reading the inbox; it does not stop the agent
// answering, which is what pausing the agent is for. It returns ErrNotFound when
// no such thread exists for that user.
func (r *Repository) UpdateStatusByUser(ctx context.Context, userID, id, status string) (models.ChatConversation, error) {
	query := fmt.Sprintf(`
		UPDATE chat_conversations
		SET status = $3, updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING %s
	`, conversationColumns)

	conversation, err := scanConversation(r.pool.QueryRow(ctx, query, id, userID, status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.ChatConversation{}, ErrNotFound
		}
		return models.ChatConversation{}, fmt.Errorf("update chat conversation: %w", err)
	}
	return conversation, nil
}

// DeleteByUser permanently removes one thread owned by the authenticated user.
// Its transcript goes with it (chat_messages cascades on delete). It returns
// ErrNotFound when no such thread exists for that user.
func (r *Repository) DeleteByUser(ctx context.Context, userID, id string) error {
	const query = `DELETE FROM chat_conversations WHERE id = $1 AND user_id = $2`
	tag, err := r.pool.Exec(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("delete chat conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanConversation reads one chat_conversations row in the column order of
// conversationColumns.
func scanConversation(row rowScanner) (models.ChatConversation, error) {
	var c models.ChatConversation
	if err := row.Scan(
		&c.ID,
		&c.UserID,
		&c.PhoneNumberID,
		&c.ChatAgentID,
		&c.PeerJID,
		&c.PeerPhone,
		&c.PeerName,
		&c.Status,
		&c.MessageCount,
		&c.LastMessageRole,
		&c.LastMessageAt,
		&c.CreatedAt,
		&c.UpdatedAt,
	); err != nil {
		return models.ChatConversation{}, err
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
