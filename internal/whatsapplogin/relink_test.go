package whatsapplogin

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/models"
)

type relinkRepoSpy struct {
	phoneNumberRepository
	existing    models.PhoneNumber
	markedID    string
	markedWAJID string
}

func (s *relinkRepoSpy) GetByUserPhone(_ context.Context, userID, phoneNumber string) (models.PhoneNumber, error) {
	if userID != s.existing.UserID || phoneNumber != *s.existing.PhoneNumber {
		return models.PhoneNumber{}, context.Canceled
	}
	return s.existing, nil
}

func (s *relinkRepoSpy) MarkConnected(_ context.Context, _, phoneNumberID, waJID string, _ *string) (models.PhoneNumber, error) {
	s.markedID, s.markedWAJID = phoneNumberID, waJID
	row := s.existing
	row.WAJID = &waJID
	row.Status = models.PhoneNumberStatusConnected
	return row, nil
}

// Rescanning a number the account already has moves that number onto the new
// device instead of refusing the scan, and the page polling the login reads
// the existing number connecting.
func TestRescanRelinksTheExistingNumber(t *testing.T) {
	phone := "+962792010132"
	repo := &relinkRepoSpy{existing: models.PhoneNumber{
		ID: "existing-row", UserID: "user_123", PhoneNumber: &phone, Status: models.PhoneNumberStatusDisconnected,
	}}
	m := newDraftManager()
	m.repo = repo
	draft := m.newTestDraft("user_123")
	draftID := draft.snapshot().ID
	jid := types.NewADJID("962792010132", 0, 7)
	session := &loginSession{
		phoneNumberID: draftID,
		userID:        "user_123",
		draft:         draft,
		client:        &whatsmeow.Client{Store: &store.Device{ID: &jid}},
		cancel:        func() {},
		done:          make(chan struct{}),
	}
	m.storeSession(session)

	row, err := m.relinkExistingNumber(session, jid)
	if err != nil {
		t.Fatalf("relinkExistingNumber() error = %v", err)
	}
	if repo.markedID != "existing-row" || repo.markedWAJID != jid.String() {
		t.Fatalf("MarkConnected(%q, %q), want the existing row on the new device", repo.markedID, repo.markedWAJID)
	}
	if row.ID != "existing-row" {
		t.Fatalf("relinked row id = %q", row.ID)
	}
	if draft.pending() {
		t.Fatal("the login still waits for a scan after relinking")
	}
	polled, ok := m.Draft("user_123", draftID)
	if !ok || polled.ID != "existing-row" || polled.Status != models.PhoneNumberStatusConnected {
		t.Fatalf("polling the login returned %+v, %t; want the existing number connected", polled, ok)
	}
}
