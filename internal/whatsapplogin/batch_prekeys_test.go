package whatsapplogin

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/util/keys"
)

// TestBatchPreKeysLive writes 812 prekeys for a throwaway device through the
// batch path against a real database, then deletes the device (its prekeys go
// with it). Runs only with PREKEY_TEST_DATABASE_URL set.
func TestBatchPreKeysLive(t *testing.T) {
	url := os.Getenv("PREKEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set PREKEY_TEST_DATABASE_URL to run against a database")
	}
	db, err := sql.Open("pgx", normalizeDatabaseURL(url))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const jid = "0000000000:99@s.whatsapp.net"
	k32, k64 := make([]byte, 32), make([]byte, 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO whatsmeow_device (jid, registration_id, noise_key, identity_key, signed_pre_key, signed_pre_key_id, signed_pre_key_sig, adv_key, adv_details, adv_account_sig, adv_account_sig_key, adv_device_sig) VALUES ($1,1,$2,$2,$2,1,$3,$2,$2,$3,$2,$3)`, jid, k32, k64); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM whatsmeow_device WHERE jid=$1`, jid)

	s := &batchPreKeyStore{PreKeyStore: failingPreKeys{t: t}, db: db, jid: jid}
	start := time.Now()
	first, err := s.GetOrGenPreKeys(ctx, 812)
	if err != nil || len(first) != 812 {
		t.Fatalf("got %d keys, err %v", len(first), err)
	}
	t.Logf("generated and saved 812 prekeys in %s", time.Since(start))
	// Asked again before upload, the same keys come back from the database.
	again, err := s.GetOrGenPreKeys(ctx, 812)
	if err != nil || len(again) != 812 || again[0].KeyID != first[0].KeyID || *again[811].Priv != *first[811].Priv {
		t.Fatalf("second call did not return the saved keys (err %v)", err)
	}
	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whatsmeow_pre_keys WHERE jid=$1`, jid).Scan(&n)
	if n != 812 {
		t.Fatalf("database has %d prekeys, want 812", n)
	}
}

// failingPreKeys fails the test if the batch path ever falls back.
type failingPreKeys struct {
	store.PreKeyStore
	t *testing.T
}

func (f failingPreKeys) GetOrGenPreKeys(context.Context, uint32) ([]*keys.PreKey, error) {
	f.t.Fatal("fell back to the one-by-one path")
	return nil, nil
}
