package whatsapplogin

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// Only a number with an active agent keeps a WhatsApp session open. Calls need
// one both ways (resolveCallAgent refuses a number without it), so a number
// with no active agent would only hold memory and a socket. Such an idle
// number stays linked (its device stays in the store and its row keeps its
// status) and opens again when an agent on it goes live (OpenForAgent), and
// once a week for a moment so WhatsApp never treats the device as abandoned.

// idleCheckInterval is how often the open sessions are checked against the
// agents.
const idleCheckInterval = time.Minute

// pairedKeepOpen is how long a freshly paired number stays open whatever its
// agent says: the owner is on the dashboard setting it up.
const pairedKeepOpen = 15 * time.Minute

// checkInEvery is how long an idle number goes unconnected before it is opened
// for checkInKeepOpen. At most checkInsPerPass a minute, so numbers that went
// idle together don't all reconnect in the same minute.
const (
	checkInEvery    = 7 * 24 * time.Hour
	checkInKeepOpen = 3 * time.Minute
	checkInsPerPass = 3
)

func (s *loginSession) keepOpenFor(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until := time.Now().Add(d); until.After(s.keepUntil) {
		s.keepUntil = until
	}
}

func (s *loginSession) keptOpen(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Before(s.keepUntil)
}

// activeNumbers returns the numbers with an active agent. filtered is false
// when that can't be known (no agents repository, or the lookup failed), and
// then nothing is treated as idle: a failed read must never close a number
// that answers calls.
func (m *Manager) activeNumbers(ctx context.Context) (active map[string]bool, filtered bool) {
	if m.agentsRepo == nil {
		return nil, false
	}
	active, err := m.agentsRepo.ActivePhoneNumberIDs(ctx)
	if err != nil {
		log.Printf("whatsapp idle: list numbers with an active agent failed: %v", err)
		return nil, false
	}
	return active, true
}

// KeepIdleNumbersClosed closes, every idleCheckInterval, each open number with
// no active agent, and gives idle numbers their weekly check-in. Closing runs
// everywhere; the check-in opens numbers, so like KeepNumbersConnected it runs
// only on the production server (RENDER set), never on a local server sharing
// the database.
func (m *Manager) KeepIdleNumbersClosed(ctx context.Context) {
	if m == nil {
		return
	}
	checkIns := os.Getenv("RENDER") != ""
	go func() {
		tried := make(map[string]time.Time)
		ticker := time.NewTicker(idleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.closeIdleSessions(ctx)
				if checkIns {
					m.checkInIdleNumbers(ctx, tried)
				}
			}
		}
	}()
}

// closeIdleSessions closes every paired session whose number has no active
// agent, unless it is kept open for now or has a call in progress. The row's
// status is left as it is: the number is still linked, only not open.
func (m *Manager) closeIdleSessions(ctx context.Context) {
	// Sessions first, agents second: a number opened for an agent that went
	// live after this snapshot isn't in it, and one opened before it had its
	// agent saved first (OpenForAgent runs after the write), so the read below
	// sees that agent active. The other order could close it straight away.
	m.mu.RLock()
	sessions := make([]*loginSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	active, filtered := m.activeNumbers(ctx)
	if !filtered {
		return
	}
	now := time.Now()

	closed := 0
	for _, session := range sessions {
		if active[session.phoneNumberID] || session.isClosing() || session.keptOpen(now) {
			continue
		}
		// A login still waiting for its scan, or one whose row isn't written
		// yet, is never cut off.
		if session.draft.pending() || !session.paired() {
			continue
		}
		if m.voice != nil && m.voice.HasLiveCallOn(session.phoneNumberID) {
			continue
		}
		m.finishSession(session, true)
		closed++
	}
	if closed > 0 {
		log.Printf("whatsapp idle: closed %d number(s) with no active agent", closed)
	}
}

// checkInIdleNumbers opens, for checkInKeepOpen, idle numbers that haven't
// connected in checkInEvery. tried remembers each attempt, so a number that
// never opens is tried once a week rather than every minute.
func (m *Manager) checkInIdleNumbers(ctx context.Context, tried map[string]time.Time) {
	active, filtered := m.activeNumbers(ctx)
	if !filtered {
		return
	}
	rows, err := m.repo.ListResumable(ctx)
	if err != nil {
		log.Printf("whatsapp idle: list linked numbers failed: %v", err)
		return
	}
	now := time.Now()
	opened := 0
	for _, row := range rows {
		if opened >= checkInsPerPass || ctx.Err() != nil {
			return
		}
		if active[row.ID] || m.getSession(row.ID) != nil {
			continue
		}
		lastSeen := tried[row.ID]
		if row.LastConnectedAt != nil && row.LastConnectedAt.After(lastSeen) {
			lastSeen = *row.LastConnectedAt
		}
		if now.Sub(lastSeen) < checkInEvery || !m.hasStoredDevice(ctx, row) {
			continue
		}
		tried[row.ID] = now
		opened++
		if err := m.resumeSession(ctx, row); err != nil {
			log.Printf("whatsapp idle: weekly check-in failed phone_number_id=%s: %v", row.ID, err)
			continue
		}
		if session := m.getSession(row.ID); session != nil {
			session.keepOpenFor(checkInKeepOpen)
		}
		log.Printf("whatsapp idle: weekly check-in phone_number_id=%s", row.ID)
	}
}

// OpenForAgent opens phoneNumberID right away when an agent on it has just
// gone live, rather than leaving it closed as idle. Runs in the background;
// nothing happens if the number is open already, isn't the user's, or has no
// stored device (a number that needs a new scan is not opened here: resuming
// it would delete its row).
func (m *Manager) OpenForAgent(userID, phoneNumberID string) {
	if m == nil {
		return
	}
	phoneNumberID = strings.TrimSpace(phoneNumberID)
	if phoneNumberID == "" || m.getSession(phoneNumberID) != nil {
		return
	}
	go func() {
		ctx := context.Background()
		row, err := m.repo.GetByUser(ctx, userID, phoneNumberID)
		if err != nil {
			log.Printf("whatsapp idle: open for agent: load number failed phone_number_id=%s: %v", phoneNumberID, err)
			return
		}
		if row.Status != models.PhoneNumberStatusConnected && row.Status != models.PhoneNumberStatusDisconnected {
			return
		}
		if m.getSession(row.ID) != nil || !m.hasStoredDevice(ctx, row) {
			return
		}
		if err := m.resumeSession(ctx, row); err != nil {
			log.Printf("whatsapp idle: open for agent failed phone_number_id=%s: %v", phoneNumberID, err)
			return
		}
		log.Printf("whatsapp idle: opened phone_number_id=%s for its agent", phoneNumberID)
	}()
}
