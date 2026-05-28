// Package conformance is the driver-agnostic test suite every Store
// implementation must pass. New drivers add a single _test.go that calls
// conformance.Run with a Factory; the suite enforces identical behaviour.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/elloloop/keychain/internal/store"
)

// Factory builds a fresh, isolated Store for each top-level subtest.
// Implementations should reset state between calls.
type Factory func(t *testing.T) store.Store

// Run executes the full conformance suite.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("WorkspaceCreateAndGet", func(t *testing.T) { testWorkspaceCreateAndGet(t, newStore(t)) })
	t.Run("WorkspaceGetNotFound", func(t *testing.T) { testWorkspaceGetNotFound(t, newStore(t)) })
	t.Run("ApiCreateRequiresWorkspace", func(t *testing.T) { testAPICreateRequiresWorkspace(t, newStore(t)) })
	t.Run("ApiCreateAndGet", func(t *testing.T) { testAPICreateAndGet(t, newStore(t)) })
	t.Run("KeyCreateRequiresApi", func(t *testing.T) { testKeyCreateRequiresAPI(t, newStore(t)) })
	t.Run("KeyCreateAndLookup", func(t *testing.T) { testKeyCreateAndLookup(t, newStore(t)) })
	t.Run("KeyGetByHashUnknown", func(t *testing.T) { testKeyGetByHashUnknown(t, newStore(t)) })
	t.Run("KeyCreateRejectsDuplicateHash", func(t *testing.T) { testKeyCreateRejectsDuplicateHash(t, newStore(t)) })
	t.Run("KeyRevoke", func(t *testing.T) { testKeyRevoke(t, newStore(t)) })
	t.Run("KeyRevokeIsIdempotent", func(t *testing.T) { testKeyRevokeIsIdempotent(t, newStore(t)) })
	t.Run("KeyRevokeUnknownReturnsNotFound", func(t *testing.T) { testKeyRevokeUnknownReturnsNotFound(t, newStore(t)) })
	t.Run("KeyRotate", func(t *testing.T) { testKeyRotate(t, newStore(t)) })
	t.Run("KeyListByApi", func(t *testing.T) { testKeyListByAPI(t, newStore(t)) })
	t.Run("KeyListFilterByOwner", func(t *testing.T) { testKeyListFilterByOwner(t, newStore(t)) })
	t.Run("KeyListPaginates", func(t *testing.T) { testKeyListPaginates(t, newStore(t)) })
	t.Run("KeyTouchLastVerified", func(t *testing.T) { testKeyTouchLastVerified(t, newStore(t)) })
	t.Run("KeyDecrementRemainingUses", func(t *testing.T) { testKeyDecrementRemainingUses(t, newStore(t)) })
	t.Run("KeyDecrementUnlimitedIsNoop", func(t *testing.T) { testKeyDecrementUnlimitedIsNoop(t, newStore(t)) })
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

func testKeyGetByHashUnknown(t *testing.T, s store.Store) {
	t.Helper()
	_, err := s.GetKeyByHash(ctx(t), bytes.Repeat([]byte{0x00}, 32))
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
