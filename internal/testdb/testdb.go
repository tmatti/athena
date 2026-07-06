// Package testdb provides the shared integration-test database harness.
// Tests skip when TEST_DATABASE_URL is unset.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tmatti/athena/internal/db"
)

// lockKey serializes test packages that share the one test database.
const lockKey = 424242

// Pool migrates the test database, truncates all data, and returns a pool.
// A session-level advisory lock is held for the duration of the test so
// parallel `go test` package runs do not interleave.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	ctx := context.Background()
	// Take the advisory lock on a plain connection before migrating: migrating
	// outside the lock lets parallel test packages race on schema-creation
	// statements like `CREATE EXTENSION`, which is not idempotent under
	// concurrent execution. The lock connection must not be db.Connect — that
	// registers pgvector types, which fails until the migration has created
	// the vector extension on a pristine database.
	lock, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect lock connection: %v", err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}
	// Registered before migrate/connect so a failure there still releases the
	// lock; closing the session releases its advisory locks.
	t.Cleanup(func() {
		_, _ = lock.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)
		_ = lock.Close(context.Background())
	})

	if err := db.Migrate(url); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `TRUNCATE memories, notes, note_chunks; DELETE FROM embedding_meta`); err != nil {
		t.Fatalf("truncate test database: %v", err)
	}
	return pool
}
