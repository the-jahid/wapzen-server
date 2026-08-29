package chatagents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/tools"
)

var (
	ErrNotFound              = errors.New("chat agent not found")
	ErrPhoneNumberNotFound   = errors.New("phone number not found")
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrToolNotFound          = errors.New("tool not found")
	ErrKnowledgeBaseConflict = errors.New("knowledge base is already attached to another agent")
	ErrToolConflict          = errors.New("tool is already attached to another agent")
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const chatAgentColumns = `
	id, created_at, updated_at,
	agent_name, phone_number_id, status,
	model_provider, model_name, model_temperature, prompt_system_prompt`

// scanRow uses explicit time values instead of a reflection-based mapper so a
// schema change cannot silently reshuffle the API resource.
func scanRow(scanner interface{ Scan(...any) error }) (Resource, error) {
	var resource Resource
	err := scanner.Scan(
		&resource.ID, &resource.CreatedAt, &resource.UpdatedAt,
		&resource.Agent.Name, &resource.Agent.PhoneNumberID, &resource.Agent.Status,
		&resource.Model.Provider, &resource.Model.Name, &resource.Model.Temperature,
		&resource.Prompt.SystemPrompt,
	)
	return resource, err
}

func (r *Repository) Create(ctx context.Context, userID string, req CreateRequest) (Resource, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Resource{}, fmt.Errorf("begin chat agent create: %w", err)
	}
	defer tx.Rollback(ctx)

	a := req.Agent
	status := defaultString(a.Status, DefaultStatus)
	provider, model, temperature := DefaultProvider, DefaultModel, DefaultTemperature
	if req.Model != nil {
		provider = defaultString(req.Model.Provider, provider)
		model = defaultString(req.Model.Name, model)
		if req.Model.Temperature != nil {
			temperature = *req.Model.Temperature
		}
	}
	systemPrompt := DefaultSystemPrompt
	if req.Prompt != nil {
		systemPrompt = defaultString(req.Prompt.SystemPrompt, systemPrompt)
	}
	phoneNumberID := normalizeNullable(a.PhoneNumberID)
	if err := verifyPhoneNumber(ctx, tx, userID, phoneNumberID); err != nil {
		return Resource{}, err
	}

	query := `INSERT INTO chat_agents (
		user_id, agent_name, phone_number_id, status,
		model_provider, model_name, model_temperature, prompt_system_prompt
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	RETURNING ` + chatAgentColumns
	resource, err := scanRow(tx.QueryRow(ctx, query,
		userID, strings.TrimSpace(a.Name), phoneNumberID,
		status, provider, model, temperature, systemPrompt,
	))
	if err != nil {
		return Resource{}, fmt.Errorf("insert chat agent: %w", err)
	}

	if req.KnowledgeBase != nil {
		if err := resolveAttachments(ctx, tx, knowledgeBases, userID, resource.ID, req.KnowledgeBase.KnowledgeBaseIDs); err != nil {
			return Resource{}, err
		}
	}
	if req.Tools != nil {
		if err := resolveAttachments(ctx, tx, chatTools, userID, resource.ID, req.Tools.ToolIDs); err != nil {
			return Resource{}, err
		}
	}
	created := []Resource{resource}
	if err := r.attachToMany(ctx, tx, created); err != nil {
		return Resource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, fmt.Errorf("commit chat agent create: %w", err)
	}
	return created[0], nil
}

func (r *Repository) GetByID(ctx context.Context, userID, id string) (Resource, error) {
	resource, err := scanRow(r.pool.QueryRow(ctx, `SELECT `+chatAgentColumns+` FROM chat_agents WHERE id=$1 AND user_id=$2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, fmt.Errorf("select chat agent: %w", err)
	}
	return r.withAttachments(ctx, resource)
}

func (r *Repository) List(ctx context.Context, userID string, page, limit int) ([]Resource, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM chat_agents WHERE user_id=$1`, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count chat agents: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT `+chatAgentColumns+` FROM chat_agents WHERE user_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list chat agents: %w", err)
	}
	defer rows.Close()
	resources := make([]Resource, 0)
	for rows.Next() {
		resource, err := scanRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan chat agent: %w", err)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate chat agents: %w", err)
	}
	if err := r.attachToMany(ctx, r.pool, resources); err != nil {
		return nil, 0, err
	}
	return resources, total, nil
}

