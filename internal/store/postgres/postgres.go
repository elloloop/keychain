// Package postgres is the durable Store driver. The schema lives in
// embedded SQL migrations applied by RunMigrations; the driver itself is
// driven by pgx/v5 and is safe for concurrent use.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for golang-migrate

	"github.com/elloloop/keychain/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the Postgres-backed implementation of store.Store.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// New opens a pgx pool against dsn, runs pending migrations, and returns a
// ready Store. The caller is responsible for the pool's lifetime via Close.
func New(ctx context.Context, dsn string) (*Store, error) {
	if err := RunMigrations(dsn); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return NewWithPool(pool), nil
}

// NewWithPool wraps an already-open pgx pool. Migrations are assumed
// already applied; useful for tests that share a pool across subtests.
func NewWithPool(pool *pgxpool.Pool) *Store {
	return &Store{
		pool: pool,
		now:  func() time.Time { return time.Now().UTC() },
	}
}

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// RunMigrations applies every pending migration to dsn. Idempotent; calling
// it on an already-current database is a no-op.
func RunMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open sql.DB: %w", err)
	}
	defer func() { _ = db.Close() }()

	drv, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrate source: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", drv)
	if err != nil {
		return fmt.Errorf("migrate new: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Workspace
// ---------------------------------------------------------------------------

func (s *Store) CreateWorkspace(ctx context.Context, w store.Workspace) (store.Workspace, error) {
	w.WorkspaceID = newID("ws")
	now := s.now()
	w.CreatedAt = now
	w.UpdatedAt = now
	meta := marshalMetadata(w.Metadata)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO workspaces (workspace_id, name, owner_principal_id, created_at, updated_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		w.WorkspaceID, w.Name, w.OwnerPrincipalID, w.CreatedAt, w.UpdatedAt, meta)
	if err != nil {
		return store.Workspace{}, fmt.Errorf("insert workspace: %w", err)
	}
	return w, nil
}

func (s *Store) GetWorkspace(ctx context.Context, id string) (store.Workspace, error) {
	var (
		w    store.Workspace
		meta []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT workspace_id, name, owner_principal_id, created_at, updated_at, metadata
		FROM workspaces WHERE workspace_id = $1`, id).Scan(
		&w.WorkspaceID, &w.Name, &w.OwnerPrincipalID, &w.CreatedAt, &w.UpdatedAt, &meta)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Workspace{}, store.ErrNotFound
		}
		return store.Workspace{}, fmt.Errorf("select workspace: %w", err)
	}
	w.Metadata = unmarshalMetadata(meta)
	return w, nil
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

func (s *Store) CreateAPI(ctx context.Context, a store.API) (store.API, error) {
	a.APIID = newID("api")
	now := s.now()
	a.CreatedAt = now
	a.UpdatedAt = now
	meta := marshalMetadata(a.Metadata)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO apis (api_id, workspace_id, name, key_prefix, created_at, updated_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		a.APIID, a.WorkspaceID, a.Name, a.KeyPrefix, a.CreatedAt, a.UpdatedAt, meta)
	if err != nil {
		if isFKViolation(err) {
			return store.API{}, fmt.Errorf("workspace %q: %w", a.WorkspaceID, store.ErrNotFound)
		}
		return store.API{}, fmt.Errorf("insert api: %w", err)
	}
	return a, nil
}

func (s *Store) GetAPI(ctx context.Context, id string) (store.API, error) {
	var (
		a    store.API
		meta []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT api_id, workspace_id, name, key_prefix, created_at, updated_at, metadata
		FROM apis WHERE api_id = $1`, id).Scan(
		&a.APIID, &a.WorkspaceID, &a.Name, &a.KeyPrefix, &a.CreatedAt, &a.UpdatedAt, &meta)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.API{}, store.ErrNotFound
		}
		return store.API{}, fmt.Errorf("select api: %w", err)
	}
	a.Metadata = unmarshalMetadata(meta)
	return a, nil
}

// ---------------------------------------------------------------------------
// Key
// ---------------------------------------------------------------------------

