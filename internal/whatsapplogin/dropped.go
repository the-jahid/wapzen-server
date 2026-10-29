package whatsapplogin

import (
	"context"
	"errors"
	"log"
	"time"

	"go.mau.fi/whatsmeow"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/phonenumbers"
)

// firstReconnectDelay is how long a dropped number waits before its first
// reconnect. whatsmeow's own auto-reconnect tries at once, which on a deploy
// is the problem: the incoming instance connecting a number is often what
// dropped the outgoing one's socket, and an instant reconnect takes the number
// straight back — the incoming instance then sees its stream replaced, gives
// the number up and holds off reconciling for supersededHold, and when the
// outgoing instance exits the number is offline until that hold runs out. The
// wait gives the incoming instance time to record its connection, which the
// check before reconnecting then sees.
const firstReconnectDelay = 5 * time.Second

// maxReconnectDelay caps the backoff between reconnect attempts.
const maxReconnectDelay = time.Minute

// disconnectGrace is how long a dropped number may stay down before its row
// says so. A deploy's handover or a blip in WhatsApp's connection is over well
// within it, and saying "disconnected" for those only alarms the user.
const disconnectGrace = 90 * time.Second

// recoverDroppedSession brings back a connected number whose socket dropped.
// It replaces whatsmeow's auto-reconnect (disabled on every session's client)
// with one that first asks whether the number is still this session's to
// keep: once another connection has marked it connected since, this session
// steps aside rather than fight for it, and never writes over the newer
// connection's status.
func (m *Manager) recoverDroppedSession(session *loginSession) {
	if !session.startRecovery() {
		return
	}
	defer session.endRecovery()

	droppedAt := time.Now()
	markedDown := false
	delay := firstReconnectDelay
	for {
		select {
		case <-session.done:
			return
		case <-time.After(delay):
		}
		if session.isClosing() || session.client.IsConnected() {
			return
		}

		switch m.droppedNumberOwner(session) {
		case ownerElsewhere:
			// Another instance holds it: this one is a deploy's outgoing
			// instance, and must not resume numbers from its reconcile loop
			// either (the same hold StreamReplaced sets).
			log.Printf("whatsapp login: dropped number was connected elsewhere since; leaving it there phone_number_id=%s", session.rowID())
			m.supersededUntil.Store(time.Now().Add(supersededHold).UnixNano())
			m.finishSession(session, false)
			return
		case ownerNone:
			log.Printf("whatsapp login: dropped number no longer names this device; not reconnecting phone_number_id=%s", session.rowID())
			m.finishSession(session, false)
			return
		}

		err := session.client.Connect()
		if err == nil || errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			// The Connected that follows marks the row connected again.
			log.Printf("whatsapp login: reconnected dropped number phone_number_id=%s", session.rowID())
			return
		}
		log.Printf("whatsapp login: reconnect of dropped number failed phone_number_id=%s: %v", session.rowID(), err)

		if !markedDown && time.Since(droppedAt) >= disconnectGrace {
			markedDown = true
			m.markDroppedIfLatest(session)
		}
		delay = min(delay*2, maxReconnectDelay)
	}
}

type droppedOwner int

const (
	// ownerThis: the number is still this session's to bring back.
	ownerThis droppedOwner = iota
	// ownerElsewhere: another connection marked it connected after this
	// session last did.
	ownerElsewhere
	// ownerNone: the row is gone or names another device (a relink).
	ownerNone
)

// droppedNumberOwner says whose a dropped number is now. A failed lookup is
// not proof of anything, so it answers ownerThis and the reconnect goes ahead.
func (m *Manager) droppedNumberOwner(session *loginSession) droppedOwner {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := m.repo.GetByUser(ctx, session.userID, session.rowID())
	if errors.Is(err, phonenumbers.ErrNotFound) {
		return ownerNone
	}
	if err != nil {
		return ownerThis
	}
	if row.WAJID == nil || *row.WAJID != session.device() {
		return ownerNone
	}
	connectedAt := session.lastConnectedAt()
	if row.LastConnectedAt != nil && !connectedAt.IsZero() && row.LastConnectedAt.After(connectedAt) {
		return ownerElsewhere
	}
	return ownerThis
}

// markDroppedIfLatest records a number that stayed down past disconnectGrace,
// unless a newer connection has been recorded for it in the meantime.
func (m *Manager) markDroppedIfLatest(session *loginSession) {
	connectedAt := session.lastConnectedAt()
	device := session.device()
	if connectedAt.IsZero() || device == "" {
		return
	}
	_, err := m.repo.MarkDisconnectedIfLatest(context.Background(), session.userID, session.rowID(), device, connectedAt)
	if err != nil && !errors.Is(err, phonenumbers.ErrNotFound) {
		log.Printf("whatsapp login: mark disconnected failed for phone_number_id=%s: %v", session.rowID(), err)
	}
}

// rowID is the phone_numbers row this session serves. A login whose scan
// relinked a number the account already had was promoted to that number's
// row, which keeps its own id.
func (s *loginSession) rowID() string {
	if s.draft != nil {
		return s.draft.snapshot().ID
	}
	return s.phoneNumberID
}

func (s *loginSession) noteConnectedAt(row models.PhoneNumber) {
	if row.LastConnectedAt == nil {
		return
	}
	s.mu.Lock()
	s.connectedAt = *row.LastConnectedAt
	s.mu.Unlock()
}

func (s *loginSession) lastConnectedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectedAt
}

func (s *loginSession) startRecovery() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recovering {
		return false
	}
	s.recovering = true
	return true
}

func (s *loginSession) endRecovery() {
	s.mu.Lock()
	s.recovering = false
	s.mu.Unlock()
}
