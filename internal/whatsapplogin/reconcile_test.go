package whatsapplogin

import (
	"context"
	"testing"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

type listResumableSpy struct {
	phoneNumberRepository
	calls int
}

func (s *listResumableSpy) ListResumable(context.Context) ([]models.PhoneNumber, error) {
	s.calls++
	return nil, nil
}

// The outgoing instance of a deploy sees its streams replaced; it must not
// then resume the numbers the incoming instance just took over.
func TestSupersededInstanceDoesNotReconcile(t *testing.T) {
	repo := &listResumableSpy{}
	m := &Manager{repo: repo, sessions: make(map[string]*loginSession)}
	m.supersededUntil.Store(time.Now().Add(supersededHold).UnixNano())

	m.reconcileSessions(context.Background())
	if repo.calls != 0 {
		t.Fatal("an instance whose streams were just replaced went on reconciling")
	}

	m.supersededUntil.Store(time.Now().Add(-time.Second).UnixNano())
	m.reconcileSessions(context.Background())
	if repo.calls != 1 {
		t.Fatal("reconciling did not resume once the hold ran out")
	}
}