func (s *Store) CreateKey(ctx context.Context, k store.Key) (store.Key, error) {
	k.KeyID = newID("key")
	now := s.now()
	k.CreatedAt = now
	k.UpdatedAt = now

	limitRefsJSON, err := json.Marshal(k.LimitRefs)
	if err != nil {
		return store.Key{}, fmt.Errorf("marshal limit_refs: %w", err)
	}
	meta := marshalMetadata(k.Metadata)
	perms := nonNilStrings(k.Permissions)

	_, err = s.pool.Exec(ctx, `
		INSERT INTO keys (
			key_id, api_id, workspace_id, owner_principal_id, name,
			key_hash, permissions, limit_refs, expires_at, remaining_uses,
			enabled, created_at, updated_at, last_verified_at, metadata
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15
		)`,
		k.KeyID, k.APIID, k.WorkspaceID, k.OwnerPrincipalID, k.Name,
		k.KeyHash, perms, limitRefsJSON, k.ExpiresAt, k.RemainingUses,
		k.Enabled, k.CreatedAt, k.UpdatedAt, k.LastVerifiedAt, meta,
	)
	if err != nil {
		if isFKViolation(err) {
			return store.Key{}, fmt.Errorf("api %q: %w", k.APIID, store.ErrNotFound)
		}
		if isUniqueViolation(err) {
			return store.Key{}, store.ErrConflict
		}
		return store.Key{}, fmt.Errorf("insert key: %w", err)
	}
	return k, nil
}

func (s *Store) GetKeyByID(ctx context.Context, id string) (store.Key, error) {
	return s.queryKey(ctx, `WHERE key_id = $1`, id)
}

func (s *Store) GetKeyByHash(ctx context.Context, hash []byte) (store.Key, error) {
	return s.queryKey(ctx, `WHERE key_hash = $1`, hash)
}

func (s *Store) queryKey(ctx context.Context, where string, args ...any) (store.Key, error) {
	var (
		k       store.Key
		refs    []byte
		meta    []byte
		expires sql.NullTime
		lastVer sql.NullTime
	)
	q := `
		SELECT key_id, api_id, workspace_id, owner_principal_id, name,
		       key_hash, permissions, limit_refs, expires_at, remaining_uses,
		       enabled, created_at, updated_at, last_verified_at, metadata
		FROM keys ` + where
	err := s.pool.QueryRow(ctx, q, args...).Scan(
		&k.KeyID, &k.APIID, &k.WorkspaceID, &k.OwnerPrincipalID, &k.Name,
		&k.KeyHash, &k.Permissions, &refs, &expires, &k.RemainingUses,
		&k.Enabled, &k.CreatedAt, &k.UpdatedAt, &lastVer, &meta,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Key{}, store.ErrNotFound
		}
		return store.Key{}, fmt.Errorf("select key: %w", err)
	}
	if expires.Valid {
		t := expires.Time
		k.ExpiresAt = &t
	}
	if lastVer.Valid {
		t := lastVer.Time
		k.LastVerifiedAt = &t
	}
	if err := json.Unmarshal(refs, &k.LimitRefs); err != nil {
		return store.Key{}, fmt.Errorf("unmarshal limit_refs: %w", err)
	}
	k.Metadata = unmarshalMetadata(meta)
	return k, nil
}

func (s *Store) RevokeKey(ctx context.Context, id string) error {
	// RETURNING 1 makes "key exists" distinguishable from "no row" without
	// a second round-trip. Already-revoked keys still match WHERE and
	// return a row, giving us idempotency for free.
	var dummy int
	err := s.pool.QueryRow(ctx, `
		UPDATE keys SET enabled = false, updated_at = $1
		WHERE key_id = $2 RETURNING 1`,
		s.now(), id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		return fmt.Errorf("update key: %w", err)
	}
	return nil
}

func (s *Store) RotateKey(ctx context.Context, id string, newHash []byte) (store.Key, error) {
	_, err := s.pool.Exec(ctx, `
		UPDATE keys SET key_hash = $1, updated_at = $2 WHERE key_id = $3`,
		newHash, s.now(), id)
	if err != nil {
		if isUniqueViolation(err) {
			return store.Key{}, store.ErrConflict
		}
		return store.Key{}, fmt.Errorf("update key hash: %w", err)
	}
	return s.GetKeyByID(ctx, id)
}

