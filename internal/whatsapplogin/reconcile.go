package whatsapplogin

import (
	"context"
	"log"
	"os"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// reconcileInterval is how often the live instance checks that every paired
// number has a session here. A deploy runs two instances side by side for a
// moment, and they take each number's stream from one another: a number the
// outgoing instance held last dies with it, and a resume that found the pooler
// full never connected at all. Both stay offline until something reconnects
// them, which used to mean the next restart.
const reconcileInterval = 2 * time.Minute

// KeepNumbersConnected resumes, every reconcileInterval, each paired number
// this process has no session for, and corrects a live number whose row says
// disconnected. It runs only on Render (which sets RENDER): a local server on
// the same database would otherwise take the numbers back and forth with
// production for as long as both run.
func (m *Manager) KeepNumbersConnected(ctx context.Context) {
	if m == nil || os.Getenv("RENDER") == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(reconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.reconcileSessions(ctx)
			}
		}
	}()
}

func (m *Manager) reconcileSessions(ctx context.Context) {
	rows, err := m.repo.ListResumable(ctx)
	if err != nil {
		log.Printf("whatsapp reconcile: list resumable numbers failed: %v", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		session := m.getSession(row.ID)
		if session == nil {
			log.Printf("whatsapp reconcile: no session for paired number, resuming phone_number_id=%s", row.ID)
			if err := m.resumeSession(ctx, row); err != nil {
				log.Printf("whatsapp reconcile: resume failed phone_number_id=%s: %v", row.ID, err)
			}
			continue
		}
		if row.Status != models.PhoneNumberStatusConnected && !session.isClosing() &&
			session.client.IsConnected() && session.client.IsLoggedIn() {
			if _, err := m.repo.MarkConnected(ctx, row.UserID, row.ID, clientJID(session.client), nil); err != nil {
				log.Printf("whatsapp reconcile: mark connected failed phone_number_id=%s: %v", row.ID, err)
			}
		}
	}
}
