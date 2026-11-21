package whatsapplogin

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/phonenumbers"
)

type deletePhoneNumberSpy struct {
	phoneNumberRepository
	userID        string
	phoneNumberID string
	waJID         string
	calls         int
	// rowJID is the device the stored row names; a delete for any other
	// device finds nothing, as the WHERE wa_jid guard does.
	rowJID string
}

func (s *deletePhoneNumberSpy) DeleteByUserDevice(_ context.Context, userID, phoneNumberID, waJID string) (models.PhoneNumber, error) {
	s.calls++
	s.userID = userID
	s.phoneNumberID = phoneNumberID
	s.waJID = waJID
	if waJID != s.rowJID {
		return models.PhoneNumber{}, phonenumbers.ErrNotFound
	}
	return models.PhoneNumber{}, nil
}

func TestDeleteLoggedOutPhoneNumberIsScopedToSessionNumber(t *testing.T) {
	repo := &deletePhoneNumberSpy{rowJID: "15550001111:7@s.whatsapp.net"}
	m := &Manager{repo: repo}
	session := &loginSession{userID: "user_123", phoneNumberID: "phone_123", deviceJID: "15550001111:7@s.whatsapp.net"}

	if err := m.deleteLoggedOutPhoneNumber(session); err != nil {
		t.Fatalf("deleteLoggedOutPhoneNumber() error = %v", err)
	}
	if repo.calls != 1 || repo.userID != "user_123" || repo.phoneNumberID != "phone_123" || repo.waJID != "15550001111:7@s.whatsapp.net" {
		t.Fatalf("DeleteByUserDevice calls=%d user=%q phone=%q jid=%q", repo.calls, repo.userID, repo.phoneNumberID, repo.waJID)
	}
}

// A relink repoints the row to the new device before unlinking the old one.
// The old device's LoggedOut (in the other instance, on a deploy) must not
// delete the relinked number.
func TestLoggedOutOldDeviceKeepsRelinkedNumber(t *testing.T) {
	repo := &deletePhoneNumberSpy{rowJID: "15550001111:9@s.whatsapp.net"}
	m := &Manager{repo: repo}
	session := &loginSession{userID: "user_123", phoneNumberID: "phone_123", deviceJID: "15550001111:7@s.whatsapp.net"}

	if err := m.deleteLoggedOutPhoneNumber(session); err != nil {
		t.Fatalf("deleteLoggedOutPhoneNumber() error = %v", err)
	}
	if repo.calls != 1 || repo.waJID != "15550001111:7@s.whatsapp.net" {
		t.Fatalf("DeleteByUserDevice calls=%d jid=%q, want one guarded delete for the old device", repo.calls, repo.waJID)
	}
}

func TestLoggedOutUnpairedLoginDeletesNothing(t *testing.T) {
	repo := &deletePhoneNumberSpy{}
	m := &Manager{repo: repo}

	if err := m.deleteLoggedOutPhoneNumber(&loginSession{userID: "user_123", phoneNumberID: "draft_1"}); err != nil {
		t.Fatalf("deleteLoggedOutPhoneNumber() error = %v", err)
	}
	if repo.calls != 0 {
		t.Fatalf("a login with no device deleted %d row(s)", repo.calls)
	}
}

type markDisconnectedSpy struct {
	phoneNumberRepository
	calls int
}

func (s *markDisconnectedSpy) MarkDisconnected(context.Context, string, string) (models.PhoneNumber, error) {
	s.calls++
	return models.PhoneNumber{}, nil
}

// On a deploy the incoming instance replaces the outgoing one's stream after
// it has already marked the number connected; the outgoing one must not
// overwrite that with "disconnected".
func TestStreamReplacedLeavesTheRowToTheNewConnection(t *testing.T) {
	repo := &markDisconnectedSpy{}
	m := &Manager{repo: repo, sessions: make(map[string]*loginSession)}
	session := &loginSession{
		userID:        "user_123",
		phoneNumberID: "phone_123",
		client:        &whatsmeow.Client{},
		cancel:        func() {},
		done:          make(chan struct{}),
		connected:     true,
	}
	m.storeSession(session)

	m.handleEvent(session, &events.StreamReplaced{})

	if repo.calls != 0 {
		t.Fatalf("stream replaced wrote disconnected %d time(s)", repo.calls)
	}
	if m.getSession("phone_123") != nil {
		t.Fatal("the replaced session is still registered")
	}
}
