package whatsapplogin

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/phonenumbers"
)

const droppedDevice = "15550001111:7@s.whatsapp.net"

type droppedRepoSpy struct {
	phoneNumberRepository
	row           models.PhoneNumber
	getErr        error
	plainMarks    int
	guardedMarks  int
	guardedDevice string
	guardedSince  time.Time
}

func (s *droppedRepoSpy) GetByUser(context.Context, string, string) (models.PhoneNumber, error) {
	return s.row, s.getErr
}

func (s *droppedRepoSpy) MarkDisconnected(context.Context, string, string) (models.PhoneNumber, error) {
	s.plainMarks++
	return models.PhoneNumber{}, nil
}

func (s *droppedRepoSpy) MarkDisconnectedIfLatest(_ context.Context, _, _, device string, since time.Time) (models.PhoneNumber, error) {
	s.guardedMarks++
	s.guardedDevice = device
	s.guardedSince = since
	return models.PhoneNumber{}, nil
}

func droppedSession(connectedAt time.Time) *loginSession {
	return &loginSession{
		userID:        "user_123",
		phoneNumberID: "phone_123",
		client:        &whatsmeow.Client{},
		cancel:        func() {},
		done:          make(chan struct{}),
		connected:     true,
		deviceJID:     droppedDevice,
		connectedAt:   connectedAt,
	}
}

// A dropped socket is a restart's handover or a blip far more often than a
// number going offline; the row must not say "disconnected" straight away.
func TestDisconnectedDoesNotMarkTheRowRightAway(t *testing.T) {
	repo := &droppedRepoSpy{}
	m := &Manager{repo: repo, sessions: make(map[string]*loginSession)}
	session := droppedSession(time.Now())
	m.storeSession(session)

	m.handleEvent(session, &events.Disconnected{})

	if repo.plainMarks != 0 || repo.guardedMarks != 0 {
		t.Fatalf("a dropped socket marked the row disconnected at once (%d plain, %d guarded)", repo.plainMarks, repo.guardedMarks)
	}
	m.finishSession(session, false)
}

func TestDroppedNumberOwner(t *testing.T) {
	ours := time.Date(2026, 10, 29, 18, 11, 0, 0, time.UTC)
	later := ours.Add(3 * time.Second)
	device := droppedDevice
	other := "15550001111:9@s.whatsapp.net"

	tests := []struct {
		name   string
		row    models.PhoneNumber
		getErr error
		want   droppedOwner
	}{
		{"still ours", models.PhoneNumber{WAJID: &device, LastConnectedAt: &ours}, nil, ownerThis},
		{"connected elsewhere since", models.PhoneNumber{WAJID: &device, LastConnectedAt: &later}, nil, ownerElsewhere},
		{"relinked onto another device", models.PhoneNumber{WAJID: &other, LastConnectedAt: &later}, nil, ownerNone},
		{"row deleted", models.PhoneNumber{}, phonenumbers.ErrNotFound, ownerNone},
		{"lookup failed", models.PhoneNumber{}, context.DeadlineExceeded, ownerThis},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{repo: &droppedRepoSpy{row: tt.row, getErr: tt.getErr}}
			if got := m.droppedNumberOwner(droppedSession(ours)); got != tt.want {
				t.Fatalf("droppedNumberOwner() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The disconnected write carries this session's device and connection time,
// so the database leaves a newer connection's "connected" alone.
func TestMarkDroppedIfLatestIsGuardedByThisConnection(t *testing.T) {
	connectedAt := time.Date(2026, 10, 29, 18, 11, 0, 0, time.UTC)
	repo := &droppedRepoSpy{}
	m := &Manager{repo: repo}

	m.markDroppedIfLatest(droppedSession(connectedAt))

	if repo.plainMarks != 0 {
		t.Fatal("an unguarded disconnected write was made")
	}
	if repo.guardedMarks != 1 || repo.guardedDevice != droppedDevice || !repo.guardedSince.Equal(connectedAt) {
		t.Fatalf("guarded write = %d call(s), device %q, since %v", repo.guardedMarks, repo.guardedDevice, repo.guardedSince)
	}

	// A session that never recorded a connection has nothing to guard with.
	m.markDroppedIfLatest(droppedSession(time.Time{}))
	if repo.guardedMarks != 1 {
		t.Fatal("a session with no recorded connection marked the row disconnected")
	}
}
