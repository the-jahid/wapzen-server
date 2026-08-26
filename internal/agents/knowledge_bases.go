package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrKnowledgeBaseNotFound is returned when an agent is attached to a knowledge
// base that does not exist or is not owned by the authenticated user. The
// offending id is appended, since a request may carry several.
var ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")

// ErrKnowledgeBaseAttachmentConflict is returned when an agent is attached to a
// knowledge base another agent already owns. A knowledge base belongs to one
// agent, so honouring the request would take it away from that agent mid-
// conversation; it has to be detached there first.
var ErrKnowledgeBaseAttachmentConflict = errors.New("knowledge base is already attached to another agent")

// normalizeKnowledgeBaseIDs trims the supplied ids, drops the blanks and
// collapses duplicates while preserving first-seen order.
func normalizeKnowledgeBaseIDs(ids []string) []string {
	return normalizeAttachmentIDs(ids)
}

// resolveKnowledgeBaseAttachments makes the agent's knowledge base set exactly
// ids, inside the caller's transaction. An empty (or nil) list detaches
// everything. See attachmentTable.resolve for the checks it makes.
func (r *Repository) resolveKnowledgeBaseAttachments(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	agentID string,
	ids []string,
) error {
	return knowledgeBaseAttachments.resolve(ctx, tx, userID, agentID, ids)
}

// getKnowledgeBaseIDs reads one agent's knowledge base ids. The slice is always
// non-nil so the resource renders [] rather than null for an agent that answers
// from its prompt alone.
func (r *Repository) getKnowledgeBaseIDs(ctx context.Context, agentID string) ([]string, error) {
	return knowledgeBaseAttachments.idsForAgent(ctx, r.pool, agentID)
}

// getKnowledgeBaseIDsForAgents loads the attachments for every agent id in one
// query, so listing a page of agents does not fan out into one query per agent.
func (r *Repository) getKnowledgeBaseIDsForAgents(ctx context.Context, agentIDs []string) (map[string][]string, error) {
	return knowledgeBaseAttachments.idsForAgents(ctx, r.pool, agentIDs)
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
// agent can only ever own its owner's knowledge bases (enforced on attachment by
// resolveKnowledgeBaseAttachments).
func (r *Repository) KnowledgeBasesForAgent(ctx context.Context, agentID string) ([]models.AgentKnowledgeBase, error) {
	query := fmt.Sprintf(`
		SELECT id, knowledge_base_name, pinecone_namespace
		FROM knowledge_bases
		WHERE agent_id = $1
			AND pinecone_namespace IS NOT NULL
			AND pinecone_namespace <> ''
		ORDER BY %s`, attachmentOrder)

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
