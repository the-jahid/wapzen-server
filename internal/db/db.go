package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// minConns is how many connections the pool keeps open while idle. A new
// connection to the hosted database costs several round trips (TCP, TLS, auth)
// before its first query, and a dashboard page asks for its lists all at once,
// so after a quiet spell each of those requests would pay that first. Four
// covers a page's worth. A non-zero pool_min_conns in DATABASE_URL wins.
const minConns = 4

// Connect opens a PostgreSQL connection pool.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	config, err := pgxpool.ParseConfig(normalizeDatabaseURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	if config.MinConns == 0 {
		config.MinConns = min(minConns, config.MaxConns)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
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

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
