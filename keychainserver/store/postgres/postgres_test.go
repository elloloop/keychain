package postgres_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

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

func TestNewAndClose(t *testing.T) {
	dsn := testPostgresDSN(t)
	s, err := postgres.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Close()
}

func TestNewRejectsUnreachablePostgres(t *testing.T) {
	_, err := postgres.New(context.Background(), "postgres://keychain:keychain@127.0.0.1:1/keychain?sslmode=disable")
	if err == nil {
		t.Fatal("New accepted unreachable Postgres")
	}
}

func TestNewRejectsMalformedPoolConfig(t *testing.T) {
	_, err := postgres.New(context.Background(), "postgres://[::1")
	if err == nil {
		t.Fatal("New accepted malformed pool config")
	}
	if !strings.Contains(err.Error(), "open pool") {
		t.Fatalf("err = %v, want open pool error", err)
	}
}

func TestNewReturnsPingErrorForCanceledContext(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := postgres.New(ctx, dsn)
	if err == nil {
		t.Fatal("New accepted canceled context")
	}
	if !strings.Contains(err.Error(), "ping") {
		t.Fatalf("err = %v, want ping error", err)
	}
}

func TestRunMigrationsRejectsBadDSN(t *testing.T) {
	err := postgres.RunMigrations("postgres://[::1")
	if err == nil {
		t.Fatal("RunMigrations accepted invalid DSN")
	}
	if !strings.Contains(err.Error(), "migrate driver") && !strings.Contains(err.Error(), "open") {
		t.Fatalf("err = %v, want migration setup error", err)
	}
}

func TestRunMigrationsReportsDirtyVersion(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx := context.Background()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET dirty = true`); err != nil {
		t.Fatalf("mark migrations dirty: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE schema_migrations SET dirty = false`)
	})

	err = postgres.RunMigrations(dsn)
	if err == nil || !strings.Contains(err.Error(), "migrate up") {
		t.Fatalf("RunMigrations err = %v, want migrate up error", err)
	}
}

func TestMethodsReturnDriverErrorsAfterPoolClose(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	pool.Close()
	s := postgres.NewWithPool(pool)
	now := time.Now().UTC()

	cases := []struct {
		name string
		call func() error
	}{
		{
			name: "CreateWorkspace",
			call: func() error {
				_, err := s.CreateWorkspace(ctx, store.Workspace{Name: "acme", OwnerPrincipalID: "owner"})
				return err
			},
		},
		{
			name: "GetWorkspace",
			call: func() error {
				_, err := s.GetWorkspace(ctx, "ws_1")
				return err
			},
		},
		{
			name: "CreateAPI",
			call: func() error {
				_, err := s.CreateAPI(ctx, store.API{WorkspaceID: "ws_1", Name: "prod"})
				return err
			},
		},
		{
			name: "GetAPI",
			call: func() error {
				_, err := s.GetAPI(ctx, "api_1")
				return err
			},
		},
		{
			name: "CreateKey",
			call: func() error {
				_, err := s.CreateKey(ctx, store.Key{
					APIID:            "api_1",
					WorkspaceID:      "ws_1",
					OwnerPrincipalID: "owner",
					KeyHash:          []byte("hash"),
					RemainingUses:    -1,
					Enabled:          true,
				})
				return err
			},
		},
		{
			name: "GetKeyByID",
			call: func() error {
				_, err := s.GetKeyByID(ctx, "key_1")
				return err
			},
		},
		{
			name: "GetKeyByHash",
			call: func() error {
				_, err := s.GetKeyByHash(ctx, []byte("hash"))
				return err
			},
		},
		{
			name: "RevokeKey",
			call: func() error {
				return s.RevokeKey(ctx, "key_1")
			},
		},
		{
			name: "RotateKey",
			call: func() error {
				_, err := s.RotateKey(ctx, "key_1", []byte("new-hash"))
				return err
			},
		},
		{
			name: "ListKeys",
			call: func() error {
				_, err := s.ListKeys(ctx, store.ListKeysOpts{APIID: "api_1"})
				return err
			},
		},
		{
			name: "TouchLastVerified",
			call: func() error {
				return s.TouchLastVerified(ctx, "key_1", now)
			},
		},
		{
			name: "DecrementRemainingUses",
			call: func() error {
				_, err := s.DecrementRemainingUses(ctx, "key_1")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("method returned nil error after pool close")
			}
			if strings.Contains(err.Error(), "store: not found") || strings.Contains(err.Error(), "store: conflict") {
				t.Fatalf("method mapped driver failure to store sentinel: %v", err)
			}
		})
	}
}

