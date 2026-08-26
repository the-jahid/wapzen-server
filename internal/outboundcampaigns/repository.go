// Package outboundcampaigns persists outbound calling campaigns into the
// outbound_campaigns table, along with the campaign_leads they hold. A campaign
// is a named batch of outbound calling owned by a user, dialled with one of that
// user's agents — the agent carries the number it speaks on, so a campaign never
// stores a number of its own.
//
// The campaign's counters live on its row but are not written here: a database
// trigger advances them from the calls the campaign places (migration 00040), so
// they cannot drift from the calls they describe. This package reads them back,
// and Analytics aggregates the leads and calls directly.
package outboundcampaigns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrNotFound is returned when a campaign does not exist or is owned by another
// user. The two are deliberately indistinguishable to the caller.
var ErrNotFound = errors.New("outbound campaign not found")

// ErrNameTaken is returned when the owner already has a campaign with the
// requested name. Names are unique per user, not globally.
var ErrNameTaken = errors.New("outbound campaign name already in use")

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-index conflict.
const uniqueViolation = "23505"

// campaignColumns is the select list of a campaign row, in the order
// scanCampaign reads them. Shared by every query that returns a row so a schema
// change is made in one place.
//
// The last three columns are not the campaign's own: the agent carries the
// number it speaks on, so its name and number are read through campaignJoins on
// every query rather than copied onto the campaign, where they could go stale
// the moment the agent was reassigned. Every column is therefore qualified, and
// callers alias outbound_campaigns as c.
const campaignColumns = `
	c.id, c.user_id, c.campaign_name, c.status, c.budget_usd,
	c.leads_count, c.calls_placed, c.answered_calls, c.successful_calls,
	c.today_calls, c.today_calls_date, c.total_usage_seconds,
	c.started_at, c.completed_at, c.created_at, c.updated_at,
	c.agent_id, a.agent_name, a.phone_number_id, p.phone_number
`

// campaignJoins resolves a campaign's agent and, through it, the phone number
// the agent speaks on. Both joins are outer: a campaign need not have an agent,
// and an agent need not have been assigned a number yet.
const campaignJoins = `
	LEFT JOIN agents a ON a.id = c.agent_id
	LEFT JOIN phone_numbers p ON p.id = a.phone_number_id
`

type dbQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Repository owns persistence for the outbound_campaigns table.
type Repository struct {
	db dbQuerier
	// now is the clock used to stamp lifecycle timestamps and to decide whether
	// today_calls is stale. It is a field so tests can pin it.
	now func() time.Time
}

// NewRepository creates an outbound campaign repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{db: pool, now: time.Now}
}

// Create inserts a campaign owned by params.UserID and returns the stored row.
// The status, the counters and the timestamps are left to the column defaults,
// so a new campaign is always a draft with everything at zero; a nil budget is
// stored as NULL, which is the uncapped setting rather than a budget of zero.
func (r *Repository) Create(ctx context.Context, params models.NewOutboundCampaign) (models.OutboundCampaign, error) {
	// The insert is wrapped in a CTE because RETURNING cannot join: the agent's
	// name and number are read from the inserted row's agent, so the created
	// campaign comes back in exactly the shape every other query returns.
	query := fmt.Sprintf(`
		WITH inserted AS (
			INSERT INTO outbound_campaigns (user_id, campaign_name, budget_usd, agent_id)
			VALUES ($1, $2, $3, $4)
			RETURNING *
		)
		SELECT %s
		FROM inserted c
		%s
	`, campaignColumns, campaignJoins)

	row, err := r.scanCampaign(r.db.QueryRow(ctx, query, params.UserID, params.Name, params.BudgetUSD, params.AgentID))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.OutboundCampaign{}, ErrNameTaken
		}
		return models.OutboundCampaign{}, fmt.Errorf("create outbound campaign: %w", err)
	}
	return row, nil
}

// ListByUser returns one page of the user's campaigns, newest first, together
// with the total number matching so the caller can build pagination metadata.
// A non-empty status narrows both the page and the total, so the metadata
// describes the filtered collection rather than the whole one.
//
// The returned slice is always non-nil so an empty page serializes as [] rather
// than null.
func (r *Repository) ListByUser(ctx context.Context, userID, status string, limit, offset int) ([]models.OutboundCampaign, int, error) {
	where := "c.user_id = $1"
	args := []any{userID}
	if status != "" {
		where += " AND c.status = $2"
		args = append(args, status)
	}

	var total int
	countQuery := "SELECT count(*) FROM outbound_campaigns c WHERE " + where
	if err := r.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count outbound campaigns: %w", err)
	}

	out := make([]models.OutboundCampaign, 0)
	if total == 0 {
		return out, 0, nil
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM outbound_campaigns c
		%s
		WHERE %s
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT $%d OFFSET $%d
	`, campaignColumns, campaignJoins, where, len(args)+1, len(args)+2)

	rows, err := r.db.Query(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list outbound campaigns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		campaign, err := r.scanCampaign(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan outbound campaign: %w", err)
		}
		out = append(out, campaign)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate outbound campaigns: %w", err)
	}
	return out, total, nil
}

// GetByUser loads one campaign by id, scoped to the authenticated user. It
// returns ErrNotFound when no such campaign exists for that user.
func (r *Repository) GetByUser(ctx context.Context, userID, id string) (models.OutboundCampaign, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM outbound_campaigns c
		%s
		WHERE c.id = $1 AND c.user_id = $2
	`, campaignColumns, campaignJoins)

	campaign, err := r.scanCampaign(r.db.QueryRow(ctx, query, id, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.OutboundCampaign{}, ErrNotFound
		}
		return models.OutboundCampaign{}, fmt.Errorf("get outbound campaign: %w", err)
	}
	return campaign, nil
}

