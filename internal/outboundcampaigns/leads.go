package outboundcampaigns

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/models"
)

var ErrLeadNotFound = errors.New("campaign lead not found")
var ErrLeadPhoneNumberTaken = errors.New("campaign lead phone number already in use")

const campaignLeadColumns = `
	id, campaign_id, phone_number, email, first_name, last_name, status,
	attempts, last_attempted_at, created_at, updated_at
`

func (r *Repository) CreateLead(ctx context.Context, userID string, params models.NewCampaignLead) (models.CampaignLead, error) {
	query := fmt.Sprintf(`
		INSERT INTO campaign_leads (campaign_id, phone_number, email, first_name, last_name)
		SELECT c.id, $3, $4, $5, $6
		FROM outbound_campaigns c
		WHERE c.id = $1 AND c.user_id = $2
		RETURNING %s
	`, campaignLeadColumns)
	lead, err := scanCampaignLead(r.db.QueryRow(ctx, query,
		params.CampaignID, userID, params.PhoneNumber, params.Email, params.FirstName, params.LastName))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.CampaignLead{}, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.CampaignLead{}, ErrLeadPhoneNumberTaken
		}
		return models.CampaignLead{}, fmt.Errorf("create campaign lead: %w", err)
	}
	return lead, nil
}

func (r *Repository) ListLeadsByCampaign(ctx context.Context, userID, campaignID, status string, limit, offset int) ([]models.CampaignLead, int, error) {
	where := "campaign_id = $1"
	args := []any{campaignID}
	if status != "" {
		where += " AND status = $2"
		args = append(args, status)
	}

	var owned bool
	var total int
	countQuery := `SELECT
		EXISTS (SELECT 1 FROM outbound_campaigns WHERE id = $1 AND user_id = $` + strconv.Itoa(len(args)+1) + `),
		(SELECT count(*) FROM campaign_leads WHERE ` + where + `)`
	if err := r.db.QueryRow(ctx, countQuery, append(args, userID)...).Scan(&owned, &total); err != nil {
		return nil, 0, fmt.Errorf("count campaign leads: %w", err)
	}
	if !owned {
		return nil, 0, ErrNotFound
	}

	out := make([]models.CampaignLead, 0)
	if total == 0 {
		return out, 0, nil
	}
	query := fmt.Sprintf(`SELECT %s FROM campaign_leads
		WHERE %s ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d`, campaignLeadColumns, where, len(args)+1, len(args)+2)
	rows, err := r.db.Query(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list campaign leads: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		lead, err := scanCampaignLead(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan campaign lead: %w", err)
		}
		out = append(out, lead)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate campaign leads: %w", err)
	}
	return out, total, nil
}

func (r *Repository) GetLeadByUser(ctx context.Context, userID, campaignID, leadID string) (models.CampaignLead, error) {
	query := fmt.Sprintf(`SELECT %s FROM campaign_leads l
		JOIN outbound_campaigns c ON c.id = l.campaign_id
		WHERE l.id = $1 AND l.campaign_id = $2 AND c.user_id = $3`, prefixedColumns("l", campaignLeadColumns))
	lead, err := scanCampaignLead(r.db.QueryRow(ctx, query, leadID, campaignID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.CampaignLead{}, ErrLeadNotFound
		}
		return models.CampaignLead{}, fmt.Errorf("get campaign lead: %w", err)
	}
	return lead, nil
}

func (r *Repository) UpdateLeadByUser(ctx context.Context, userID, campaignID, leadID string, params models.CampaignLeadUpdate) (models.CampaignLead, error) {
	if params.IsEmpty() {
		return models.CampaignLead{}, fmt.Errorf("update campaign lead: no fields to update")
	}
	set := []string{"updated_at = now()"}
	args := []any{leadID, campaignID, userID}
	next := 4
	add := func(column string, value any) {
		set = append(set, fmt.Sprintf("%s = $%d", column, next))
		args = append(args, value)
		next++
	}
	if params.SetPhone {
		add("phone_number", params.PhoneNumber)
	}
	if params.SetEmail {
		add("email", params.Email)
	}
	if params.SetFirst {
		add("first_name", params.FirstName)
	}
	if params.SetLast {
		add("last_name", params.LastName)
	}
	if params.SetStatus {
		add("status", params.Status)
	}

	query := fmt.Sprintf(`UPDATE campaign_leads l SET %s
		FROM outbound_campaigns c
		WHERE l.id = $1 AND l.campaign_id = $2
			AND c.id = l.campaign_id AND c.user_id = $3
		RETURNING %s`, strings.Join(set, ", "), prefixedColumns("l", campaignLeadColumns))
	lead, err := scanCampaignLead(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.CampaignLead{}, ErrLeadNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.CampaignLead{}, ErrLeadPhoneNumberTaken
		}
		return models.CampaignLead{}, fmt.Errorf("update campaign lead: %w", err)
	}
	return lead, nil
}

func (r *Repository) DeleteLeadByUser(ctx context.Context, userID, campaignID, leadID string) error {
	const query = `DELETE FROM campaign_leads l USING outbound_campaigns c
		WHERE l.id = $1 AND l.campaign_id = $2
			AND c.id = l.campaign_id AND c.user_id = $3`
	tag, err := r.db.Exec(ctx, query, leadID, campaignID, userID)
	if err != nil {
		return fmt.Errorf("delete campaign lead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeadNotFound
	}
	return nil
}

func scanCampaignLead(row rowScanner) (models.CampaignLead, error) {
	var lead models.CampaignLead
	err := row.Scan(&lead.ID, &lead.CampaignID, &lead.PhoneNumber, &lead.Email,
		&lead.FirstName, &lead.LastName, &lead.Status, &lead.Attempts,
		&lead.LastAttemptedAt, &lead.CreatedAt, &lead.UpdatedAt)
	return lead, err
}

func prefixedColumns(prefix, columns string) string {
	parts := strings.Split(strings.TrimSpace(columns), ",")
	for i := range parts {
		parts[i] = prefix + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}
