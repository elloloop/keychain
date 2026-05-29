// Package conformance is the driver-agnostic test suite every Store
// implementation must pass. New drivers add a single _test.go that calls
// conformance.Run with a Factory; the suite enforces identical behaviour.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/elloloop/keychain/keychainserver/store"
)

// Factory builds a fresh, isolated Store for each top-level subtest.
// Implementations should reset state between calls.
type Factory func(t *testing.T) store.Store

// Run executes the full conformance suite.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("WorkspaceCreateAndGet", func(t *testing.T) { testWorkspaceCreateAndGet(t, newStore(t)) })
	t.Run("WorkspaceGetNotFound", func(t *testing.T) { testWorkspaceGetNotFound(t, newStore(t)) })
	t.Run("WorkspaceMetadataIsIsolated", func(t *testing.T) { testWorkspaceMetadataIsIsolated(t, newStore(t)) })
	t.Run("ApiCreateRequiresWorkspace", func(t *testing.T) { testAPICreateRequiresWorkspace(t, newStore(t)) })
	t.Run("ApiCreateAndGet", func(t *testing.T) { testAPICreateAndGet(t, newStore(t)) })
	t.Run("ApiGetNotFound", func(t *testing.T) { testAPIGetNotFound(t, newStore(t)) })
	t.Run("ApiMetadataIsIsolated", func(t *testing.T) { testAPIMetadataIsIsolated(t, newStore(t)) })
	t.Run("KeyCreateRequiresApi", func(t *testing.T) { testKeyCreateRequiresAPI(t, newStore(t)) })
	t.Run("KeyCreateAndLookup", func(t *testing.T) { testKeyCreateAndLookup(t, newStore(t)) })
	t.Run("KeyCreatePreservesAllFields", func(t *testing.T) { testKeyCreatePreservesAllFields(t, newStore(t)) })
	t.Run("KeyInputMutationDoesNotChangeStoredKey", func(t *testing.T) { testKeyInputMutationDoesNotChangeStoredKey(t, newStore(t)) })
	t.Run("KeyLookupMutationDoesNotChangeStoredKey", func(t *testing.T) { testKeyLookupMutationDoesNotChangeStoredKey(t, newStore(t)) })
	t.Run("KeyListMutationDoesNotChangeStoredKey", func(t *testing.T) { testKeyListMutationDoesNotChangeStoredKey(t, newStore(t)) })
	t.Run("KeyListPreservesAllFields", func(t *testing.T) { testKeyListPreservesAllFields(t, newStore(t)) })
	t.Run("KeyGetByHashUnknown", func(t *testing.T) { testKeyGetByHashUnknown(t, newStore(t)) })
	t.Run("KeyGetByIDUnknown", func(t *testing.T) { testKeyGetByIDUnknown(t, newStore(t)) })
	t.Run("KeyCreateRejectsDuplicateHash", func(t *testing.T) { testKeyCreateRejectsDuplicateHash(t, newStore(t)) })
	t.Run("KeyRevoke", func(t *testing.T) { testKeyRevoke(t, newStore(t)) })
	t.Run("KeyRevokeIsIdempotent", func(t *testing.T) { testKeyRevokeIsIdempotent(t, newStore(t)) })
	t.Run("KeyRevokeUnknownReturnsNotFound", func(t *testing.T) { testKeyRevokeUnknownReturnsNotFound(t, newStore(t)) })
	t.Run("KeyRotate", func(t *testing.T) { testKeyRotate(t, newStore(t)) })
	t.Run("KeyRotateUnknownReturnsNotFound", func(t *testing.T) { testKeyRotateUnknownReturnsNotFound(t, newStore(t)) })
	t.Run("KeyRotateRejectsDuplicateHash", func(t *testing.T) { testKeyRotateRejectsDuplicateHash(t, newStore(t)) })
	t.Run("KeyListByApi", func(t *testing.T) { testKeyListByAPI(t, newStore(t)) })
	t.Run("KeyListFilterByOwner", func(t *testing.T) { testKeyListFilterByOwner(t, newStore(t)) })
	t.Run("KeyListPaginates", func(t *testing.T) { testKeyListPaginates(t, newStore(t)) })
	t.Run("KeyListDefaultsPageSize", func(t *testing.T) { testKeyListDefaultsPageSize(t, newStore(t)) })
	t.Run("KeyListUnknownApiIsEmpty", func(t *testing.T) { testKeyListUnknownAPIIsEmpty(t, newStore(t)) })
	t.Run("KeyListPageTokenCanSkipToInsertionPoint", func(t *testing.T) { testKeyListPageTokenCanSkipToInsertionPoint(t, newStore(t)) })
	t.Run("KeyTouchLastVerified", func(t *testing.T) { testKeyTouchLastVerified(t, newStore(t)) })
	t.Run("KeyTouchLastVerifiedUnknownReturnsNotFound", func(t *testing.T) { testKeyTouchLastVerifiedUnknownReturnsNotFound(t, newStore(t)) })
	t.Run("KeyDecrementRemainingUses", func(t *testing.T) { testKeyDecrementRemainingUses(t, newStore(t)) })
	t.Run("KeyDecrementUnlimitedIsNoop", func(t *testing.T) { testKeyDecrementUnlimitedIsNoop(t, newStore(t)) })
	t.Run("KeyDecrementDepletedReturnsErrDepleted", func(t *testing.T) { testKeyDecrementDepletedReturnsErrDepleted(t, newStore(t)) })
	t.Run("KeyDecrementUnknownReturnsNotFound", func(t *testing.T) { testKeyDecrementUnknownReturnsNotFound(t, newStore(t)) })
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func newWorkspace(name string) store.Workspace {
	return store.Workspace{
		Name:             name,
		OwnerPrincipalID: "user_owner",
	}
}