// UpdateByUser applies a partial update to one campaign owned by the
// authenticated user and returns the stored row. Only the non-nil fields of
// params are written; ClearBudget writes NULL, which is how a spend cap is
// removed.
//
// The lifecycle timestamps follow the status rather than being supplied:
// started_at is stamped the first time a campaign goes running (COALESCE keeps
// the original start across a pause and resume), and completed_at is stamped
// when it reaches a terminal status. The legal transitions themselves are
// checked by the handler against the stored row, since deciding them here would
// mean re-reading it.
func (r *Repository) UpdateByUser(ctx context.Context, userID, id string, params models.OutboundCampaignUpdate) (models.OutboundCampaign, error) {
	if params.IsEmpty() {
		return models.OutboundCampaign{}, fmt.Errorf("update outbound campaign: no fields to update")
	}

	set := []string{"updated_at = now()"}
	args := []any{id, userID}
	next := 3
	addField := func(column string, value any) {
		set = append(set, fmt.Sprintf("%s = $%d", column, next))
		args = append(args, value)
		next++
	}

	if params.Name != nil {
		addField("campaign_name", *params.Name)
	}
	if params.ClearBudget {
		set = append(set, "budget_usd = NULL")
	} else if params.BudgetUSD != nil {
		addField("budget_usd", *params.BudgetUSD)
	}
	if params.ClearAgent {
		set = append(set, "agent_id = NULL")
	} else if params.AgentID != nil {
		addField("agent_id", *params.AgentID)
	}
	if params.Status != nil {
		addField("status", *params.Status)
		switch {
		case *params.Status == models.CampaignStatusRunning:
			set = append(set, "started_at = COALESCE(started_at, now())")
		case models.IsTerminalCampaignStatus(*params.Status):
			set = append(set, "completed_at = now()")
		}
	}

	// As in Create, the write is wrapped in a CTE so the updated row can be
	// returned with its agent's name and number joined in.
	query := fmt.Sprintf(`
		WITH updated AS (
			UPDATE outbound_campaigns
			SET %s
			WHERE id = $1 AND user_id = $2
			RETURNING *
		)
		SELECT %s
		FROM updated c
		%s
	`, strings.Join(set, ", "), campaignColumns, campaignJoins)

	row, err := r.scanCampaign(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.OutboundCampaign{}, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return models.OutboundCampaign{}, ErrNameTaken
		}
		return models.OutboundCampaign{}, fmt.Errorf("update outbound campaign: %w", err)
	}
	return row, nil
}

// DeleteByUser removes one campaign owned by the authenticated user. It returns
// ErrNotFound when no such campaign exists for that user, so deleting somebody
// else's reads the same as deleting one that never existed.
func (r *Repository) DeleteByUser(ctx context.Context, userID, id string) error {
	const query = `DELETE FROM outbound_campaigns WHERE id = $1 AND user_id = $2`
	tag, err := r.db.Exec(ctx, query, id, userID)
	if err != nil {
		return fmt.Errorf("delete outbound campaign: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanCampaign reads one row and fills in the derived fields, so every campaign
// leaving this package carries its rates and a today's counter that is only
// reported when it is actually today's.
func (r *Repository) scanCampaign(row rowScanner) (models.OutboundCampaign, error) {
	var (
		campaign       models.OutboundCampaign
		todayCallsDate *time.Time
	)
	if err := row.Scan(
		&campaign.ID,
		&campaign.UserID,
		&campaign.Name,
		&campaign.Status,
		&campaign.BudgetUSD,
		&campaign.LeadsCount,
		&campaign.CallsPlaced,
		&campaign.AnsweredCalls,
		&campaign.SuccessfulCalls,
		&campaign.TodayCalls,
		&todayCallsDate,
		&campaign.TotalUsageSeconds,
		&campaign.StartedAt,
		&campaign.CompletedAt,
		&campaign.CreatedAt,
		&campaign.UpdatedAt,
		&campaign.AgentID,
		&campaign.AgentName,
		&campaign.PhoneNumberID,
		&campaign.PhoneNumber,
	); err != nil {
		return models.OutboundCampaign{}, err
	}
	if todayCallsDate != nil {
		formatted := todayCallsDate.Format("2006-01-02")
		campaign.TodayCallsDate = &formatted
	}
	campaign.ApplyDerivedFields(r.now())
	return campaign, nil
}
