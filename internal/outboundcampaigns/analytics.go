package outboundcampaigns

import (
	"context"
	"fmt"

	"whatsapp-ai-caller-server/internal/models"
)

// maxEndReasons bounds the hangup-reason breakdown. Past a handful the tail is
// noise, and the dashboard renders it as a list rather than a chart precisely
// because it is unbounded free text.
const maxEndReasons = 6

// Analytics aggregates one campaign's performance from its leads and the calls
// it placed, over the last `days` days of daily activity. It returns ErrNotFound
// when the campaign does not exist or belongs to another user.
//
// This is four small aggregate queries rather than one: they group by different
// things (lead status, call status, day, hangup reason) and stitching them into
// a single statement would mean a cross join whose rows have to be unpicked
// again in Go. Reading them separately is both shorter and what the query
// planner handles well, each hitting an index the schema already has.
func (r *Repository) Analytics(ctx context.Context, userID, campaignID string, days int) (models.CampaignAnalytics, error) {
	if days < 1 {
		days = models.CampaignAnalyticsDefaultDays
	}
	if days > models.CampaignAnalyticsMaxDays {
		days = models.CampaignAnalyticsMaxDays
	}

	analytics := models.CampaignAnalytics{CampaignID: campaignID}

	// The ownership check rides on the lead query so a campaign that is not the
	// caller's is reported as missing before any of its numbers are read.
	const leadsQuery = `
		SELECT
			EXISTS (SELECT 1 FROM outbound_campaigns WHERE id = $1 AND user_id = $2),
			(SELECT count(*) FROM campaign_leads WHERE campaign_id = $1),
			(SELECT count(*) FROM campaign_leads WHERE campaign_id = $1 AND status = 'pending'),
			(SELECT count(*) FROM campaign_leads WHERE campaign_id = $1 AND status = 'calling'),
			(SELECT count(*) FROM campaign_leads WHERE campaign_id = $1 AND status = 'called'),
			(SELECT count(*) FROM campaign_leads WHERE campaign_id = $1 AND status = 'failed')`

	var owned bool
	leads := &analytics.Leads
	if err := r.db.QueryRow(ctx, leadsQuery, campaignID, userID).Scan(
		&owned, &leads.Total, &leads.Pending, &leads.Calling,
		&leads.Called, &leads.Failed,
	); err != nil {
		return models.CampaignAnalytics{}, fmt.Errorf("campaign lead analytics: %w", err)
	}
	if !owned {
		return models.CampaignAnalytics{}, ErrNotFound
	}

	// duration_seconds is only set once a call has ended, so the average is taken
	// over the calls that have one rather than over every call — otherwise a
	// campaign mid-dial would report its conversations as getting shorter.
	const callsQuery = `
		SELECT
			count(*),
			count(*) FILTER (WHERE status = 'received'),
			count(*) FILTER (WHERE status = 'answered'),
			count(*) FILTER (WHERE status = 'ended'),
			count(*) FILTER (WHERE status = 'declined'),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE answered_at IS NOT NULL),
			count(*) FILTER (WHERE status = 'ended' AND answered_at IS NOT NULL),
			COALESCE(sum(duration_seconds), 0)::int,
			COALESCE(round(avg(duration_seconds) FILTER (WHERE duration_seconds IS NOT NULL)), 0)::int,
			COALESCE(max(duration_seconds), 0)::int,
			min(created_at),
			max(created_at)
		FROM calls
		WHERE user_id = $2 AND campaign_id = $1`

	calls := &analytics.Calls
	talk := &analytics.TalkTime
	if err := r.db.QueryRow(ctx, callsQuery, campaignID, userID).Scan(
		&calls.Total, &calls.Received, &calls.Answered, &calls.Ended,
		&calls.Declined, &calls.Failed, &calls.Connected, &calls.Successful,
		&talk.TotalSeconds, &talk.AverageSeconds, &talk.LongestSeconds,
		&analytics.FirstCallAt, &analytics.LastCallAt,
	); err != nil {
		return models.CampaignAnalytics{}, fmt.Errorf("campaign call analytics: %w", err)
	}

	daily, err := r.dailyActivity(ctx, userID, campaignID, days)
	if err != nil {
		return models.CampaignAnalytics{}, err
	}
	analytics.Daily = daily

	reasons, err := r.endReasons(ctx, userID, campaignID)
	if err != nil {
		return models.CampaignAnalytics{}, err
	}
	analytics.EndReasons = reasons

	analytics.ApplyDerivedRates()
	return analytics, nil
}

// dailyActivity returns one row per day for the last `days` days, oldest first.
// The days come from generate_series rather than from the calls, so a day with
// no calls is a zero in the series instead of a missing point the chart would
// have to invent — a gap in calling is itself something worth seeing.
func (r *Repository) dailyActivity(ctx context.Context, userID, campaignID string, days int) ([]models.CampaignDailyActivity, error) {
	const query = `
		SELECT to_char(series.day::date, 'YYYY-MM-DD'),
			COALESCE(activity.calls, 0),
			COALESCE(activity.answered, 0)
		FROM generate_series(current_date - ($3::int - 1), current_date, interval '1 day') AS series(day)
		LEFT JOIN (
			SELECT created_at::date AS bucket,
				count(*) AS calls,
				count(*) FILTER (WHERE answered_at IS NOT NULL) AS answered
			FROM calls
			WHERE user_id = $2 AND campaign_id = $1
				AND created_at >= current_date - ($3::int - 1)
			GROUP BY 1
		) AS activity ON activity.bucket = series.day::date
		ORDER BY series.day`

	rows, err := r.db.Query(ctx, query, campaignID, userID, days)
	if err != nil {
		return nil, fmt.Errorf("campaign daily analytics: %w", err)
	}
	defer rows.Close()

	out := make([]models.CampaignDailyActivity, 0, days)
	for rows.Next() {
		var point models.CampaignDailyActivity
		if err := rows.Scan(&point.Date, &point.Calls, &point.Answered); err != nil {
			return nil, fmt.Errorf("scan campaign daily analytics: %w", err)
		}
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaign daily analytics: %w", err)
	}
	return out, nil
}

// endReasons returns the campaign's most common hangup reasons, most frequent
// first, with the reason itself as the tie-breaker so equal counts come back in
// a stable order rather than whatever the scan happened to produce.
func (r *Repository) endReasons(ctx context.Context, userID, campaignID string) ([]models.CampaignEndReason, error) {
	const query = `
		SELECT end_reason, count(*)
		FROM calls
		WHERE user_id = $2 AND campaign_id = $1 AND end_reason IS NOT NULL
		GROUP BY end_reason
		ORDER BY count(*) DESC, end_reason
		LIMIT $3`

	rows, err := r.db.Query(ctx, query, campaignID, userID, maxEndReasons)
	if err != nil {
		return nil, fmt.Errorf("campaign end reason analytics: %w", err)
	}
	defer rows.Close()

	out := make([]models.CampaignEndReason, 0)
	for rows.Next() {
		var reason models.CampaignEndReason
		if err := rows.Scan(&reason.Reason, &reason.Count); err != nil {
			return nil, fmt.Errorf("scan campaign end reason: %w", err)
		}
		out = append(out, reason)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaign end reasons: %w", err)
	}
	return out, nil
}
