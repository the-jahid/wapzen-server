package whatsapplogin

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/util/keys"
)

// batchPreKeyStore makes whatsmeow's prekey generation one INSERT instead of
// one per key.
//
// A freshly linked device uploads 812 prekeys before whatsmeow reports it
// Connected, and the stock SQL store writes them one row at a time. Against a
// distant database (432 ms a round trip measured 2026-11-22 to the Sydney
// Supabase) that is about six minutes during which the new number cannot
// receive anything — no messages, no calls. Here the keys are generated in
// memory and written in a few multi-row INSERTs inside one transaction.
//
// Every other prekey operation goes to the wrapped store unchanged, and a batch
// that fails falls back to it, so the worst case is today's slow path.
type batchPreKeyStore struct {
	store.PreKeyStore
	db  *sql.DB
	jid string
	mu  sync.Mutex
}

// preKeyBatchRows keeps one INSERT well under Postgres's 65535-parameter limit
// (4 parameters a row).
const preKeyBatchRows = 1000

// useBatchPreKeys puts the batching store in front of device's prekey store.
// It is a no-op for a device without an identity yet (its stores are created
// when the pairing is saved — call again on PairSuccess) or one already wrapped.
func useBatchPreKeys(db *sql.DB, device *store.Device) {
	if db == nil || device == nil || device.ID == nil || device.PreKeys == nil {
		return
	}
	if _, ok := device.PreKeys.(*batchPreKeyStore); ok {
		return
	}
	device.PreKeys = &batchPreKeyStore{PreKeyStore: device.PreKeys, db: db, jid: device.ID.String()}
}

func (s *batchPreKeyStore) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	preKeys, err := s.getOrGen(ctx, count)
	if err != nil {
		log.Printf("whatsapp prekeys: batch generation failed for %s, using the one-by-one path: %v", s.jid, err)
		return s.PreKeyStore.GetOrGenPreKeys(ctx, count)
	}
	return preKeys, nil
}

func (s *batchPreKeyStore) getOrGen(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_id, key FROM whatsmeow_pre_keys WHERE jid=$1 AND uploaded=false ORDER BY key_id LIMIT $2`, s.jid, count)
	if err != nil {
		return nil, fmt.Errorf("query unuploaded prekeys: %w", err)
	}
	preKeys := make([]*keys.PreKey, 0, count)
	for rows.Next() {
		var id uint32
		var priv []byte
		if err := rows.Scan(&id, &priv); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan prekey: %w", err)
		}
		if len(priv) != 32 {
			rows.Close()
			return nil, fmt.Errorf("prekey %d has %d bytes", id, len(priv))
		}
		preKeys = append(preKeys, &keys.PreKey{KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(priv)), KeyID: id})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read prekeys: %w", err)
	}
	if uint32(len(preKeys)) >= count {
		return preKeys, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var lastID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(key_id) FROM whatsmeow_pre_keys WHERE jid=$1`, s.jid).Scan(&lastID); err != nil {
		return nil, fmt.Errorf("query next prekey id: %w", err)
	}
	nextID := uint32(lastID.Int64) + 1

	fresh := make([]*keys.PreKey, 0, int(count)-len(preKeys))
	for i := uint32(len(preKeys)); i < count; i++ {
		fresh = append(fresh, keys.NewPreKey(nextID))
		nextID++
	}
	for start := 0; start < len(fresh); start += preKeyBatchRows {
		end := min(start+preKeyBatchRows, len(fresh))
		var query strings.Builder
		query.WriteString(`INSERT INTO whatsmeow_pre_keys (jid, key_id, key, uploaded) VALUES `)
		args := make([]any, 0, (end-start)*4)
		for i, key := range fresh[start:end] {
			if i > 0 {
				query.WriteString(",")
			}
			n := len(args)
			fmt.Fprintf(&query, "($%d,$%d,$%d,$%d)", n+1, n+2, n+3, n+4)
			args = append(args, s.jid, key.KeyID, key.Priv[:], false)
		}
		if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
			return nil, fmt.Errorf("insert prekeys: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit prekeys: %w", err)
	}
	return append(preKeys, fresh...), nil
}
