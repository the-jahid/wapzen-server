package db

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// keepWarmInterval is how often idle connections are exercised. It sits well
// under the usual idle cut-offs between here and the database (NAT tables,
// poolers, firewalls drop silent TCP flows after a few minutes), so a
// connection the pool is holding is one that still works.
const keepWarmInterval = 30 * time.Second

// keepWarmTimeout bounds one round of pings. A ping that cannot finish in this
// time is on a dead connection, which the failed ping removes from the pool —
// here, in the background, instead of on the next request.
const keepWarmTimeout = 5 * time.Second

// KeepPoolWarm pings every idle connection in pool every keepWarmInterval
// until ctx ends.
//
// Why: the first inbound WhatsApp call after a quiet spell answered ~4 s
// slower than a call placed a minute after another one. The connections the
// pool kept through the quiet spell had gone cold (or been dropped on the way
// to the database without either side noticing), so the call's queries paid
// for re-pinging, re-dialling and re-preparing on the caller's ring time.
// Pinging them in the background keeps that cost off the call.
//
// It only touches connections that are already idle, so it never competes
// with real work for a connection and never opens more than the pool's
// MinConns would anyway.
func KeepPoolWarm(ctx context.Context, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(keepWarmInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingIdlePool(ctx, pool)
			}
		}
	}()
}

func pingIdlePool(ctx context.Context, pool *pgxpool.Pool) {
	ctx, cancel := context.WithTimeout(ctx, keepWarmTimeout)
	defer cancel()
	conns := pool.AcquireAllIdle(ctx)
	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(conn *pgxpool.Conn) {
			defer wg.Done()
			if err := conn.Ping(ctx); err != nil {
				// A connection that fails its ping is broken; destroy it rather
				// than hand it back, and let the pool's MinConns replace it.
				_ = conn.Conn().Close(context.Background())
				if ctx.Err() == nil {
					log.Printf("db: keep-warm ping failed, dropped connection: %v", err)
				}
			}
			conn.Release()
		}(conn)
	}
	wg.Wait()
	// Top the pool back up to MinConns now, while nothing is waiting on it,
	// rather than leaving the next request to dial.
	if stat := pool.Stat(); stat.TotalConns() < pool.Config().MinConns {
		_ = pool.Ping(ctx)
	}
}

// KeepSQLWarm holds up to n connections of a database/sql handle open and
// pings each every keepWarmInterval until ctx ends. It is the database/sql
// counterpart of KeepPoolWarm, for whatsmeow's key store: an inbound call's
// encryption key is decrypted through that store before the call can even be
// pre-accepted, so a store that had let its connections go during a quiet
// spell made the caller wait for fresh connections first.
//
// n should match the handle's SetMaxIdleConns so the pinged connections are
// the ones kept. A connection busy with real work is not waited for past the
// round's timeout.
func KeepSQLWarm(ctx context.Context, sqlDB *sql.DB, n int) {
	if sqlDB == nil || n <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(keepWarmInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingSQL(ctx, sqlDB, n)
			}
		}
	}()
}

func pingSQL(ctx context.Context, sqlDB *sql.DB, n int) {
	ctx, cancel := context.WithTimeout(ctx, keepWarmTimeout)
	defer cancel()
	// Check out all n at once so each ping lands on a different connection;
	// pinging one after another would reuse the same idle connection n times.
	conns := make([]*sql.Conn, 0, n)
	for i := 0; i < n; i++ {
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			break
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		if err := conn.PingContext(ctx); err != nil && ctx.Err() == nil {
			log.Printf("db: keep-warm ping failed on store connection: %v", err)
		}
		// Close returns a healthy connection to the pool; database/sql discards
		// one whose ping reported it bad.
		_ = conn.Close()
	}
}