func newAPI(workspaceID, name string) store.API {
	return store.API{
		WorkspaceID: workspaceID,
		Name:        name,
		KeyPrefix:   "ck_test_",
	}
}

func newKey(apiID, workspaceID, owner string, hash []byte) store.Key {
	return store.Key{
		APIID:            apiID,
		WorkspaceID:      workspaceID,
		OwnerPrincipalID: owner,
		Name:             "test-key",
		KeyHash:          hash,
		Permissions:      []string{"chat:write"},
		RemainingUses:    -1,
		Enabled:          true,
	}
}

func seedWorkspace(t *testing.T, s store.Store) store.Workspace {
	t.Helper()
	w, err := s.CreateWorkspace(ctx(t), newWorkspace("acme"))
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return w
}

func seedAPI(t *testing.T, s store.Store, workspaceID string) store.API {
	t.Helper()
	a, err := s.CreateAPI(ctx(t), newAPI(workspaceID, "prod"))
	if err != nil {
		t.Fatalf("seed api: %v", err)
	}
	return a
}

// ---------------------------------------------------------------------------
// Workspace tests
// ---------------------------------------------------------------------------

func testWorkspaceCreateAndGet(t *testing.T, s store.Store) {
	t.Helper()
	created, err := s.CreateWorkspace(ctx(t), newWorkspace("acme"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.WorkspaceID == "" {
		t.Fatal("WorkspaceID should be assigned by the store")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be assigned by the store")
	}

	got, err := s.GetWorkspace(ctx(t), created.WorkspaceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WorkspaceID != created.WorkspaceID {
		t.Fatalf("WorkspaceID mismatch: %q vs %q", got.WorkspaceID, created.WorkspaceID)
	}
	if got.Name != "acme" {
		t.Fatalf("Name = %q, want %q", got.Name, "acme")
	}
	if got.OwnerPrincipalID != "user_owner" {
		t.Fatalf("OwnerPrincipalID = %q", got.OwnerPrincipalID)
	}
}

func testWorkspaceGetNotFound(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.GetWorkspace(ctx(t), "ws_missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testWorkspaceMetadataIsIsolated(t *testing.T, s store.Store) {
	t.Helper()
	meta := map[string]string{"tier": "prod"}
	created, err := s.CreateWorkspace(ctx(t), store.Workspace{
		Name:             "acme",
		OwnerPrincipalID: "user_owner",
		Metadata:         meta,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	meta["tier"] = "mutated"
	meta["new"] = "leak"

	got, err := s.GetWorkspace(ctx(t), created.WorkspaceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := map[string]string{"tier": "prod"}
	if !maps.Equal(got.Metadata, want) {
		t.Fatalf("Metadata = %v, want %v", got.Metadata, want)
	}

	got.Metadata["tier"] = "client-mutated"
	again, err := s.GetWorkspace(ctx(t), created.WorkspaceID)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	if !maps.Equal(again.Metadata, want) {
		t.Fatalf("Metadata changed after mutating returned value: %v", again.Metadata)
	}
}

// ---------------------------------------------------------------------------
// API tests
// ---------------------------------------------------------------------------

func testAPICreateRequiresWorkspace(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.CreateAPI(ctx(t), newAPI("ws_missing", "prod"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testAPICreateAndGet(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	created, err := s.CreateAPI(ctx(t), newAPI(w.WorkspaceID, "prod"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.APIID == "" {
		t.Fatal("APIID should be assigned by the store")
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be assigned by the store")
	}

	got, err := s.GetAPI(ctx(t), created.APIID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WorkspaceID != w.WorkspaceID {
		t.Fatalf("WorkspaceID = %q, want %q", got.WorkspaceID, w.WorkspaceID)
	}
	if got.KeyPrefix != "ck_test_" {
		t.Fatalf("KeyPrefix = %q", got.KeyPrefix)
	}
}

func testAPIGetNotFound(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.GetAPI(ctx(t), "api_missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testAPIMetadataIsIsolated(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	meta := map[string]string{"region": "eu"}
	created, err := s.CreateAPI(ctx(t), store.API{
		WorkspaceID: w.WorkspaceID,
		Name:        "prod",
		KeyPrefix:   "ck_prod_",
		Metadata:    meta,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	meta["region"] = "mutated"
	meta["new"] = "leak"

	got, err := s.GetAPI(ctx(t), created.APIID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := map[string]string{"region": "eu"}
	if !maps.Equal(got.Metadata, want) {
		t.Fatalf("Metadata = %v, want %v", got.Metadata, want)
	}

	got.Metadata["region"] = "client-mutated"
	again, err := s.GetAPI(ctx(t), created.APIID)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	if !maps.Equal(again.Metadata, want) {
		t.Fatalf("Metadata changed after mutating returned value: %v", again.Metadata)
	}
}

// ---------------------------------------------------------------------------
// Key tests
// ---------------------------------------------------------------------------

func testKeyCreateRequiresAPI(t *testing.T, s store.Store) {
	t.Helper()
	hash := []byte("hash-abc")
	_, err := s.CreateKey(ctx(t), newKey("api_missing", "ws_x", "user_1", hash))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testKeyCreateAndLookup(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	hash := bytes.Repeat([]byte{0xab}, 32)

	created, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", hash))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.KeyID == "" {
		t.Fatal("KeyID should be assigned by the store")
	}
	if !bytes.Equal(created.KeyHash, hash) {
		t.Fatal("KeyHash should round-trip exactly")
	}

	byID, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("GetKeyByID: %v", err)
	}
	if byID.KeyID != created.KeyID {
		t.Fatalf("GetKeyByID mismatch")
	}

	byHash, err := s.GetKeyByHash(ctx(t), hash)
	if err != nil {
		t.Fatalf("GetKeyByHash: %v", err)
	}
	if byHash.KeyID != created.KeyID {
		t.Fatalf("GetKeyByHash mismatch")
	}
}

func testKeyCreatePreservesAllFields(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	lastVerified := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	hash := bytes.Repeat([]byte{0xcd}, 32)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", hash)
	k.Name = "full-key"
	k.Permissions = []string{"chat:read", "chat:write"}
	k.LimitRefs = []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}}
	k.ExpiresAt = &expires
	k.LastVerifiedAt = &lastVerified
	k.Metadata = map[string]string{"env": "test"}

	created, err := s.CreateKey(ctx(t), k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertKeyFields(t, got, keySnapshot{
		hash:           hash,
		permissions:    []string{"chat:read", "chat:write"},
		limitRefs:      []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}},
		expiresAt:      &expires,
		lastVerifiedAt: &lastVerified,
		metadata:       map[string]string{"env": "test"},
	})
}

func testKeyInputMutationDoesNotChangeStoredKey(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	lastVerified := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	hash := bytes.Repeat([]byte{0xdd}, 32)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", hash)
	k.Permissions = []string{"chat:read", "chat:write"}
	k.LimitRefs = []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}}
	k.ExpiresAt = &expires
	k.LastVerifiedAt = &lastVerified
	k.Metadata = map[string]string{"env": "test"}
	wantHash := append([]byte(nil), hash...)
	wantExpires := expires
	wantLastVerified := lastVerified

	created, err := s.CreateKey(ctx(t), k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	k.KeyHash[0] = 0xee
	k.Permissions[0] = "mutated"
	k.LimitRefs[0].LimitID = "mutated"
	*k.ExpiresAt = expires.Add(time.Hour)
	*k.LastVerifiedAt = lastVerified.Add(time.Hour)
	k.Metadata["env"] = "mutated"

	got, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertKeyFields(t, got, keySnapshot{
		hash:           wantHash,
		permissions:    []string{"chat:read", "chat:write"},
		limitRefs:      []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}},
		expiresAt:      &wantExpires,
		lastVerifiedAt: &wantLastVerified,
		metadata:       map[string]string{"env": "test"},
	})
}

func testKeyLookupMutationDoesNotChangeStoredKey(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	hash := bytes.Repeat([]byte{0xde}, 32)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", hash)
	k.Permissions = []string{"chat:read"}
	k.LimitRefs = []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}}
	k.ExpiresAt = &expires
	k.Metadata = map[string]string{"env": "test"}
	created, err := s.CreateKey(ctx(t), k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	mutateKeyValue(got)

	again, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	assertKeyFields(t, again, keySnapshot{
		hash:        hash,
		permissions: []string{"chat:read"},
		limitRefs:   []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}},
		expiresAt:   &expires,
		metadata:    map[string]string{"env": "test"},
	})
}

func testKeyListMutationDoesNotChangeStoredKey(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	hash := bytes.Repeat([]byte{0xdf}, 32)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", hash)
	k.Permissions = []string{"chat:read"}
	k.LimitRefs = []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}}
	k.ExpiresAt = &expires
	k.Metadata = map[string]string{"env": "test"}
	created, err := s.CreateKey(ctx(t), k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID, PageSize: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Keys) != 1 {
		t.Fatalf("list returned %d keys, want 1", len(res.Keys))
	}
	mutateKeyValue(res.Keys[0])

	again, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("get again: %v", err)
	}
	assertKeyFields(t, again, keySnapshot{
		hash:        hash,
		permissions: []string{"chat:read"},
		limitRefs:   []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}},
		expiresAt:   &expires,
		metadata:    map[string]string{"env": "test"},
	})
}

func testKeyListPreservesAllFields(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	lastVerified := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	hash := bytes.Repeat([]byte{0xe0}, 32)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", hash)
	k.Permissions = []string{"chat:read", "chat:write"}
	k.LimitRefs = []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}}
	k.ExpiresAt = &expires
	k.LastVerifiedAt = &lastVerified
	k.Metadata = map[string]string{"env": "test"}
	if _, err := s.CreateKey(ctx(t), k); err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Keys) != 1 {
		t.Fatalf("list returned %d keys, want 1", len(res.Keys))
	}
	assertKeyFields(t, res.Keys[0], keySnapshot{
		hash:           hash,
		permissions:    []string{"chat:read", "chat:write"},
		limitRefs:      []store.LimitRef{{LimitID: "tokens", ScopeKey: "user:user_1"}},
		expiresAt:      &expires,
		lastVerifiedAt: &lastVerified,
		metadata:       map[string]string{"env": "test"},
	})
}

