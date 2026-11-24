package broadcasts

// The auto-dialer. Every contact added to a broadcast is queued
// (broadcast_contacts.call_status = 'queued'); the dialer calls queued contacts
// one after another from the broadcast agent's WhatsApp number:
//
//   - one call at a time per WhatsApp number: nothing is placed while a call
//     on that number (from the dialer, the dashboard or an inbound caller) is
//     still ringing or connected, nor within callGap of the last one ending;
//   - numbers are independent, so broadcasts on different numbers dial in
//     parallel;
//   - a paused broadcast is skipped. The dialer pauses a broadcast itself,
//     with paused_reason, when the problem is the number, the agent or the
//     voice minutes rather than the contact, so one disconnected number does
//     not fail a whole list;
//   - a contact the call could not be placed to is marked failed with the
//     reason; once a placed call is over the contact becomes answered or
//     no_answer.
//
// State lives in the database only, so a restart picks up where it left off.

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/whatsapplogin"
)

const (
	dialerTick = 3 * time.Second
	// callGap is the pause between one call on a number ending and the next.
	callGap = 5 * time.Second
	// liveCallWindow ignores call rows stuck in a live status for longer than
	// any real call (a crash mid-call), so they cannot block a number forever.
	liveCallWindow = "45 minutes"
	// claimTimeout returns a contact to the queue when it was claimed but no
	// call was recorded for it (the process died while placing).
	claimTimeout = "3 minutes"
	placeTimeout = 90 * time.Second
)

// CallPlacer places outbound calls; *whatsapplogin.Manager implements it.
type CallPlacer interface {
	PlaceOutboundCall(ctx context.Context, req whatsapplogin.OutboundCall) (string, error)
}

// Dialer works through queued broadcast contacts.
type Dialer struct {
	pool   *pgxpool.Pool
	placer CallPlacer

	mu      sync.Mutex
	placing map[string]bool // phone_number_id -> a PlaceOutboundCall is in flight
}

// NewDialer creates the dialer. Start it with Run.
func NewDialer(pool *pgxpool.Pool, placer CallPlacer) *Dialer {
	return &Dialer{pool: pool, placer: placer, placing: map[string]bool{}}
}

