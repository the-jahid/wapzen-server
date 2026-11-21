package agents

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// A voice knowledge base (voice_knowledge_bases, never chat's knowledge_bases)
// is attached by carrying an agent_id that points back at its agent, and the
// agent's write path replaces that set wholesale. A base answers for one agent
// at a time. Deleting the agent detaches its bases (ON DELETE SET NULL) rather
// than deleting them, so their vectors are never orphaned. (Tools are shared
// instead, through a join table — see tools.go.)

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
// Then that it is not already another agent's. Attaching a base
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
			if owner.agentID != nil && *owner.agentID != agentID {
				return fmt.Errorf("%w: %s", ErrKnowledgeBaseAttachmentConflict, id)
			}
		}
	}

	// `<> ALL` of an empty array is true for every row, so this detaches the lot
	// when ids is empty, which is the documented meaning of an empty list.
	const detachQuery = `
		UPDATE voice_knowledge_bases
		SET agent_id = NULL, updated_at = now()
		WHERE agent_id = $1 AND id <> ALL($2::text[])`
	if _, err := tx.Exec(ctx, detachQuery, agentID, ids); err != nil {
		return fmt.Errorf("detach knowledge bases: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	const attachQuery = `
		UPDATE voice_knowledge_bases
		SET agent_id = $1, updated_at = now()
		WHERE id = ANY($2::text[]) AND agent_id IS DISTINCT FROM $1`
	if _, err := tx.Exec(ctx, attachQuery, agentID, ids); err != nil {
		return fmt.Errorf("attach knowledge bases: %w", err)
	}
	return nil
}

// knowledgeBaseOwner is who currently holds a knowledge base.
type knowledgeBaseOwner struct {
	agentID *string
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
		SELECT id, agent_id
		FROM voice_knowledge_bases
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
		if err := rows.Scan(&id, &owner.agentID); err != nil {
			return nil, fmt.Errorf("scan knowledge base owner: %w", err)
		}
		owners[id] = owner
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge base owners: %w", err)
	}
	return owners, nil
}

// knowledgeBaseIDsQuery selects the (agent_id, id) pairs of every knowledge base
// attached to the agents in $1; see Repository.attachmentIDs.
const knowledgeBaseIDsQuery = `
	SELECT agent_id, id
	FROM voice_knowledge_bases
	WHERE agent_id = ANY($1::text[])
	ORDER BY agent_id, ` + knowledgeBaseOrder

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
		FROM voice_knowledge_bases
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

// KnowledgeDocumentsForAgent returns the full text of every source in the
// agent's knowledge bases, in attachment order, but only when all of it
// together is at most maxChars characters; otherwise (or when there is nothing)
// it returns none, and the call searches the vector store instead. The size
// check runs in the same query, so a large knowledge base costs one round trip
// and never ships its text.
//
// Unscoped by user for the same reason as KnowledgeBasesForAgent.
func (r *Repository) KnowledgeDocumentsForAgent(ctx context.Context, agentID string, maxChars int) ([]models.KnowledgeDocument, error) {
	if maxChars <= 0 {
		return nil, nil
	}
	const query = `
		WITH src AS (
			SELECT kb.knowledge_base_name, kb.created_at AS kb_created, kb.id AS kb_id,
				s.title, s.content, s.created_at AS source_created, s.id AS source_id
			FROM voice_knowledge_base_sources s
			JOIN voice_knowledge_bases kb ON kb.id = s.knowledge_base_id
			WHERE kb.agent_id = $1
		), total AS (
			SELECT COALESCE(SUM(char_length(content)), 0) AS chars FROM src
		)
		SELECT src.knowledge_base_name, src.title, src.content
		FROM src, total
		WHERE total.chars <= $2
		ORDER BY src.kb_created, src.kb_id, src.source_created, src.source_id`

	rows, err := r.pool.Query(ctx, query, agentID, maxChars)
	if err != nil {
		return nil, fmt.Errorf("select agent knowledge documents: %w", err)
	}
	defer rows.Close()

	var documents []models.KnowledgeDocument
	for rows.Next() {
		var document models.KnowledgeDocument
		if err := rows.Scan(&document.KnowledgeBase, &document.Title, &document.Content); err != nil {
			return nil, fmt.Errorf("scan agent knowledge document: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent knowledge documents: %w", err)
	}
	return documents, nil
}

// groupIDsByAgent reads the next result of an attachment batch — (agent_id, id)
// pairs — and groups the ids by agent. It is shared by the knowledge base and
// tool readers; subject names the attachment kind in errors.
func groupIDsByAgent(results pgx.BatchResults, subject string) (map[string][]string, error) {
	out := make(map[string][]string)
	rows, err := results.Query()
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
