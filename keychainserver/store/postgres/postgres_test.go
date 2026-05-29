package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/elloloop/keychain/keychainserver/store"
	"github.com/elloloop/keychain/keychainserver/store/conformance"
	"github.com/elloloop/keychain/keychainserver/store/postgres"
)

// TestConformance runs the shared Store conformance suite against a real
// Postgres. Skipped unless KEYCHAIN_TEST_POSTGRES_URL is set. Tests share a
// single pool and reset state via TRUNCATE between subtests.
func TestConformance(t *testing.T) {
	dsn := os.Getenv("KEYCHAIN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("KEYCHAIN_TEST_POSTGRES_URL not set; skipping Postgres conformance suite")
	}

	ctx := context.Background()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	conformance.Run(t, func(t *testing.T) store.Store {
		t.Helper()
		truncate(t, pool)
		return postgres.NewWithPool(pool)
	})
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `TRUNCATE workspaces, apis, keys RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
