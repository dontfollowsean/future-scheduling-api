// Package postgrestest connects integration tests to a real Postgres.
package postgrestest

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dontfollowsean/future-scheduling-api/internal/postgres"
)

// maxConns lets the 20-way concurrency test run all of its inserts at
// once rather than queueing for a connection.
const maxConns = 25

// Pool returns a pool on the database named by TEST_DATABASE_URL, with
// migrations applied and the appointments table empty. If the variable is
// unset it skips the test, so "go test ./..." still runs the unit tests
// anywhere. The pool is closed when the test ends.
//
// Every test starts from an empty table, so tests that use Pool must not
// run in parallel with each other.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping integration test")
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(t.Context(), "TRUNCATE appointments RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate appointments: %v", err)
	}
	return pool
}