func (s *Store) ListKeys(ctx context.Context, opts store.ListKeysOpts) (store.ListKeysResult, error) {
	pageSize := int(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	// Over-fetch by one to detect "is there another page" without a
	// follow-up count query.
	rows, err := s.pool.Query(ctx, `
		SELECT key_id, api_id, workspace_id, owner_principal_id, name,
		       key_hash, permissions, limit_refs, expires_at, remaining_uses,
		       enabled, created_at, updated_at, last_verified_at, metadata
		FROM keys
		WHERE api_id = $1
		  AND ($2 = '' OR owner_principal_id = $2)
		  AND ($3 = '' OR key_id > $3)
		ORDER BY key_id ASC
		LIMIT $4`,
		opts.APIID, opts.OwnerPrincipalID, opts.PageToken, pageSize+1)
	if err != nil {
		return store.ListKeysResult{}, fmt.Errorf("select keys: %w", err)
	}
	defer rows.Close()

	out := make([]store.Key, 0, pageSize)
	for rows.Next() {
		var (
			k       store.Key
			refs    []byte
			meta    []byte
			expires sql.NullTime
			lastVer sql.NullTime
		)
		if err := rows.Scan(
			&k.KeyID, &k.APIID, &k.WorkspaceID, &k.OwnerPrincipalID, &k.Name,
			&k.KeyHash, &k.Permissions, &refs, &expires, &k.RemainingUses,
			&k.Enabled, &k.CreatedAt, &k.UpdatedAt, &lastVer, &meta,
		); err != nil {
			return store.ListKeysResult{}, fmt.Errorf("scan key: %w", err)
		}
		if expires.Valid {
			t := expires.Time
			k.ExpiresAt = &t
		}
		if lastVer.Valid {
			t := lastVer.Time
			k.LastVerifiedAt = &t
		}
		if err := json.Unmarshal(refs, &k.LimitRefs); err != nil {
			return store.ListKeysResult{}, fmt.Errorf("unmarshal limit_refs: %w", err)
		}
		k.Metadata = unmarshalMetadata(meta)
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return store.ListKeysResult{}, fmt.Errorf("iterate keys: %w", err)
	}

	next := ""
	if len(out) > pageSize {
		next = out[pageSize-1].KeyID
		out = out[:pageSize]
	}
	return store.ListKeysResult{Keys: out, NextPageToken: next}, nil
}

func (s *Store) TouchLastVerified(ctx context.Context, id string, at time.Time) error {
	at = at.UTC()
	tag, err := s.pool.Exec(ctx, `UPDATE keys SET last_verified_at = $1 WHERE key_id = $2`, at, id)
	if err != nil {
		return fmt.Errorf("touch last_verified_at: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) DecrementRemainingUses(ctx context.Context, id string) (int64, error) {
	// CASE preserves unlimited (-1) keys; GREATEST clamps depleted credit
	// at 0 instead of going negative on a racing duplicate verify.
	var remaining int64
	err := s.pool.QueryRow(ctx, `
		UPDATE keys
		SET remaining_uses = CASE
		    WHEN remaining_uses < 0 THEN remaining_uses
		    ELSE GREATEST(remaining_uses - 1, 0)
		END,
		    updated_at = $1
		WHERE key_id = $2
		RETURNING remaining_uses`,
		s.now(), id).Scan(&remaining)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, store.ErrNotFound
		}
		return 0, fmt.Errorf("decrement remaining: %w", err)
	}
	return remaining, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newID(prefix string) string {
	return prefix + "_" + uuid.NewString()
}

// nonNilStrings makes sure pgx serialises an empty slice as `'{}'::text[]`
// rather than NULL when the column is NOT NULL DEFAULT '{}'.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func marshalMetadata(m map[string]string) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, _ := json.Marshal(m)
	return b
}

func unmarshalMetadata(b []byte) map[string]string {
	if len(b) == 0 {
		return nil
	}
	m := map[string]string{}
	_ = json.Unmarshal(b, &m)
	if len(m) == 0 {
		return nil
	}
	return m
}

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