func (r *Repository) Update(ctx context.Context, userID, id string, req UpdateRequest) (Resource, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Resource{}, fmt.Errorf("begin chat agent update: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := scanRow(tx.QueryRow(ctx, `SELECT `+chatAgentColumns+` FROM chat_agents WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	} else if err != nil {
		return Resource{}, fmt.Errorf("lock chat agent: %w", err)
	}

	// Validate the effective state, not just fields present in this PATCH. This
	// catches both activating an agent that has no number and removing the
	// number from an agent that is already active.
	nextStatus := current.Agent.Status
	nextPhoneNumberID := current.Agent.PhoneNumberID
	if a := req.Agent; a != nil {
		if a.Status != nil {
			nextStatus = *a.Status
		}
		if a.PhoneNumberID.Set {
			nextPhoneNumberID = normalizeNullable(a.PhoneNumberID.Value)
		}
	}
	if err := validateActiveHasPhone(nextStatus, nextPhoneNumberID); err != nil {
		return Resource{}, err
	}

	sets := make([]string, 0)
	args := []any{id, userID}
	add := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	if a := req.Agent; a != nil {
		if a.Name != nil {
			add("agent_name", strings.TrimSpace(*a.Name))
		}
		if a.PhoneNumberID.Set {
			phone := normalizeNullable(a.PhoneNumberID.Value)
			if err := verifyPhoneNumber(ctx, tx, userID, phone); err != nil {
				return Resource{}, err
			}
			add("phone_number_id", phone)
		}
		if a.Status != nil {
			add("status", *a.Status)
		}
	}
	if m := req.Model; m != nil {
		if m.Provider != nil {
			add("model_provider", *m.Provider)
		}
		if m.Name != nil {
			add("model_name", strings.TrimSpace(*m.Name))
		}
		if m.Temperature != nil {
			add("model_temperature", *m.Temperature)
		}
	}
	if p := req.Prompt; p != nil {
		if p.SystemPrompt != nil {
			add("prompt_system_prompt", strings.TrimSpace(*p.SystemPrompt))
		}
	}
	touchAgent := len(sets) > 0 ||
		(req.KnowledgeBase != nil && req.KnowledgeBase.KnowledgeBaseIDs != nil) ||
		(req.Tools != nil && req.Tools.ToolIDs != nil)
	if len(sets) > 0 {
		sets = append(sets, "updated_at=now()")
		if _, err := tx.Exec(ctx, `UPDATE chat_agents SET `+strings.Join(sets, ", ")+` WHERE id=$1 AND user_id=$2`, args...); err != nil {
			return Resource{}, fmt.Errorf("update chat agent: %w", err)
		}
	} else if touchAgent {
		if _, err := tx.Exec(ctx, `UPDATE chat_agents SET updated_at=now() WHERE id=$1 AND user_id=$2`, id, userID); err != nil {
			return Resource{}, fmt.Errorf("touch chat agent: %w", err)
		}
	}
	if req.KnowledgeBase != nil && req.KnowledgeBase.KnowledgeBaseIDs != nil {
		if err := resolveAttachments(ctx, tx, knowledgeBases, userID, id, *req.KnowledgeBase.KnowledgeBaseIDs); err != nil {
			return Resource{}, err
		}
	}
	if req.Tools != nil && req.Tools.ToolIDs != nil {
		if err := resolveAttachments(ctx, tx, chatTools, userID, id, *req.Tools.ToolIDs); err != nil {
			return Resource{}, err
		}
	}
	resource, err := scanRow(tx.QueryRow(ctx, `SELECT `+chatAgentColumns+` FROM chat_agents WHERE id=$1 AND user_id=$2`, id, userID))
	if err != nil {
		return Resource{}, fmt.Errorf("read updated chat agent: %w", err)
	}
	updated := []Resource{resource}
	if err := r.attachToMany(ctx, tx, updated); err != nil {
		return Resource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, fmt.Errorf("commit chat agent update: %w", err)
	}
	return updated[0], nil
}

