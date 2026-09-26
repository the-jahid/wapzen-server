package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// resolvePhoneNumberAssignment checks, inside the caller's transaction, that the
// agent identified by agentID (empty during Create, since the row does not yet
// exist) may hold phoneNumberID: the number must be the user's own, and no other
// agent may already hold it. A nil phoneNumberID clears the assignment and needs
// no check.
func resolvePhoneNumberAssignment(ctx context.Context, tx pgx.Tx, userID, agentID string, phoneNumberID *string) error {
	if phoneNumberID == nil {
		return nil
	}

	// Lock the phone number row so concurrent assignments to the same number
	// serialize, and confirm it belongs to this user (which also guarantees
	// every other agent referencing it is owned by the same user).
	var lockedID string
	err := tx.QueryRow(ctx,
		`SELECT id FROM phone_numbers WHERE id = $1 AND user_id = $2 FOR UPDATE`,
		*phoneNumberID, userID,
	).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPhoneNumberNotFound
	}
	if err != nil {
		return fmt.Errorf("lock phone number: %w", err)
	}

	const conflictQuery = `
		SELECT id
		FROM agents
		WHERE phone_number_id = $1
			AND ($2 = '' OR id <> $2)
		LIMIT 1`

	var conflictID string
	err = tx.QueryRow(ctx, conflictQuery, *phoneNumberID, agentID).Scan(&conflictID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check phone number assignment: %w", err)
	}
	return ErrPhoneNumberAssignmentConflict
}

// LookupPhoneNumberForAgent returns the agent's assigned phone_number_id, and
// whether the agent exists at all for userID.
//
// The two answers are separate because they mean different things to a caller
// attaching an agent to something: an unknown agent is a 404, while a known one
// with no number is a validation error the owner fixes by assigning a number.
// Reading them off a single nil would collapse both into the same case.
func (r *Repository) LookupPhoneNumberForAgent(ctx context.Context, userID, agentID string) (*string, bool, error) {
	const q = `SELECT phone_number_id FROM agents WHERE id = $1 AND user_id = $2`
	var phoneNumberID *string
	err := r.pool.QueryRow(ctx, q, agentID, userID).Scan(&phoneNumberID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select agent phone number: %w", err)
	}
	return phoneNumberID, true, nil
}

// AssignPhoneNumberIfUnassigned links phoneNumberID to the agent, but only while
// that agent still carries no number of its own. It backs the QR login started
// from an agent's editor: the number that login produces belongs to the agent
// that asked for it, so the scan alone finishes the setup. An assignment the
// owner made by hand in the meantime wins, which is why this never overwrites a
// number already on the agent.
//
// The bool reports whether an assignment was written. false with a nil error
// means there was nothing to do — no such agent for this user, or it already has
// a number. A number another agent holds is ErrPhoneNumberAssignmentConflict.
func (r *Repository) AssignPhoneNumberIfUnassigned(ctx context.Context, userID, agentID, phoneNumberID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	agentID = strings.TrimSpace(agentID)
	phoneNumberID = strings.TrimSpace(phoneNumberID)
	if userID == "" || agentID == "" || phoneNumberID == "" {
		return false, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// The same ownership and exclusivity rules a PATCH goes through, taken under
	// the same row lock, so a scan landing while the owner assigns the number by
	// hand serializes instead of handing it to two agents.
	if err := resolvePhoneNumberAssignment(ctx, tx, userID, agentID, &phoneNumberID); err != nil {
		return false, err
	}

	const q = `
		UPDATE agents
		SET phone_number_id = $1, updated_at = now()
		WHERE id = $2
			AND user_id = $3
			AND (phone_number_id IS NULL OR btrim(phone_number_id) = '')`
	tag, err := tx.Exec(ctx, q, phoneNumberID, agentID, userID)
	if err != nil {
		return false, fmt.Errorf("assign phone number: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit tx: %w", err)
	}
	return true, nil
}