func TestCorruptRemainingUsesReturnsDriverErrors(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx := context.Background()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	truncate(t, pool)
	if _, err := pool.Exec(ctx, `ALTER TABLE keys ALTER COLUMN remaining_uses DROP NOT NULL`); err != nil {
		t.Fatalf("allow corrupt remaining_uses: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE keys SET remaining_uses = -1 WHERE remaining_uses IS NULL`)
		_, _ = pool.Exec(context.Background(), `ALTER TABLE keys ALTER COLUMN remaining_uses SET NOT NULL`)
	})

	s := postgres.NewWithPool(pool)
	w, err := s.CreateWorkspace(ctx, store.Workspace{Name: "acme", OwnerPrincipalID: "owner"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	a, err := s.CreateAPI(ctx, store.API{WorkspaceID: w.WorkspaceID, Name: "prod"})
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	k, err := s.CreateKey(ctx, store.Key{
		APIID:            a.APIID,
		WorkspaceID:      w.WorkspaceID,
		OwnerPrincipalID: "owner",
		Name:             "corrupt-remaining",
		KeyHash:          bytes.Repeat([]byte{0x2}, 32),
		RemainingUses:    1,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE keys SET remaining_uses = NULL WHERE key_id = $1`, k.KeyID); err != nil {
		t.Fatalf("corrupt remaining_uses: %v", err)
	}

	if _, err := s.ListKeys(ctx, store.ListKeysOpts{APIID: a.APIID}); err == nil || !strings.Contains(err.Error(), "scan key") {
		t.Fatalf("ListKeys err = %v, want scan key error", err)
	}
	if _, err := s.DecrementRemainingUses(ctx, k.KeyID); err == nil || !strings.Contains(err.Error(), "select remaining") {
		t.Fatalf("DecrementRemainingUses err = %v, want select remaining error", err)
	}
}

func TestInvalidLimitRefsJSONReturnsDecodeErrors(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx := context.Background()
	if err := postgres.RunMigrations(dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	truncate(t, pool)

	s := postgres.NewWithPool(pool)
	w, err := s.CreateWorkspace(ctx, store.Workspace{Name: "acme", OwnerPrincipalID: "owner"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	a, err := s.CreateAPI(ctx, store.API{WorkspaceID: w.WorkspaceID, Name: "prod"})
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	k, err := s.CreateKey(ctx, store.Key{
		APIID:            a.APIID,
		WorkspaceID:      w.WorkspaceID,
		OwnerPrincipalID: "owner",
		Name:             "bad-json",
		KeyHash:          bytes.Repeat([]byte{0x1}, 32),
		RemainingUses:    -1,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE keys SET limit_refs = '{}'::jsonb WHERE key_id = $1`, k.KeyID); err != nil {
		t.Fatalf("corrupt limit_refs: %v", err)
	}

	if _, err := s.GetKeyByID(ctx, k.KeyID); err == nil || !strings.Contains(err.Error(), "unmarshal limit_refs") {
		t.Fatalf("GetKeyByID err = %v, want unmarshal limit_refs", err)
	}
	if _, err := s.ListKeys(ctx, store.ListKeysOpts{APIID: a.APIID}); err == nil || !strings.Contains(err.Error(), "unmarshal limit_refs") {
		t.Fatalf("ListKeys err = %v, want unmarshal limit_refs", err)
	}
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `TRUNCATE workspaces, apis, keys RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func testPostgresDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("KEYCHAIN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("KEYCHAIN_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	return dsn
}