func (r *Repository) Delete(ctx context.Context, userID, id string) error {
	command, err := r.pool.Exec(ctx, `DELETE FROM chat_agents WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete chat agent: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) PhoneNumberID(ctx context.Context, userID, id string) (*string, error) {
	var phone *string
	err := r.pool.QueryRow(ctx, `SELECT phone_number_id FROM chat_agents WHERE id=$1 AND user_id=$2`, id, userID).Scan(&phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return phone, err
}

// LiveInboundForPhoneNumber is deliberately unscoped by user: message delivery
// starts with the connected phone-number row, whose ownership was verified when
// the assignment was written.
func (r *Repository) LiveInboundForPhoneNumber(ctx context.Context, phoneNumberID string) (*LiveAgent, error) {
	var live LiveAgent
	err := r.pool.QueryRow(ctx, `
		SELECT id, model_provider, model_name, model_temperature, prompt_system_prompt
		FROM chat_agents
		WHERE phone_number_id=$1 AND status='active'`, phoneNumberID).
		Scan(&live.ID, &live.ModelProvider, &live.ModelName, &live.ModelTemperature, &live.SystemPrompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select live chat agent: %w", err)
	}
	return &live, nil
}

func (r *Repository) KnowledgeBasesForChatAgent(ctx context.Context, id string) ([]models.AgentKnowledgeBase, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, knowledge_base_name, pinecone_namespace FROM knowledge_bases WHERE chat_agent_id=$1 AND pinecone_namespace IS NOT NULL ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.AgentKnowledgeBase, 0)
	for rows.Next() {
		var base models.AgentKnowledgeBase
		if err := rows.Scan(&base.ID, &base.Name, &base.Namespace); err != nil {
			return nil, err
		}
		out = append(out, base)
	}
	return out, rows.Err()
}

// ToolsForChatAgent loads the tools the chat agent may call, resolved down to
// what running one needs, in attachment order. It backs the incoming-message
// path, which needs the function names and descriptions to declare to the model
// and the configuration to execute when the model calls one.
//
// Like the live-agent lookup it runs alongside it is unscoped by user: message
// delivery starts from the connected phone number, whose ownership was verified
// when the assignment was written, and a chat agent can only ever be attached to
// its own owner's tools.
//
// A row whose stored configuration cannot be decoded is skipped rather than
// failing the reply: one broken tool must not cost the sender an answer.
func (r *Repository) ToolsForChatAgent(ctx context.Context, id string) ([]models.AgentTool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, type, tool_name, description, config
		FROM tools
		WHERE chat_agent_id=$1
		ORDER BY created_at,id`, id)
	if err != nil {
		return nil, fmt.Errorf("select chat agent tools: %w", err)
	}
	defer rows.Close()

	out := make([]models.AgentTool, 0)
	for rows.Next() {
		var (
			tool   models.Tool
			config []byte
		)
		if err := rows.Scan(&tool.ID, &tool.Type, &tool.Name, &tool.Description, &config); err != nil {
			return nil, fmt.Errorf("scan chat agent tool: %w", err)
		}
		if err := tools.DecodeConfig(&tool, config); err != nil {
			continue
		}
		out = append(out, models.AgentTool{
			ID:           tool.ID,
			Type:         tool.Type,
			Name:         tool.Name,
			Description:  tool.Description,
			APIRequest:   tool.APIRequest,
			TransferCall: tool.TransferCall,
			SendText:     tool.SendText,
			EndCall:      tool.EndCall,
		})
	}
	return out, rows.Err()
}

