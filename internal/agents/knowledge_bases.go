package agents

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// A knowledge base is attached by carrying an agent_id that points back at its
// agent, and the agent's write path replaces that set wholesale. The direct
// column makes the agent the owner of what it attaches, which is what the
// cascade in migration 00044 then means: deleting an agent deletes its
// knowledge bases rather than orphaning them, and a base answers for one agent
// at a time. (Tools are shared instead, through a join table — see tools.go.)

// knowledgeBaseOrder is how an agent's knowledge bases are read back everywhere:
// oldest first, ties broken by id so the order is stable.
const knowledgeBaseOrder = "created_at, id"

// attachKnowledgeBases makes the agent's knowledge base set exactly ids, inside
// the caller's transaction. An empty (or nil) list detaches everything.
//
// Every id is checked twice before anything is written. First that the base
// exists and belongs to this user: the foreign key only proves the row exists,
// so without this an id guessed from another account would attach. A missing or
// foreign id fails the whole write with ErrKnowledgeBaseNotFound, which reads
// the same either way so ownership never leaks.
//
// Then that it is not already another agent's — voice or chat. Attaching a base
// another agent answers from would silently take it away mid-conversation, so
// the request is refused with ErrKnowledgeBaseAttachmentConflict instead, the
// way a phone number already assigned elsewhere is. Detaching it there first is
// the deliberate act that makes it available.
//
// Bases that survive the change are left alone, so an attachment that did not
// move does not get a fresh updated_at.
func attachKnowledgeBases(ctx context.Context, tx pgx.Tx, userID, agentID string, ids []string) error {
	ids = normalizeIDs(ids)

	if len(ids) > 0 {
		owners, err := lockKnowledgeBases(ctx, tx, userID, ids)
		if err != nil {
			return err
		}
		// Walk the request rather than the result, so the id reported is the
		// first one the user cannot attach — which matters when a request
		// carries several.
		for _, id := range ids {
			owner, found := owners[id]
			if !found {
				return fmt.Errorf("%w: %s", ErrKnowledgeBaseNotFound, id)
			}
			if owner.chatAgentID != nil || (owner.agentID != nil && *owner.agentID != agentID) {
				return fmt.Errorf("%w: %s", ErrKnowledgeBaseAttachmentConflict, id)
			}
		}
	}

	// `<> ALL` of an empty array is true for every row, so this detaches the lot
	// when ids is empty, which is the documented meaning of an empty list.
	const detachQuery = `
		UPDATE knowledge_bases
		SET agent_id = NULL, updated_at = now()
		WHERE agent_id = $1 AND id <> ALL($2::text[])`
	if _, err := tx.Exec(ctx, detachQuery, agentID, ids); err != nil {
		return fmt.Errorf("detach knowledge bases: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	const attachQuery = `
		UPDATE knowledge_bases
		SET agent_id = $1, updated_at = now()
		WHERE id = ANY($2::text[]) AND agent_id IS DISTINCT FROM $1`
	if _, err := tx.Exec(ctx, attachQuery, agentID, ids); err != nil {
		return fmt.Errorf("attach knowledge bases: %w", err)
	}
	return nil
}

// knowledgeBaseOwner is who currently holds a knowledge base.
type knowledgeBaseOwner struct {
	agentID     *string
	chatAgentID *string
}

// lockKnowledgeBases locks the requested bases that belong to userID and returns
// each one's current owner, keyed by id. An id absent from the map is one the
// user cannot attach — it does not exist, or it is somebody else's.
//
// The rows are locked because both checks attachKnowledgeBases makes are
// read-then-write: without the lock, two requests could each see a base as
// unattached and both claim it, and the loser's agent would end up quietly
// detached. Locking in id order keeps overlapping requests from deadlocking.
func lockKnowledgeBases(ctx context.Context, tx pgx.Tx, userID string, ids []string) (map[string]knowledgeBaseOwner, error) {
	const query = `
		SELECT id, agent_id, chat_agent_id
		FROM knowledge_bases
		WHERE id = ANY($1::text[]) AND user_id = $2
		ORDER BY id
		FOR UPDATE`

	rows, err := tx.Query(ctx, query, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("lock knowledge bases: %w", err)
	}
	defer rows.Close()

	owners := make(map[string]knowledgeBaseOwner, len(ids))
	for rows.Next() {
		var (
			id    string
			owner knowledgeBaseOwner
		)
		if err := rows.Scan(&id, &owner.agentID, &owner.chatAgentID); err != nil {
			return nil, fmt.Errorf("scan knowledge base owner: %w", err)
		}
		owners[id] = owner
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge base owners: %w", err)
	}
	return owners, nil
}

// knowledgeBaseIDsForAgent reads one agent's knowledge base ids. The slice is
// always non-nil so the resource renders [] rather than null.
func knowledgeBaseIDsForAgent(ctx context.Context, q querier, agentID string) ([]string, error) {
	query := fmt.Sprintf(`SELECT id FROM knowledge_bases WHERE agent_id = $1 ORDER BY %s`, knowledgeBaseOrder)

	rows, err := q.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select knowledge base attachments: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan knowledge base attachments: %w", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// knowledgeBaseIDsForAgents loads the knowledge base ids of every agent in one
// query, grouped by agent id, so listing a page of agents does not fan out into
// one query per agent. Agents with none are absent from the result.
func knowledgeBaseIDsForAgents(ctx context.Context, q querier, agentIDs []string) (map[string][]string, error) {
	query := fmt.Sprintf(`
		SELECT agent_id, id
		FROM knowledge_bases
		WHERE agent_id = ANY($1::text[])
		ORDER BY agent_id, %s`, knowledgeBaseOrder)
	return groupIDsByAgent(ctx, q, query, agentIDs, "knowledge base")
}

// KnowledgeBasesForAgent loads the knowledge bases an agent can answer from,
// resolved down to their vector-store namespaces, in attachment order. It backs
// the live-call path — a call needs the namespaces to retrieve from and the
// names to tell the model what it can look things up in — and the agent delete,
// which purges those namespaces once the cascade has removed the rows.
//
// Knowledge bases without a namespace are left out: nothing is indexed under
// one, so including it would only widen the search with a namespace that can
// never match.
//
// It is unscoped by user, like the lookups in runtime.go: an agent can only ever
// own its owner's knowledge bases (enforced by attachKnowledgeBases).
func (r *Repository) KnowledgeBasesForAgent(ctx context.Context, agentID string) ([]models.AgentKnowledgeBase, error) {
	query := fmt.Sprintf(`
		SELECT id, knowledge_base_name, pinecone_namespace
		FROM knowledge_bases
		WHERE agent_id = $1
			AND pinecone_namespace IS NOT NULL
			AND pinecone_namespace <> ''
		ORDER BY %s`, knowledgeBaseOrder)

	rows, err := r.pool.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select agent knowledge bases: %w", err)
	}
	defer rows.Close()

	bases := make([]models.AgentKnowledgeBase, 0)
	for rows.Next() {
		var base models.AgentKnowledgeBase
		if err := rows.Scan(&base.ID, &base.Name, &base.Namespace); err != nil {
			return nil, fmt.Errorf("scan agent knowledge base: %w", err)
		}
		bases = append(bases, base)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent knowledge bases: %w", err)
	}
	return bases, nil
}

// groupIDsByAgent runs a query selecting (agent_id, id) pairs for the given
// agents and groups the ids by agent. It is shared by the knowledge base and
// tool bulk readers; subject names the attachment kind in errors.
func groupIDsByAgent(ctx context.Context, q querier, query string, agentIDs []string, subject string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(agentIDs) == 0 {
		return out, nil
	}

	rows, err := q.Query(ctx, query, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("select %s attachments: %w", subject, err)
	}
	defer rows.Close()

	for rows.Next() {
		var agentID, id string
		if err := rows.Scan(&agentID, &id); err != nil {
			return nil, fmt.Errorf("scan %s attachment: %w", subject, err)
		}
		out[agentID] = append(out[agentID], id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s attachments: %w", subject, err)
	}
	return out, nil
}
