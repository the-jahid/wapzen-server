package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrKnowledgeBaseNotFound is returned when an agent is attached to a knowledge
// base that does not exist or is not owned by the authenticated user. The
// offending id is appended, since a request may carry several.
var ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")

// normalizeKnowledgeBaseIDs trims the supplied ids, drops the blanks and
// collapses duplicates while preserving first-seen order. Attaching the same
// knowledge base twice is the same state as attaching it once, so the duplicate
// is dropped here rather than left to the primary key.
func normalizeKnowledgeBaseIDs(ids []string) []string {
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

// resolveKnowledgeBaseAttachments makes the agent's attachment set exactly ids,
// inside the caller's transaction. An empty (or nil) list detaches everything.
//
// Every id is first checked against the user's own knowledge bases: the join
// table's foreign key only proves a knowledge base exists, not that this user
// owns it, so without this check an id guessed from another account would
// attach. A missing or foreign id fails the whole write with
// ErrKnowledgeBaseNotFound, which reads the same either way so ownership never
// leaks.
//
// Attachments that survive the change are left in place rather than deleted and
// re-inserted, so created_at keeps meaning "attached since".
func (r *Repository) resolveKnowledgeBaseAttachments(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	agentID string,
	ids []string,
) error {
	ids = normalizeKnowledgeBaseIDs(ids)

	if len(ids) > 0 {
		// Report the first id the user cannot attach rather than a bare count, so
		// a request carrying several says which one was wrong.
		const missingQuery = `
			SELECT requested.id
			FROM unnest($1::text[]) AS requested(id)
			WHERE NOT EXISTS (
				SELECT 1 FROM knowledge_bases kb
				WHERE kb.id = requested.id AND kb.user_id = $2
			)
			LIMIT 1`

		var missingID string
		err := tx.QueryRow(ctx, missingQuery, ids, userID).Scan(&missingID)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s", ErrKnowledgeBaseNotFound, missingID)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check knowledge base ownership: %w", err)
		}
	}

	const deleteQuery = `
		DELETE FROM agent_knowledge_bases
		WHERE agent_id = $1 AND knowledge_base_id <> ALL($2::text[])`
	if _, err := tx.Exec(ctx, deleteQuery, agentID, ids); err != nil {
		return fmt.Errorf("detach knowledge bases: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	const insertQuery = `
		INSERT INTO agent_knowledge_bases (agent_id, knowledge_base_id)
		SELECT $1, requested.id FROM unnest($2::text[]) AS requested(id)
		ON CONFLICT (agent_id, knowledge_base_id) DO NOTHING`
	if _, err := tx.Exec(ctx, insertQuery, agentID, ids); err != nil {
		return fmt.Errorf("attach knowledge bases: %w", err)
	}
	return nil
}

// knowledgeBaseIDOrder is how attachments are read back everywhere: oldest
// attachment first, ties broken by id so a batch attached in one request has a
// stable order rather than whatever the table hands back.
const knowledgeBaseIDOrder = "created_at, knowledge_base_id"

// getKnowledgeBaseIDs reads one agent's attached knowledge base ids. The slice
// is always non-nil so the resource renders [] rather than null for an agent
// that answers from its prompt alone.
func (r *Repository) getKnowledgeBaseIDs(ctx context.Context, agentID string) ([]string, error) {
	query := fmt.Sprintf(`
		SELECT knowledge_base_id
		FROM agent_knowledge_bases
		WHERE agent_id = $1
		ORDER BY %s`, knowledgeBaseIDOrder)

	rows, err := r.pool.Query(ctx, query, agentID)
	if err != nil {
		return nil, fmt.Errorf("select knowledge base attachments: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan knowledge base attachment: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge base attachments: %w", err)
	}
	return ids, nil
}

// KnowledgeBasesForAgent loads the knowledge bases an agent can answer from,
// resolved down to their vector-store namespaces, in attachment order. It backs
// the live-call path: a call needs the namespaces to retrieve from and the names
// to tell the model what it can look things up in, not the full resources.
//
// Knowledge bases without a namespace are left out. There is nothing indexed
// under a knowledge base that has none, so including it would only widen the
// search with a namespace that can never match.
//
// Like the live-agent lookups it runs alongside, it is unscoped by user: it is
// reached from call handling, which knows the agent but not who owns it, and an
// agent can only ever be attached to its owner's knowledge bases (enforced on
// attachment by resolveKnowledgeBaseAttachments).
func (r *Repository) KnowledgeBasesForAgent(ctx context.Context, agentID string) ([]models.AgentKnowledgeBase, error) {
	// The order is knowledgeBaseIDOrder, spelled out because both tables carry a
	// created_at and the join would leave it ambiguous.
	const query = `
		SELECT kb.id, kb.knowledge_base_name, kb.pinecone_namespace
		FROM agent_knowledge_bases akb
		JOIN knowledge_bases kb ON kb.id = akb.knowledge_base_id
		WHERE akb.agent_id = $1
			AND kb.pinecone_namespace IS NOT NULL
			AND kb.pinecone_namespace <> ''
		ORDER BY akb.created_at, akb.knowledge_base_id`

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

// getKnowledgeBaseIDsForAgents loads the attachments for every agent id in one
// query, grouped by agent id, so listing a page of agents does not fan out into
// one query per agent. Agents with no attachments are absent from the result;
// buildResource turns that into an empty list.
func (r *Repository) getKnowledgeBaseIDsForAgents(ctx context.Context, agentIDs []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(agentIDs) == 0 {
		return out, nil
	}

	query := fmt.Sprintf(`
		SELECT agent_id, knowledge_base_id
		FROM agent_knowledge_bases
		WHERE agent_id = ANY($1)
		ORDER BY agent_id, %s`, knowledgeBaseIDOrder)

	rows, err := r.pool.Query(ctx, query, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("select knowledge base attachments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var agentID, knowledgeBaseID string
		if err := rows.Scan(&agentID, &knowledgeBaseID); err != nil {
			return nil, fmt.Errorf("scan knowledge base attachment: %w", err)
		}
		out[agentID] = append(out[agentID], knowledgeBaseID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge base attachments: %w", err)
	}
	return out, nil
}
