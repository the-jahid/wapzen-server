package agents

import (
	"context"
	"log"
	"strings"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/swagger/modules/agents/types"
)

// InboundCallRefresher reconnects the WhatsApp session for a phone number so its
// inbound-call handling matches the number's current agent assignment.
// Satisfied by *whatsapplogin.Manager.
type InboundCallRefresher interface {
	RefreshInboundCallHandling(phoneNumberID string)
}

// NamespacePurger deletes a knowledge base's vectors from the vector store.
// Satisfied by *knowledgebases.Indexer. Deleting an agent cascades to the
// knowledge bases it owns, and the rows going away does not take their vectors
// with them — this is how the service cleans up after the cascade.
type NamespacePurger interface {
	Enabled() bool
	PurgeNamespace(ctx context.Context, kb models.KnowledgeBase) error
}

// Service is the business layer for agents. It validates requests, applies the
// rules that shape a write before it reaches the repository, and runs the side
// effects a change has outside the database: reconnecting a WhatsApp number whose
// assignment moved, and purging the vectors of knowledge bases a delete took
// with it.
type Service struct {
	repo    *Repository
	inbound InboundCallRefresher
	indexer NamespacePurger
}

// NewService creates the agents service. inbound may be nil to skip the
// reconnect that applies an assignment change to a running WhatsApp session (it
// then takes effect on the number's next reconnect); indexer may be nil to leave
// a deleted agent's vector namespaces in place.
func NewService(repo *Repository, inbound InboundCallRefresher, indexer NamespacePurger) *Service {
	return &Service{repo: repo, inbound: inbound, indexer: indexer}
}

// Create validates and persists a new agent owned by userID.
func (s *Service) Create(ctx context.Context, userID string, req types.CreateAgentRequest) (types.AgentResource, error) {
	if req.Agent == nil || strings.TrimSpace(req.Agent.Name) == "" {
		return types.AgentResource{}, invalid("agent.name is required")
	}

	// The go-live rule: an agent may only be active with a phone number to call
	// from. Without one it starts paused — overriding a requested active status
	// and the column's 'active' default — so a new agent is never born Live with
	// nothing to call from. With a number, the requested status (or the schema
	// default) stands. Updates enforce the same rule in Repository.Update.
	if !hasPhoneNumberAssigned(req.Agent.PhoneNumberID) {
		agent := *req.Agent
		agent.Status = "inactive"
		req.Agent = &agent
	}

	res, err := s.repo.Create(ctx, userID, req)
	if err != nil {
		return types.AgentResource{}, err
	}

	// A new agent can only add an assignment, so reconnect the assigned number (if
	// any) to install its inbound-call handler.
	s.refreshInbound(res.Agent.PhoneNumberID)
	return res, nil
}

// Get returns one agent owned by userID.
func (s *Service) Get(ctx context.Context, userID, agentID string) (types.AgentResource, error) {
	return s.repo.GetByID(ctx, userID, agentID)
}

// List returns one page (1-based) of the agents owned by userID and the total
// number of agents they own.
func (s *Service) List(ctx context.Context, userID string, page, limit int) ([]types.AgentResource, int, error) {
	return s.repo.List(ctx, userID, limit, (page-1)*limit)
}

// Update applies a partial update, given as the raw JSON body, to an agent
// owned by userID. Only the fields present in body change.
func (s *Service) Update(ctx context.Context, userID, agentID string, body []byte) (types.AgentResource, error) {
	patch, err := parsePatch(body)
	if err != nil {
		return types.AgentResource{}, err
	}

	// Capture the assignment before the update so a change of number can
	// reconnect both the old and the new one afterward. Best effort: on error
	// oldPhone is nil and only the new number (if any) is reconnected.
	oldPhone, _, _ := s.repo.LookupPhoneNumberForAgent(ctx, userID, agentID)

	res, err := s.repo.Update(ctx, userID, agentID, patch)
	if err != nil {
		return types.AgentResource{}, err
	}

	// Only reconnect when the assigned number actually changed, so unrelated edits
	// (prompt, voice, status, …) never trigger a needless WhatsApp reconnect.
	if !samePhoneNumber(oldPhone, res.Agent.PhoneNumberID) {
		s.refreshInbound(oldPhone, res.Agent.PhoneNumberID)
	}
	return res, nil
}

// Delete removes an agent owned by userID, together with the knowledge bases it
// owns (cascaded by the schema) and the vectors those bases had indexed (purged
// here, since no cascade reaches the vector store). Its tools are shared, so
// they are only detached.
func (s *Service) Delete(ctx context.Context, userID, agentID string) error {
	// Capture the assigned number before deletion so its inbound handler can be
	// removed afterward (deleting the agent leaves the number unassigned).
	assignedPhone, _, _ := s.repo.LookupPhoneNumberForAgent(ctx, userID, agentID)

	// And the namespaces of the knowledge bases the delete is about to cascade
	// to, which are unreachable once their rows are gone. Nothing is purged
	// unless the delete below succeeds, so a failed or unauthorized delete
	// touches no vectors.
	doomedBases, _ := s.repo.KnowledgeBasesForAgent(ctx, agentID)

	if err := s.repo.Delete(ctx, userID, agentID); err != nil {
		return err
	}

	s.refreshInbound(assignedPhone)
	s.purgeNamespaces(ctx, doomedBases)
	return nil
}

// refreshInbound reconnects each distinct, assigned phone number so its
// inbound-call handling is re-evaluated against the current agent assignment.
// Nil entries, blanks and duplicates are skipped.
func (s *Service) refreshInbound(phoneNumberIDs ...*string) {
	if s.inbound == nil {
		return
	}
	seen := make(map[string]struct{}, len(phoneNumberIDs))
	for _, p := range phoneNumberIDs {
		id := normalizePhoneNumberID(p)
		if id == nil {
			continue
		}
		if _, ok := seen[*id]; ok {
			continue
		}
		seen[*id] = struct{}{}
		s.inbound.RefreshInboundCallHandling(*id)
	}
}

// purgeNamespaces deletes the vectors of knowledge bases an agent delete has
// just removed.
//
// Best effort, like the knowledge-base delete endpoint's own purge: the rows are
// already gone, so a namespace that could not be dropped is logged rather than
// turned into a failed delete. It leaves vectors nothing can reach, not a
// half-deleted agent.
func (s *Service) purgeNamespaces(ctx context.Context, bases []models.AgentKnowledgeBase) {
	if s.indexer == nil || !s.indexer.Enabled() {
		return
	}
	for _, base := range bases {
		namespace := base.Namespace
		if namespace == "" {
			continue
		}
		kb := models.KnowledgeBase{ID: base.ID, NamespaceID: &namespace}
		if err := s.indexer.PurgeNamespace(ctx, kb); err != nil {
			log.Printf("agent delete: purging vector namespace for knowledge base %s failed: %v", base.ID, err)
		}
	}
}
