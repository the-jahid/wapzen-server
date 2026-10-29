package chatagents

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Account limits for chat agents. They are enforced here and never advertised:
// the dashboard learns about one only when it has been reached.
const (
	// MaxChatAgentsPerUser is how many chat agents one account may own.
	MaxChatAgentsPerUser = 2
	// MaxRepliesPerAgent is how many WhatsApp replies one chat agent may send
	// over its lifetime.
	MaxRepliesPerAgent = 200
	// MaxRepliesPerNumber is how many chat-agent replies one WhatsApp number may
	// send over its lifetime, whichever agent wrote them.
	MaxRepliesPerNumber = 200
)

// ErrChatAgentLimit is returned when creating a chat agent would take the
// account past MaxChatAgentsPerUser.
var ErrChatAgentLimit = errors.New("you have reached the maximum number of chat agents for your account")

// phoneKeySQL is the key a number's reply count is kept under: the number
// itself, so relinking it (a new phone_numbers row) does not reset the count.
const phoneKeySQL = `COALESCE(p.phone_number, p.id)`

// replyLimitReachedSQL is the resource's reply_limit_reached column: true once
// the agent, or the number it is assigned to, has used its replies.
var replyLimitReachedSQL = `(chat_agents.ai_reply_count >= ` + strconv.Itoa(MaxRepliesPerAgent) + ` OR EXISTS (
		SELECT 1 FROM phone_numbers p
		JOIN chat_reply_usage u ON u.phone_key = ` + phoneKeySQL + `
		WHERE p.id = chat_agents.phone_number_id AND u.ai_reply_count >= ` + strconv.Itoa(MaxRepliesPerNumber) + `))`

// ensureCanCreate refuses a new chat agent once the account owns the maximum.
// The user row is locked first so two concurrent creates cannot both see room
// for one more.
func ensureCanCreate(ctx context.Context, tx pgx.Tx, userID string) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
		return fmt.Errorf("lock user for chat agent create: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat_agents WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return fmt.Errorf("count chat agents: %w", err)
	}
	if count >= MaxChatAgentsPerUser {
		return ErrChatAgentLimit
	}
	return nil
}

// ReserveReply takes one reply from both the agent's and the number's
// allowance before the model is asked, so concurrent messages cannot overshoot
// either limit. It reports false, and takes nothing, when either is used up.
// A reply that then fails to go out is handed back with ReleaseReply.
func (r *Repository) ReserveReply(ctx context.Context, agentID, phoneNumberID string) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin reserve reply: %w", err)
	}
	defer tx.Rollback(ctx)

	command, err := tx.Exec(ctx,
		`UPDATE chat_agents SET ai_reply_count = ai_reply_count + 1 WHERE id=$1 AND ai_reply_count < $2`,
		agentID, MaxRepliesPerAgent)
	if err != nil {
		return false, fmt.Errorf("reserve agent reply: %w", err)
	}
	if command.RowsAffected() == 0 {
		return false, nil
	}

	var reserved bool
	err = tx.QueryRow(ctx, `
		INSERT INTO chat_reply_usage (phone_key, ai_reply_count)
		SELECT `+phoneKeySQL+`, 1 FROM phone_numbers p WHERE p.id=$1
		ON CONFLICT (phone_key) DO UPDATE
			SET ai_reply_count = chat_reply_usage.ai_reply_count + 1, updated_at = now()
			WHERE chat_reply_usage.ai_reply_count < $2
		RETURNING true`, phoneNumberID, MaxRepliesPerNumber).Scan(&reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reserve number reply: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reserve reply: %w", err)
	}
	return true, nil
}

// ReleaseReply hands back a reply taken by ReserveReply that was never sent.
func (r *Repository) ReleaseReply(ctx context.Context, agentID, phoneNumberID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin release reply: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE chat_agents SET ai_reply_count = GREATEST(ai_reply_count - 1, 0) WHERE id=$1`, agentID); err != nil {
		return fmt.Errorf("release agent reply: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE chat_reply_usage u SET ai_reply_count = GREATEST(u.ai_reply_count - 1, 0), updated_at = now()
		FROM phone_numbers p
		WHERE p.id=$1 AND u.phone_key = `+phoneKeySQL, phoneNumberID); err != nil {
		return fmt.Errorf("release number reply: %w", err)
	}
	return tx.Commit(ctx)
}