func testKeyGetByHashUnknown(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.GetKeyByHash(ctx(t), bytes.Repeat([]byte{0x00}, 32))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testKeyGetByIDUnknown(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.GetKeyByID(ctx(t), "key_missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testKeyCreateRejectsDuplicateHash(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	hash := bytes.Repeat([]byte{0x11}, 32)

	if _, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", hash)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_2", hash))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func testKeyRevoke(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	k, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x22}, 32)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.RevokeKey(ctx(t), k.KeyID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, err := s.GetKeyByID(ctx(t), k.KeyID)
	if err != nil {
		t.Fatalf("get after revoke: %v", err)
	}
	if got.Enabled {
		t.Fatal("Enabled should be false after revoke")
	}
}

func testKeyRevokeIsIdempotent(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	k, _ := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x33}, 32)))

	if err := s.RevokeKey(ctx(t), k.KeyID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := s.RevokeKey(ctx(t), k.KeyID); err != nil {
		t.Fatalf("second revoke must be idempotent, got: %v", err)
	}
}

func testKeyRevokeUnknownReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	err := s.RevokeKey(ctx(t), "key_missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testKeyRotate(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	oldHash := bytes.Repeat([]byte{0x44}, 32)
	newHash := bytes.Repeat([]byte{0x55}, 32)
	k, _ := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", oldHash))

	rotated, err := s.RotateKey(ctx(t), k.KeyID, newHash)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.KeyID != k.KeyID {
		t.Fatalf("KeyID changed across rotation: %q -> %q", k.KeyID, rotated.KeyID)
	}
	if !bytes.Equal(rotated.KeyHash, newHash) {
		t.Fatal("KeyHash should be the new hash after rotation")
	}

	if _, err := s.GetKeyByHash(ctx(t), oldHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old hash should no longer resolve, got err = %v", err)
	}
	if _, err := s.GetKeyByHash(ctx(t), newHash); err != nil {
		t.Fatalf("new hash should resolve: %v", err)
	}
}

func testKeyRotateUnknownReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.RotateKey(ctx(t), "key_missing", bytes.Repeat([]byte{0x56}, 32))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testKeyRotateRejectsDuplicateHash(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	hash1 := bytes.Repeat([]byte{0x57}, 32)
	hash2 := bytes.Repeat([]byte{0x58}, 32)
	k, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", hash1))
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_2", hash2)); err != nil {
		t.Fatalf("create second: %v", err)
	}

	_, err = s.RotateKey(ctx(t), k.KeyID, hash2)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	got, err := s.GetKeyByID(ctx(t), k.KeyID)
	if err != nil {
		t.Fatalf("get after rejected rotate: %v", err)
	}
	if !bytes.Equal(got.KeyHash, hash1) {
		t.Fatal("rejected rotate changed the original key hash")
	}
}

func testKeyListByAPI(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a1 := seedAPI(t, s, w.WorkspaceID)
	a2, _ := s.CreateAPI(ctx(t), newAPI(w.WorkspaceID, "staging"))
	_, _ = s.CreateKey(ctx(t), newKey(a1.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x60}, 32)))
	_, _ = s.CreateKey(ctx(t), newKey(a1.APIID, w.WorkspaceID, "user_2", bytes.Repeat([]byte{0x61}, 32)))
	_, _ = s.CreateKey(ctx(t), newKey(a2.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x62}, 32)))

	res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a1.APIID, PageSize: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Keys) != 2 {
		t.Fatalf("got %d keys for api1, want 2", len(res.Keys))
	}
	for _, k := range res.Keys {
		if k.APIID != a1.APIID {
			t.Fatalf("list returned a key from api %q", k.APIID)
		}
	}
}

