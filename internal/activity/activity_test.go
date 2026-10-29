package activity

import (
	"bytes"
	"context"
	"testing"
	"time"
)

type fakeStore struct{ saved chan Entry }

func (f fakeStore) Insert(_ context.Context, entry Entry) error {
	f.saved <- entry
	return nil
}

func TestLogPrintsAndSavesUserEntries(t *testing.T) {
	var out bytes.Buffer
	SetOutput(&out)
	store := fakeStore{saved: make(chan Entry, 2)}
	UseStore(store)
	t.Cleanup(func() { UseStore(nil) })

	Log(Entry{UserID: "u1", Email: "a@example.com", Event: QRScanFailed, Message: "QR scan failed: a@example.com", Detail: "expired"})

	select {
	case got := <-store.saved:
		if got.UserID != "u1" || got.Event != QRScanFailed || got.Detail != "expired" {
			t.Fatalf("saved entry = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("entry was never saved")
	}
	if !bytes.Contains(out.Bytes(), []byte("QR scan failed: a@example.com")) {
		t.Fatalf("entry was not printed: %q", out.String())
	}
}

func TestLogDoesNotSaveEntriesWithoutUser(t *testing.T) {
	SetOutput(&bytes.Buffer{})
	store := fakeStore{saved: make(chan Entry, 1)}
	UseStore(store)
	t.Cleanup(func() { UseStore(nil) })

	Log(Entry{Email: "unknown", Event: SignedIn, Message: "user signed in: unknown"})

	select {
	case got := <-store.saved:
		t.Fatalf("saved an entry with no user: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}
