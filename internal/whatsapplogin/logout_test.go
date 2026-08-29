package whatsapplogin

import (
	"context"
	"testing"

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
