package whatsapplogin

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"whatsapp-ai-caller-server/internal/models"
)

type deletePhoneNumberSpy struct {
	phoneNumberRepository
	userID        string
	phoneNumberID string
	calls         int
}

func (s *deletePhoneNumberSpy) DeleteByUser(_ context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	s.calls++
	s.userID = userID
	s.phoneNumberID = phoneNumberID
	return models.PhoneNumber{}, nil
}

func TestDeleteLoggedOutPhoneNumberIsScopedToSessionNumber(t *testing.T) {
	repo := &deletePhoneNumberSpy{}
	m := &Manager{
		repo: repo,
		aiHistory: map[string][]aiMessage{
			"agent_1|phone_123|peer_1": {{Role: "user", Content: "delete me"}},
			"agent_1|phone_456|peer_1": {{Role: "user", Content: "keep me"}},
		},
	}
	session := &loginSession{userID: "user_123", phoneNumberID: "phone_123"}

	if err := m.deleteLoggedOutPhoneNumber(session); err != nil {
		t.Fatalf("deleteLoggedOutPhoneNumber() error = %v", err)
	}
	if repo.calls != 1 || repo.userID != "user_123" || repo.phoneNumberID != "phone_123" {
		t.Fatalf("DeleteByUser calls=%d user=%q phone=%q", repo.calls, repo.userID, repo.phoneNumberID)
	}
	if _, ok := m.aiHistory["agent_1|phone_123|peer_1"]; ok {
		t.Fatal("logged-out number's conversation history was not deleted")
	}
	if _, ok := m.aiHistory["agent_1|phone_456|peer_1"]; !ok {
		t.Fatal("another number's conversation history was deleted")
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
