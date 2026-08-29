package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// An agent's knowledge bases and its tools are attached the same way: the
// attached row carries an agent_id pointing back at its agent, and the agent's
// write path replaces that set wholesale. The two differ only in which table
// they read and which errors they report, so the mechanics live here once —
// this is lock-ordering-sensitive code that should not be maintained twice.
//
// The alternative, a join table per kind, is what this replaced. A direct column
// makes the agent the owner of what it attaches, which is what the cascade in
// migrations 00044 and 00045 then means: deleting an agent deletes its knowledge
// bases and tools rather than orphaning them.

// attachmentTable is one kind of row an agent attaches. name is a table
// identifier interpolated into SQL, so it is only ever a constant from the two
// values below; subject is how the kind reads inside a wrapped error.
type attachmentTable struct {
	name        string
	subject     string
	errNotFound error
	errConflict error
}

var (
	knowledgeBaseAttachments = attachmentTable{
		name:        "knowledge_bases",
		subject:     "knowledge base",
		errNotFound: ErrKnowledgeBaseNotFound,
		errConflict: ErrKnowledgeBaseAttachmentConflict,
	}

	toolAttachments = attachmentTable{
		name:        "tools",
		subject:     "tool",
		errNotFound: ErrToolNotFound,
		errConflict: ErrToolAttachmentConflict,
	}
)

// attachmentOrder is how an agent's attachments are read back everywhere: oldest
// first, ties broken by id so the order is stable rather than whatever the table
// hands back. It is the attached row's own creation order now that there is no
// join row to date the attachment itself.
const attachmentOrder = "created_at, id"

// querier is the read half of pgxpool.Pool, so the read helpers can be handed
// the pool without the package depending on the concrete type.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// normalizeAttachmentIDs trims the supplied ids, drops the blanks and collapses
// duplicates while preserving first-seen order. Attaching the same row twice is
// the same state as attaching it once.
func normalizeAttachmentIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// resolve makes the agent's attachment set exactly ids, inside the caller's
// transaction. An empty (or nil) list detaches everything.
//
// Every id is checked twice before anything is written. First that the row
// exists and belongs to this user: the foreign key only proves the row exists,
// so without this an id guessed from another account would attach — and for a
// tool, that account's endpoint would then be called with this agent's calls. A
// missing or foreign id fails the whole write with errNotFound, which reads the
// same either way so ownership never leaks.
//
// Then that it is not already another agent's. One row now has one agent, so
// attaching a base another agent answers from would silently take it away
// mid-conversation; the request is refused with errConflict instead, the way a
// phone number already assigned to another agent is (see
// resolvePhoneNumberAssignment). Detaching it there first is the deliberate act
// that makes it available.
//
// Rows that survive the change are left alone, so an attachment that did not
// move does not get a fresh updated_at.
func (t attachmentTable) resolve(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	agentID string,
	ids []string,
) error {
	ids = normalizeAttachmentIDs(ids)

	if len(ids) > 0 {
		owners, err := t.lockRequested(ctx, tx, userID, ids)
		if err != nil {
			return err
		}
		// Walk the request rather than the result, so the id reported is the
		// first one the user cannot attach — which matters when a request
		// carries several.
		for _, id := range ids {
			owner, found := owners[id]
			if !found {
				return fmt.Errorf("%w: %s", t.errNotFound, id)
			}
			if owner.chat != nil || (owner.voice != nil && *owner.voice != agentID) {
				return fmt.Errorf("%w: %s", t.errConflict, id)
			}
		}
	}

	// `<> ALL` of an empty array is true for every row, so this detaches the
	// lot when ids is empty, which is the documented meaning of an empty list.
	detachQuery := fmt.Sprintf(`
		UPDATE %s
		SET agent_id = NULL, updated_at = now()
		WHERE agent_id = $1 AND id <> ALL($2::text[])`, t.name)
	if _, err := tx.Exec(ctx, detachQuery, agentID, ids); err != nil {
		return fmt.Errorf("detach %ss: %w", t.subject, err)
	}

	if len(ids) == 0 {
		return nil
	}

	attachQuery := fmt.Sprintf(`
		UPDATE %s
		SET agent_id = $1, updated_at = now()
		WHERE id = ANY($2::text[]) AND agent_id IS DISTINCT FROM $1`, t.name)
	if _, err := tx.Exec(ctx, attachQuery, agentID, ids); err != nil {
		return fmt.Errorf("attach %ss: %w", t.subject, err)
	}
	return nil
}

// lockRequested locks the requested rows that belong to userID and returns each
// one's current agent, keyed by id. An id absent from the map is one the user
// cannot attach — it does not exist, or it is somebody else's.
//
// The rows are locked because both checks resolve.. makes are read-then-write:
// without the lock, two requests could each see a base as unattached and both
// claim it, and the loser's agent would end up quietly detached. Locking in id
// order keeps two overlapping requests from deadlocking each other.
func (t attachmentTable) lockRequested(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	ids []string,
) (map[string]attachmentOwner, error) {
	query := fmt.Sprintf(`
		SELECT id, agent_id, chat_agent_id
		FROM %s
		WHERE id = ANY($1::text[]) AND user_id = $2
		ORDER BY id
		FOR UPDATE`, t.name)

	rows, err := tx.Query(ctx, query, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("lock %ss: %w", t.subject, err)
	}
	defer rows.Close()

	owners := make(map[string]attachmentOwner, len(ids))
	for rows.Next() {
		var (
			id    string
			owner attachmentOwner
		)
		if err := rows.Scan(&id, &owner.voice, &owner.chat); err != nil {
			return nil, fmt.Errorf("scan %s owner: %w", t.subject, err)
		}
		owners[id] = owner
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s owners: %w", t.subject, err)
	}
	return owners, nil
}

type attachmentOwner struct {
	voice *string
	chat  *string
}

// idsForAgent reads one agent's attached ids. The slice is always non-nil so the
// resource renders [] rather than null for an agent that has none.
func (t attachmentTable) idsForAgent(ctx context.Context, q querier, agentID string) ([]string, error) {
	query := fmt.Sprintf(`
		SELECT id
		FROM %s
		WHERE agent_id = $1
		ORDER BY %s`, t.name, attachmentOrder)

	rows, err := q.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select %s attachments: %w", t.subject, err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s attachment: %w", t.subject, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s attachments: %w", t.subject, err)
	}
	return ids, nil
}

// idsForAgents loads the attachments for every agent id in one query, grouped by
// agent id, so listing a page of agents does not fan out into one query per
// agent. Agents with no attachments are absent from the result; buildResource
// turns that into an empty list.
func (t attachmentTable) idsForAgents(ctx context.Context, q querier, agentIDs []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(agentIDs) == 0 {
		return out, nil
	}

	query := fmt.Sprintf(`
		SELECT agent_id, id
		FROM %s
		WHERE agent_id = ANY($1::text[])
		ORDER BY agent_id, %s`, t.name, attachmentOrder)

	rows, err := q.Query(ctx, query, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("select %s attachments: %w", t.subject, err)
	}
	defer rows.Close()

	for rows.Next() {
		var agentID, id string
		if err := rows.Scan(&agentID, &id); err != nil {
			return nil, fmt.Errorf("scan %s attachment: %w", t.subject, err)
		}
		out[agentID] = append(out[agentID], id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s attachments: %w", t.subject, err)
	}
	return out, nil
}