func testKeyListFilterByOwner(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	_, _ = s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x70}, 32)))
	_, _ = s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_2", bytes.Repeat([]byte{0x71}, 32)))
	_, _ = s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x72}, 32)))

	res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID, OwnerPrincipalID: "user_1", PageSize: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Keys) != 2 {
		t.Fatalf("got %d keys for user_1, want 2", len(res.Keys))
	}
	for _, k := range res.Keys {
		if k.OwnerPrincipalID != "user_1" {
			t.Fatalf("filter leaked: %q", k.OwnerPrincipalID)
		}
	}
}

func testKeyListPaginates(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	for i := 0; i < 5; i++ {
		hash := bytes.Repeat([]byte{byte(0x80 + i)}, 32)
		if _, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", hash)); err != nil {
			t.Fatalf("seed key %d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	token := ""
	for page := 0; page < 10; page++ {
		res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID, PageSize: 2, PageToken: token})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, k := range res.Keys {
			if seen[k.KeyID] {
				t.Fatalf("duplicate key across pages: %s", k.KeyID)
			}
			seen[k.KeyID] = true
		}
		if res.NextPageToken == "" {
			break
		}
		token = res.NextPageToken
	}
	if len(seen) != 5 {
		t.Fatalf("paginated through %d keys, want 5", len(seen))
	}
}

