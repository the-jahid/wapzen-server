package activity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository saves activity entries to the activity_logs table, and QR scan
// entries to qr_scan_attempts as well.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Insert saves one entry. An empty Detail is stored as NULL, and an empty
// Email is filled in from the user (a resumed number's session has none). A QR
// scan entry also starts or settles its qr_scan_attempts row in the same round
// trip.
func (r *Repository) Insert(ctx context.Context, entry Entry) error {
	batch := &pgx.Batch{}
	batch.Queue(`
		INSERT INTO activity_logs (user_id, email, event, message, detail)
		VALUES ($1, COALESCE(NULLIF($2, ''), (SELECT email FROM users WHERE id = $1), ''), $3, $4, NULLIF($5, ''))`,
		entry.UserID, entry.Email, string(entry.Event), entry.Message, entry.Detail,
	)
	if entry.Attempt != nil && entry.Attempt.ID != "" {
		queueAttempt(batch, entry)
	}
	return r.pool.SendBatch(ctx, batch).Close()
}

// AbandonedReason is the failure_reason given to an attempt no process will
// settle any more.
const AbandonedReason = "the server restarted before the scan finished"

// SettleAbandonedAttempts fails every attempt still open after olderThan. A
// login lives in one process's memory, so a deploy or crash ends it without an
// outcome and its row would otherwise read "started" forever. olderThan must
// outlast the longest login, so the other instance of a deploy keeps its live
// ones.
func (r *Repository) SettleAbandonedAttempts(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE qr_scan_attempts
		SET status = 'failed', failure_reason = $2, finished_at = now()
		WHERE status = 'started' AND started_at < now() - make_interval(secs => $1)`,
		olderThan.Seconds(), AbandonedReason,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// attemptStatus is the qr_scan_attempts.status an outcome event settles on.
var attemptStatus = map[Event]string{
	QRScanWorked:    "worked",
	QRScanFailed:    "failed",
	QRScanCancelled: "cancelled",
}

// queueAttempt writes the attempt row for a QR scan entry. Both statements
// upsert because each entry is saved on its own goroutine: an outcome can land
// before its start, and the start must then not reset it. An attempt settles
// once; a later outcome for a settled row changes nothing.
func queueAttempt(batch *pgx.Batch, entry Entry) {
	attempt := entry.Attempt
	if entry.Event == QRScanStarted {
		batch.Queue(`
			INSERT INTO qr_scan_attempts (id, user_id, email, phone_number_id, agent_id, method, user_agent)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), COALESCE(NULLIF($6, ''), 'qr'), NULLIF($7, ''))
			ON CONFLICT (id) DO UPDATE SET
				phone_number_id = EXCLUDED.phone_number_id,
				agent_id = EXCLUDED.agent_id,
				method = EXCLUDED.method,
				user_agent = EXCLUDED.user_agent`,
			attempt.ID, entry.UserID, entry.Email, attempt.PhoneNumberID, attempt.AgentID, attempt.Method, attempt.UserAgent,
		)
		return
	}
	status, ok := attemptStatus[entry.Event]
	if !ok {
		return
	}
	// A worked scan's detail is the number it linked; any other outcome's is
	// the reason it ended.
	number, reason := "", entry.Detail
	if entry.Event == QRScanWorked {
		number, reason = entry.Detail, ""
	}
	batch.Queue(`
		INSERT INTO qr_scan_attempts (id, user_id, email, status, phone_number, failure_reason, finished_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), now())
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			phone_number = EXCLUDED.phone_number,
			failure_reason = EXCLUDED.failure_reason,
			finished_at = EXCLUDED.finished_at
		WHERE qr_scan_attempts.status = 'started'`,
		attempt.ID, entry.UserID, entry.Email, status, number, reason,
	)
}
