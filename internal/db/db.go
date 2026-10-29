package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib" // also registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// The Supabase session pooler gives every client together only pool_size (15)
// connections, and one more is refused outright ("max clients reached"). That
// budget is shared by this pool, whatsmeow's store (WhatsAppStoreMaxConns), a
// deploy's outgoing instance while the new one boots, and any local dev server
// pointed at the same database. pgxpool's own default, max(4, NumCPU), would
// let one instance take the lot, so the pool is capped here instead.
// pool_max_conns / pool_min_conns in DATABASE_URL win over both.
//
// One instance takes at most maxConns + WhatsAppStoreMaxConns (7), so the
// outgoing and incoming instances of a deploy fit in 15 together; migrations
// borrow this pool rather than adding a connection of their own.
//
// minConns is how many connections the pool keeps open while idle. A new
// connection to the hosted database costs several round trips (TCP, TLS, auth)
// before its first query, and a dashboard page asks for its lists all at once,
// so after a quiet spell each of those requests would pay that first.
const (
	maxConns        = 5
	minConns        = 2
	maxConnIdleTime = 5 * time.Minute

	// WhatsAppStoreMaxConns caps whatsmeow's database/sql handle, which would
	// otherwise open a connection for every concurrent store query.
	WhatsAppStoreMaxConns = 2
)

// startupWait is how long Connect keeps retrying while the pooler is full. On a
// deploy the outgoing instance still holds its connections until the new one
// is healthy, so the first attempt can find no room; it frees up shortly.
const startupWait = 60 * time.Second

// Connect opens a PostgreSQL connection pool.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	normalized := normalizeDatabaseURL(databaseURL)
	config, err := pgxpool.ParseConfig(normalized)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	if !strings.Contains(normalized, "pool_max_conns") {
		config.MaxConns = maxConns
	}
	if config.MinConns == 0 {
		config.MinConns = min(minConns, config.MaxConns)
	}
	config.MaxConnIdleTime = maxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := RetryWhilePoolerFull(ctx, func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

// RetryWhilePoolerFull runs fn, and runs it again every few seconds for up to
// startupWait while it fails only because the pooler has no connection to give.
// Every startup step that opens connections goes through it, since any one of
// them can land while a deploy's outgoing instance still holds its share.
func RetryWhilePoolerFull(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(startupWait)
	err := fn()
	for IsPoolerFull(err) && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(3 * time.Second)
		err = fn()
	}
	return err
}

// IsPoolerFull reports whether err is the Supabase pooler refusing a new client
// because every connection in its pool is already handed out.
func IsPoolerFull(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "EMAXCONNSESSION") ||
		strings.Contains(err.Error(), "max clients reached"))
}

// Batcher sends a pgx.Batch; *pgxpool.Pool and pgx.Tx both do.
type Batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// ListPage runs a list endpoint's count and its page of rows as one batch, so
// the pair costs a single round trip to Postgres rather than one each. scan is
// called for every row of the page, in order, and the total is returned.
func ListPage(
	ctx context.Context,
	q Batcher,
	countSQL string, countArgs []any,
	pageSQL string, pageArgs []any,
	scan func(pgx.Rows) error,
) (int, error) {
	batch := &pgx.Batch{}
	batch.Queue(countSQL, countArgs...)
	batch.Queue(pageSQL, pageArgs...)
	results := q.SendBatch(ctx, batch)
	defer results.Close()

	var total int
	if err := results.QueryRow().Scan(&total); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}

	rows, err := results.Query()
	if err != nil {
		return 0, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate: %w", err)
	}
	rows.Close()
	return total, results.Close()
}

func normalizeDatabaseURL(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return databaseURL
	}

	query := parsed.Query()
	if query.Has("schema") {
		query.Del("schema")
		parsed.RawQuery = query.Encode()
	}

	return parsed.String()
}

// Migrate applies all pending database migrations embedded in migrations/*.sql.
//
// goose works over the database/sql interface rather than pgxpool, so we open a
// short-lived *sql.DB using the pgx stdlib driver, run the migrations, then
// close it. Already-applied migrations are tracked in the goose_db_version
// table, so this is safe to call on every startup.
func Migrate(databaseURL string) error {
	sqlDB, err := sql.Open("pgx", normalizeDatabaseURL(databaseURL))
	if err != nil {
		return fmt.Errorf("open sql db for migrations: %w", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)

	return RetryWhilePoolerFull(context.Background(), func() error { return migrate(sqlDB) })
}

// MigratePool is Migrate over a pool that is already connected, borrowing one
// of its connections instead of opening another. Closing the database/sql
// handle it wraps the pool in leaves the pool open.
func MigratePool(pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	return migrate(sqlDB)
}

func migrate(sqlDB *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
