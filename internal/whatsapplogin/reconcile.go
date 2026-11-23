package whatsapplogin

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/models"
)

// reconcileInterval is how often the live instance checks that every paired
// number has a session here. A deploy runs two instances side by side for a
// moment, and they take each number's stream from one another: a number the
// outgoing instance held last dies with it, and a resume that found the pooler
// full never connected at all. Both stay offline until something reconnects
// them, which used to mean the next restart.
const reconcileInterval = 2 * time.Minute

// supersededHold is how long an instance leaves reconciling alone after
// another one replaced one of its streams. That is the outgoing instance of a
// deploy watching the incoming one take its numbers over: resuming them would
// only take them back, and fight the instance that is meant to keep them.
const supersededHold = 10 * time.Minute

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
	if time.Now().UnixNano() < m.supersededUntil.Load() {
		return
	}
	rows, err := m.repo.ListResumable(ctx)
	if err != nil {
		log.Printf("whatsapp reconcile: list resumable numbers failed: %v", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil || time.Now().UnixNano() < m.supersededUntil.Load() {
			return
		}
		session := m.getSession(row.ID)
		if session == nil {
			// Only a number whose device is stored is resumed. resumeSession
			// deletes a row with none, which is right at startup but not here: a
			// row can name a device that is mid-handover between sessions.
			if !m.hasStoredDevice(ctx, row) {
				continue
			}
			log.Printf("whatsapp reconcile: no session for paired number, resuming phone_number_id=%s", row.ID)
			if err := m.resumeSession(ctx, row); err != nil {
				log.Printf("whatsapp reconcile: resume failed phone_number_id=%s: %v", row.ID, err)
			}
			continue
		}
		if row.Status != models.PhoneNumberStatusConnected && !session.isClosing() &&
			session.client.IsConnected() && session.client.IsLoggedIn() {
			updated, err := m.repo.MarkConnected(ctx, row.UserID, row.ID, clientJID(session.client), nil)
			if err != nil {
				log.Printf("whatsapp reconcile: mark connected failed phone_number_id=%s: %v", row.ID, err)
			} else {
				session.noteConnectedAt(updated)
			}
		}
	}
}

func (m *Manager) hasStoredDevice(ctx context.Context, row models.PhoneNumber) bool {
	if row.WAJID == nil {
		return false
	}
	jid, err := types.ParseJID(strings.TrimSpace(*row.WAJID))
	if err != nil {
		return false
	}
	device, err := m.container.GetDevice(ctx, jid)
	return err == nil && device != nil
}