// Run dials until ctx is cancelled.
func (d *Dialer) Run(ctx context.Context) {
	if d == nil || d.placer == nil {
		return
	}
	log.Printf("broadcast dialer: started")
	ticker := time.NewTicker(dialerTick)
	defer ticker.Stop()
	for {
		d.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type dialJob struct {
	contactID     string
	userID        string
	phoneNumber   string
	broadcastID   string
	agentID       string
	phoneNumberID string
}

func (d *Dialer) tick(ctx context.Context) {
	if err := d.settle(ctx); err != nil {
		log.Printf("broadcast dialer: settle: %v", err)
	}
	jobs, err := d.nextJobs(ctx)
	if err != nil {
		log.Printf("broadcast dialer: pick: %v", err)
		return
	}
	for _, job := range jobs {
		d.mu.Lock()
		busy := d.placing[job.phoneNumberID]
		if !busy {
			d.placing[job.phoneNumberID] = true
		}
		d.mu.Unlock()
		if busy {
			continue
		}
		claimed, err := d.claim(ctx, job.contactID)
		if err != nil || !claimed {
			if err != nil {
				log.Printf("broadcast dialer: claim contact %s: %v", job.contactID, err)
			}
			d.release(job.phoneNumberID)
			continue
		}
		go d.place(job)
	}
}

func (d *Dialer) release(phoneNumberID string) {
	d.mu.Lock()
	delete(d.placing, phoneNumberID)
	d.mu.Unlock()
}

// settle moves contacts whose call is over to answered / no_answer, and puts
// claims that never produced a call back in the queue.
func (d *Dialer) settle(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, `
		UPDATE broadcast_contacts bc
		SET call_status = CASE WHEN c.answered_at IS NOT NULL THEN 'answered' ELSE 'no_answer' END,
		    updated_at = now()
		FROM calls c
		WHERE bc.call_status = 'calling'
		  AND bc.last_call_id = c.id
		  AND (c.status NOT IN ('received', 'answered') OR c.created_at < now() - interval '`+liveCallWindow+`')`); err != nil {
		return err
	}
	_, err := d.pool.Exec(ctx, `
		UPDATE broadcast_contacts
		SET call_status = 'queued', updated_at = now()
		WHERE call_status = 'calling' AND last_call_id IS NULL
		  AND updated_at < now() - interval '`+claimTimeout+`'`)
	return err
}

// nextJobs returns, for each WhatsApp number that is free right now, the
// first queued contact of a running broadcast using it.
func (d *Dialer) nextJobs(ctx context.Context) ([]dialJob, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT DISTINCT ON (a.phone_number_id)
		       bc.id, bc.user_id, bc.phone_number, b.id, a.id, a.phone_number_id
		FROM broadcast_contacts bc
		JOIN broadcasts b ON b.id = bc.broadcast_id AND NOT b.paused
		JOIN agents a ON a.id = b.agent_id AND a.phone_number_id IS NOT NULL
		WHERE bc.call_status = 'queued'
		  AND NOT EXISTS (
		    SELECT 1 FROM calls c
		    WHERE c.phone_number_id = a.phone_number_id
		      AND ((c.status IN ('received', 'answered') AND c.created_at > now() - interval '`+liveCallWindow+`')
		           OR c.updated_at > now() - make_interval(secs => $1)))
		  AND NOT EXISTS (
		    SELECT 1 FROM broadcast_contacts x
		    JOIN broadcasts xb ON xb.id = x.broadcast_id
		    JOIN agents xa ON xa.id = xb.agent_id
		    WHERE x.call_status = 'calling' AND xa.phone_number_id = a.phone_number_id)
		ORDER BY a.phone_number_id, bc.queued_at, bc.id`, callGap.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []dialJob
	for rows.Next() {
		var j dialJob
		if err := rows.Scan(&j.contactID, &j.userID, &j.phoneNumber, &j.broadcastID, &j.agentID, &j.phoneNumberID); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (d *Dialer) claim(ctx context.Context, contactID string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `
		UPDATE broadcast_contacts
		SET call_status = 'calling', last_call_id = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1 AND call_status = 'queued'`, contactID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (d *Dialer) place(job dialJob) {
	defer d.release(job.phoneNumberID)
	ctx, cancel := context.WithTimeout(context.Background(), placeTimeout)
	defer cancel()

	callID, err := d.placer.PlaceOutboundCall(ctx, whatsapplogin.OutboundCall{
		UserID:        job.userID,
		PhoneNumberID: job.phoneNumberID,
		To:            job.phoneNumber,
		ExpectAgentID: job.agentID,
	})
	// Bookkeeping must happen even if ctx ran out while placing.
	db, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()

	if err == nil {
		if _, err := d.pool.Exec(db, `UPDATE calls SET broadcast_contact_id = $2 WHERE id = $1`, callID, job.contactID); err != nil {
			log.Printf("broadcast dialer: link call %s: %v", callID, err)
		}
		if _, err := d.pool.Exec(db, `UPDATE broadcast_contacts SET last_call_id = $2, updated_at = now() WHERE id = $1`, job.contactID, callID); err != nil {
			log.Printf("broadcast dialer: record call %s on contact %s: %v", callID, job.contactID, err)
		}
		log.Printf("broadcast dialer: broadcast %s calling contact %s (call %s)", job.broadcastID, job.contactID, callID)
		return
	}

	if reason, stop := pauseReason(err); stop {
		// Not this contact's fault: put it back first in line and stop the
		// broadcast until the user fixes the cause and resumes.
		if _, e := d.pool.Exec(db, `UPDATE broadcast_contacts SET call_status = 'queued', updated_at = now() WHERE id = $1`, job.contactID); e != nil {
			log.Printf("broadcast dialer: requeue contact %s: %v", job.contactID, e)
		}
		if _, e := d.pool.Exec(db, `UPDATE broadcasts SET paused = true, paused_reason = $2, updated_at = now() WHERE id = $1`, job.broadcastID, reason); e != nil {
			log.Printf("broadcast dialer: pause broadcast %s: %v", job.broadcastID, e)
		}
		log.Printf("broadcast dialer: paused broadcast %s: %v", job.broadcastID, err)
		return
	}

	msg := "could not place the call: " + err.Error()
	if _, e := d.pool.Exec(db, `UPDATE broadcast_contacts SET call_status = 'failed', last_error = $2, updated_at = now() WHERE id = $1`, job.contactID, truncate(msg, 300)); e != nil {
		log.Printf("broadcast dialer: fail contact %s: %v", job.contactID, e)
	}
	log.Printf("broadcast dialer: contact %s failed: %v", job.contactID, err)
}

// pauseReason maps errors that stop every call on the number to the reason
// shown on the paused broadcast.
func pauseReason(err error) (string, bool) {
	switch {
	case errors.Is(err, whatsapplogin.ErrVoiceMinutesUsedUp):
		return "Your voice minutes are used up. Buy more minutes, then resume.", true
	case errors.Is(err, whatsapplogin.ErrNumberNotConnected):
		return "The agent's WhatsApp number is not connected. Reconnect it, then resume.", true
	case errors.Is(err, whatsapplogin.ErrCallHandlerUnavailable):
		return "The agent's WhatsApp number needs reconnecting before it can call. Reconnect it, then resume.", true
	case errors.Is(err, whatsapplogin.ErrNoLiveAgent):
		return "The agent is paused or only takes inbound calls. Turn it on with outbound calls allowed, then resume.", true
	case errors.Is(err, whatsapplogin.ErrAgentMismatch):
		return "The agent no longer owns this WhatsApp number. Check the agent's number, then resume.", true
	case errors.Is(err, whatsapplogin.ErrCallingUnavailable):
		return "Calling is not available on the server right now.", true
	}
	return "", false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
