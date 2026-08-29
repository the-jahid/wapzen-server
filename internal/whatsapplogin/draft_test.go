package whatsapplogin

import (
	"sync"
	"testing"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// newDraftManager builds the parts of a Manager the draft bookkeeping touches.
// Everything else — the repository, the whatsmeow container — stays out, so
// these exercise the "an unscanned QR code is not a phone number" rule without
// a database.
func newDraftManager() *Manager {
	return &Manager{
		sessions:   make(map[string]*loginSession),
		userStarts: make(map[string]*sync.Mutex),
		drafts:     make(map[string]*draftLogin),
	}
}

func (m *Manager) newTestDraft(userID string) *draftLogin {
	draft := &draftLogin{row: models.PhoneNumber{
		ID:     newLoginID(),
		UserID: userID,
		Status: models.PhoneNumberStatusPendingQR,
	}}
	m.storeDraft(draft)
	return draft
}

func TestDraftIsScopedToItsOwner(t *testing.T) {
	m := newDraftManager()
	draft := m.newTestDraft("user_123")

	if _, ok := m.Draft("user_123", draft.snapshot().ID); !ok {
		t.Fatal("owner cannot read their own unscanned login")
	}
	if _, ok := m.Draft("user_456", draft.snapshot().ID); ok {
		t.Fatal("another user can read someone else's unscanned login")
	}
}

func TestLiveDraftNeedsARunningSession(t *testing.T) {
	m := newDraftManager()
	draft := m.newTestDraft("user_123")

	// No session behind it: whatever this draft was, it is not something to
	// hand a caller asking for a code.
	if _, ok := m.liveDraft("user_123", ""); ok {
		t.Fatal("a draft with no session was offered as live")
	}

	id := draft.snapshot().ID
	session := &loginSession{phoneNumberID: id, userID: "user_123", draft: draft}
	m.storeSession(session)

	row, ok := m.liveDraft("user_123", "")
	if !ok {
		t.Fatal("the running login was not offered back")
	}
	if row.ID != id {
		t.Fatalf("live draft id = %q, want %q", row.ID, id)
	}

	// An agent editor asking for a code will not inherit a login promised to a
	// different agent.
	if _, ok := m.liveDraft("user_123", "agent_9"); ok {
		t.Fatal("a login promised elsewhere was offered to another agent")
	}
}

func TestPromotedDraftIsNoLongerPending(t *testing.T) {
	m := newDraftManager()
	draft := m.newTestDraft("user_123")
	id := draft.snapshot().ID
	m.storeSession(&loginSession{phoneNumberID: id, userID: "user_123", draft: draft})

	draft.promote(models.PhoneNumber{ID: id, UserID: "user_123", Status: models.PhoneNumberStatusConnected})
	m.dropDraft(id)

	if draft.pending() {
		t.Fatal("draft still reports pending after the scan created its row")
	}
	if _, ok := m.Draft("user_123", id); ok {
		t.Fatal("a login that became a real phone number is still served from memory")
	}
	if _, ok := m.liveDraft("user_123", ""); ok {
		t.Fatal("a scanned login was offered as a code to scan")
	}
}

func TestEndedDraftIsDroppedAfterItsGrace(t *testing.T) {
	m := newDraftManager()
	draft := m.newTestDraft("user_123")
	draft.end()

	if draft.expired(time.Now().UTC()) {
		t.Fatal("a login that just ended is already gone; the page polling it sees nothing")
	}
	if !draft.expired(time.Now().UTC().Add(draftGrace + time.Minute)) {
		t.Fatal("a long-finished login is still held in memory")
	}

	// storeDraft sweeps, so the stale one goes when the next login arrives.
	draft.mu.Lock()
	draft.endedAt = time.Now().UTC().Add(-2 * draftGrace)
	draft.mu.Unlock()
	m.newTestDraft("user_123")

	if _, ok := m.Draft("user_123", draft.snapshot().ID); ok {
		t.Fatal("the stale login was not swept")
	}
}

func TestNilDraftMeansTheRowAlreadyExists(t *testing.T) {
	var draft *draftLogin
	if draft.pending() {
		t.Fatal("a session with no draft reported that it has no row")
	}
}
