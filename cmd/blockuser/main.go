// Command blockuser blocks or unblocks an account by email:
//
//	go run ./cmd/blockuser someone@example.com
//	go run ./cmd/blockuser -unblock someone@example.com
//
// A blocked account's requests get a 403 (the dashboard shows a blocked screen),
// its agents stop answering messages and calls, and its numbers are not
// reconnected on the next restart. Running servers serve the cached user row
// for up to five minutes before the block reaches the dashboard and API.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"whatsapp-ai-caller-server/internal/config"
	"whatsapp-ai-caller-server/internal/db"
)

func main() {
	unblock := flag.Bool("unblock", false, "clear the block instead of setting it")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: blockuser [-unblock] <email>")
	}
	email := flag.Arg(0)

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	q := `UPDATE users SET blocked_at = now(), updated_at = now() WHERE lower(email) = lower($1) AND blocked_at IS NULL`
	if *unblock {
		q = `UPDATE users SET blocked_at = NULL, updated_at = now() WHERE lower(email) = lower($1) AND blocked_at IS NOT NULL`
	}
	// Simple protocol so this also works through the transaction pooler (:6543)
	// when the session pooler is full.
	tag, err := pool.Exec(ctx, q, pgx.QueryExecModeSimpleProtocol, email)
	if err != nil {
		log.Fatalf("update failed: %v", err)
	}

	var blockedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT blocked_at FROM users WHERE lower(email) = lower($1)`,
		pgx.QueryExecModeSimpleProtocol, email).Scan(&blockedAt); err != nil {
		log.Fatalf("no user with email %s: %v", email, err)
	}
	state := "not blocked"
	if blockedAt != nil {
		state = "blocked since " + blockedAt.UTC().Format(time.RFC3339)
	}
	log.Printf("%s: %d row(s) changed, now %s", email, tag.RowsAffected(), state)
}
