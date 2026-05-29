// Package store is the persistence boundary for keychain. Drivers
// (memory, postgres) implement the Store interface and are verified
// identical by the conformance suite in internal/store/conformance.
//
// The persisted shape (Workspace, API, Key) mirrors the proto wire types
// but lives in this package so the storage layer is not tied to the wire
// contract and so internal-only fields (KeyHash) never leak through the
// public API.
package store

import (
	"context"
	"errors"
	"time"
)

// Errors returned by every driver. Drivers must use these sentinels (or
// wrap them with %w) so callers can branch on them.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// Workspace is the top-level tenant boundary.
type Workspace struct {
	WorkspaceID      string
	Name             string
	OwnerPrincipalID string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Metadata         map[string]string
}

// API is a logical product within a workspace under which keys are issued.
type API struct {
	APIID       string
	WorkspaceID string
	Name        string
	KeyPrefix   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Metadata    map[string]string
}

// LimitRef points at a rate-limiter Limit. JSON tags are present so the
// type round-trips cleanly through Postgres's JSONB column on the keys
// table; field names on the wire stay snake_case to match the proto.
type LimitRef struct {
	LimitID  string `json:"limit_id"`
	ScopeKey string `json:"scope_key"`
}

// Key is the persisted bearer credential. KeyHash is sha256(plaintext) and
// must never be serialised out of the store layer.
type Key struct {
	KeyID            string
	APIID            string
	WorkspaceID      string
	OwnerPrincipalID string
	Name             string
	KeyHash          []byte
	Permissions      []string
	LimitRefs        []LimitRef
	// ExpiresAt is optional; nil means no expiry.
	ExpiresAt *time.Time
	// RemainingUses < 0 means unlimited credit. >= 0 is a hard cap that
	// decrements on each successful verify.
	RemainingUses  int64
	Enabled        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastVerifiedAt *time.Time
	Metadata       map[string]string
}

// ListKeysOpts are the filter + pagination knobs accepted by ListKeys.
// OwnerPrincipalID is optional.
type ListKeysOpts struct {
	APIID            string
	OwnerPrincipalID string
	PageSize         int32
	PageToken        string
}

// ListKeysResult is the page returned by ListKeys; NextPageToken is empty
// on the final page.
type ListKeysResult struct {
	Keys          []Key
	NextPageToken string
}

// Store is the persistence boundary. Implementations must be safe for
// concurrent use across goroutines.
type Store interface {
	// Workspace management.
	CreateWorkspace(ctx context.Context, w Workspace) (Workspace, error)
	GetWorkspace(ctx context.Context, id string) (Workspace, error)

	// API management. CreateAPI must fail with ErrNotFound (referencing
	// WorkspaceID) if the parent workspace does not exist.
	CreateAPI(ctx context.Context, a API) (API, error)
	GetAPI(ctx context.Context, id string) (API, error)

	// Key management. CreateKey must fail with ErrNotFound if the parent
	// API does not exist. CreateKey must fail with ErrConflict if another
	// key with the same KeyHash already exists (hash collision or replay).
	CreateKey(ctx context.Context, k Key) (Key, error)
	GetKeyByID(ctx context.Context, id string) (Key, error)
	GetKeyByHash(ctx context.Context, hash []byte) (Key, error)
	// RevokeKey is idempotent — revoking an already-revoked key is a no-op
	// and returns nil. Revoking an unknown key returns ErrNotFound.
	RevokeKey(ctx context.Context, id string) error
	// RotateKey replaces the KeyHash and bumps UpdatedAt; KeyID is stable.
	RotateKey(ctx context.Context, id string, newHash []byte) (Key, error)
	ListKeys(ctx context.Context, opts ListKeysOpts) (ListKeysResult, error)

	// Hot-path side effects. TouchLastVerified is fire-and-forget shaped
	// but returns an error so callers can log it; it MUST NOT block the
	// verify decision in production wiring.
	TouchLastVerified(ctx context.Context, id string, at time.Time) error
	// DecrementRemainingUses subtracts 1 atomically from a key with
	// RemainingUses >= 0 and returns the new value. For unlimited keys
	// (RemainingUses < 0) it is a no-op and returns -1.
	DecrementRemainingUses(ctx context.Context, id string) (remaining int64, err error)
}