func testKeyListDefaultsPageSize(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	for i := 0; i < 55; i++ {
		hash := bytes.Repeat([]byte{byte(i)}, 32)
		if _, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", hash)); err != nil {
			t.Fatalf("seed key %d: %v", i, err)
		}
	}

	first, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Keys) != 50 {
		t.Fatalf("first page returned %d keys, want default page size 50", len(first.Keys))
	}
	if first.NextPageToken == "" {
		t.Fatal("first page should include a next token")
	}

	second, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Keys) != 5 {
		t.Fatalf("second page returned %d keys, want remaining 5", len(second.Keys))
	}
	if second.NextPageToken != "" {
		t.Fatalf("second page next token = %q, want empty", second.NextPageToken)
	}
}

func testKeyListUnknownAPIIsEmpty(t *testing.T, s store.Store) {
	t.Helper()
	res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: "api_missing", PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Keys) != 0 || res.NextPageToken != "" {
		t.Fatalf("ListKeys for unknown api = %+v, want empty result", res)
	}
}

func testKeyListPageTokenCanSkipToInsertionPoint(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	var ids []string
	for i := 0; i < 3; i++ {
		hash := bytes.Repeat([]byte{byte(0xc0 + i)}, 32)
		k, err := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", hash))
		if err != nil {
			t.Fatalf("seed key %d: %v", i, err)
		}
		ids = append(ids, k.KeyID)
	}
	slices.Sort(ids)
	token := ids[0] + "~"

	res, err := s.ListKeys(ctx(t), store.ListKeysOpts{APIID: a.APIID, PageSize: 10, PageToken: token})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Keys) != 2 {
		t.Fatalf("got %d keys after synthetic token, want 2", len(res.Keys))
	}
	if res.Keys[0].KeyID != ids[1] || res.Keys[1].KeyID != ids[2] {
		t.Fatalf("keys after token = [%s %s], want [%s %s]", res.Keys[0].KeyID, res.Keys[1].KeyID, ids[1], ids[2])
	}
}

func testKeyTouchLastVerified(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	k, _ := s.CreateKey(ctx(t), newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0x90}, 32)))

	at := time.Now().UTC().Truncate(time.Second)
	if err := s.TouchLastVerified(ctx(t), k.KeyID, at); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ := s.GetKeyByID(ctx(t), k.KeyID)
	if got.LastVerifiedAt == nil {
		t.Fatal("LastVerifiedAt should be populated after touch")
	}
	if !got.LastVerifiedAt.Equal(at) {
		t.Fatalf("LastVerifiedAt = %v, want %v", got.LastVerifiedAt, at)
	}
}

func testKeyTouchLastVerifiedUnknownReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	err := s.TouchLastVerified(ctx(t), "key_missing", time.Now().UTC())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func testKeyDecrementRemainingUses(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0xA0}, 32))
	k.RemainingUses = 3
	created, err := s.CreateKey(ctx(t), k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.DecrementRemainingUses(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("decrement: %v", err)
	}
	if got != 2 {
		t.Fatalf("remaining = %d, want 2", got)
	}
	got, _ = s.DecrementRemainingUses(ctx(t), created.KeyID)
	if got != 1 {
		t.Fatalf("remaining = %d, want 1", got)
	}
	got, _ = s.DecrementRemainingUses(ctx(t), created.KeyID)
	if got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
}

func testKeyDecrementUnlimitedIsNoop(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0xB0}, 32))
	k.RemainingUses = -1
	created, _ := s.CreateKey(ctx(t), k)

	got, err := s.DecrementRemainingUses(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("decrement: %v", err)
	}
	if got != -1 {
		t.Fatalf("unlimited key returned remaining = %d, want -1", got)
	}
}

func testKeyDecrementDepletedReturnsErrDepleted(t *testing.T, s store.Store) {
	t.Helper()
	w := seedWorkspace(t, s)
	a := seedAPI(t, s, w.WorkspaceID)
	k := newKey(a.APIID, w.WorkspaceID, "user_1", bytes.Repeat([]byte{0xB1}, 32))
	k.RemainingUses = 0
	created, err := s.CreateKey(ctx(t), k)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.DecrementRemainingUses(ctx(t), created.KeyID)
	if !errors.Is(err, store.ErrDepleted) {
		t.Fatalf("err = %v, want ErrDepleted", err)
	}
	if got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	after, err := s.GetKeyByID(ctx(t), created.KeyID)
	if err != nil {
		t.Fatalf("get after depleted decrement: %v", err)
	}
	if after.RemainingUses != 0 {
		t.Fatalf("RemainingUses = %d, want 0", after.RemainingUses)
	}
}

func testKeyDecrementUnknownReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.DecrementRemainingUses(ctx(t), "key_missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

type keySnapshot struct {
	hash           []byte
	permissions    []string
	limitRefs      []store.LimitRef
	expiresAt      *time.Time
	lastVerifiedAt *time.Time
	metadata       map[string]string
}

func assertKeyFields(t *testing.T, got store.Key, want keySnapshot) {
	t.Helper()
	if !bytes.Equal(got.KeyHash, want.hash) {
		t.Fatalf("KeyHash = %x, want %x", got.KeyHash, want.hash)
	}
	if !slices.Equal(got.Permissions, want.permissions) {
		t.Fatalf("Permissions = %v, want %v", got.Permissions, want.permissions)
	}
	if !slices.Equal(got.LimitRefs, want.limitRefs) {
		t.Fatalf("LimitRefs = %v, want %v", got.LimitRefs, want.limitRefs)
	}
	if !sameOptionalTime(got.ExpiresAt, want.expiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, want.expiresAt)
	}
	if !sameOptionalTime(got.LastVerifiedAt, want.lastVerifiedAt) {
		t.Fatalf("LastVerifiedAt = %v, want %v", got.LastVerifiedAt, want.lastVerifiedAt)
	}
	if !maps.Equal(got.Metadata, want.metadata) {
		t.Fatalf("Metadata = %v, want %v", got.Metadata, want.metadata)
	}
}

func mutateKeyValue(k store.Key) {
	if len(k.KeyHash) > 0 {
		k.KeyHash[0] ^= 0xff
	}
	if len(k.Permissions) > 0 {
		k.Permissions[0] = "mutated"
	}
	if len(k.LimitRefs) > 0 {
		k.LimitRefs[0].LimitID = "mutated"
	}
	if k.ExpiresAt != nil {
		*k.ExpiresAt = k.ExpiresAt.Add(time.Hour)
	}
	if k.LastVerifiedAt != nil {
		*k.LastVerifiedAt = k.LastVerifiedAt.Add(time.Hour)
	}
	if k.Metadata != nil {
		k.Metadata["env"] = "mutated"
	}
}

func sameOptionalTime(got, want *time.Time) bool {
	switch {
	case got == nil || want == nil:
		return got == nil && want == nil
	default:
		return got.Equal(*want)
	}
}