func verifyPhoneNumber(ctx context.Context, tx pgx.Tx, userID string, id *string) error {
	if id == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM phone_numbers WHERE id=$1 AND user_id=$2 FOR SHARE`, *id, userID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return ErrPhoneNumberNotFound
	} else if err != nil {
		return fmt.Errorf("verify phone number: %w", err)
	}
	return nil
}

type attachmentKind struct {
	table, idsField, notFoundSubject string
	notFound, conflict               error
}

var knowledgeBases = attachmentKind{"knowledge_bases", "knowledge_base_ids", "knowledge base", ErrKnowledgeBaseNotFound, ErrKnowledgeBaseConflict}
var chatTools = attachmentKind{"tools", "tool_ids", "tool", ErrToolNotFound, ErrToolConflict}

func resolveAttachments(ctx context.Context, tx pgx.Tx, kind attachmentKind, userID, chatAgentID string, rawIDs []string) error {
	ids := normalizeIDs(rawIDs)
	if len(ids) > 0 {
		query := fmt.Sprintf(`SELECT id, agent_id, chat_agent_id FROM %s WHERE id=ANY($1::text[]) AND user_id=$2 ORDER BY id FOR UPDATE`, kind.table)
		rows, err := tx.Query(ctx, query, ids, userID)
		if err != nil {
			return fmt.Errorf("lock %ss: %w", kind.notFoundSubject, err)
		}
		owners := make(map[string][2]*string, len(ids))
		for rows.Next() {
			var id string
			var voice, chat *string
			if err := rows.Scan(&id, &voice, &chat); err != nil {
				rows.Close()
				return err
			}
			owners[id] = [2]*string{voice, chat}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			owner, ok := owners[id]
			if !ok {
				return fmt.Errorf("%w: %s", kind.notFound, id)
			}
			if owner[0] != nil || (owner[1] != nil && *owner[1] != chatAgentID) {
				return fmt.Errorf("%w: %s", kind.conflict, id)
			}
		}
	}
	detach := fmt.Sprintf(`UPDATE %s SET chat_agent_id=NULL, updated_at=now() WHERE chat_agent_id=$1 AND id <> ALL($2::text[])`, kind.table)
	if _, err := tx.Exec(ctx, detach, chatAgentID, ids); err != nil {
		return err
	}
	if len(ids) > 0 {
		attach := fmt.Sprintf(`UPDATE %s SET chat_agent_id=$1, updated_at=now() WHERE id=ANY($2::text[]) AND chat_agent_id IS DISTINCT FROM $1`, kind.table)
		if _, err := tx.Exec(ctx, attach, chatAgentID, ids); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) withAttachments(ctx context.Context, resource Resource) (Resource, error) {
	resources := []Resource{resource}
	if err := r.attachToMany(ctx, r.pool, resources); err != nil {
		return Resource{}, err
	}
	return resources[0], nil
}

type attachmentQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (r *Repository) attachToMany(ctx context.Context, q attachmentQueryer, resources []Resource) error {
	if len(resources) == 0 {
		return nil
	}
	ids := make([]string, len(resources))
	positions := make(map[string]int, len(resources))
	for i := range resources {
		ids[i] = resources[i].ID
		positions[resources[i].ID] = i
		resources[i].KnowledgeBase.KnowledgeBaseIDs = []string{}
		resources[i].Tools.ToolIDs = []string{}
	}
	for _, spec := range []struct {
		kind attachmentKind
		set  func(*Resource, string)
	}{
		{knowledgeBases, func(r *Resource, id string) {
			r.KnowledgeBase.KnowledgeBaseIDs = append(r.KnowledgeBase.KnowledgeBaseIDs, id)
		}},
		{chatTools, func(r *Resource, id string) { r.Tools.ToolIDs = append(r.Tools.ToolIDs, id) }},
	} {
		query := fmt.Sprintf(`SELECT chat_agent_id,id FROM %s WHERE chat_agent_id=ANY($1::text[]) ORDER BY chat_agent_id,created_at,id`, spec.kind.table)
		rows, err := q.Query(ctx, query, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var owner, id string
			if err := rows.Scan(&owner, &id); err != nil {
				rows.Close()
				return err
			}
			i := positions[owner]
			spec.set(&resources[i], id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func normalizeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func normalizeNullable(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
