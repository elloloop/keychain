// Package memory is the in-process Store driver. It is the differential
// reference for tests and a workable default for single-instance
// deployments that do not need persistence — every keychain restart
// invalidates every issued key.
package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/elloloop/keychain/internal/store"
)

// Store is an in-memory implementation of store.Store. Safe for concurrent
// use via a single Mutex around every read and write.
type Store struct {
	mu         sync.Mutex
	workspaces map[string]store.Workspace
	apis       map[string]store.API
	keys       map[string]store.Key
	hashIndex  map[string]string // string(hash) -> keyID
	now        func() time.Time
}

// New returns an empty in-memory Store.
func New() *Store {
	return &Store{
		workspaces: map[string]store.Workspace{},
		apis:       map[string]store.API{},
		keys:       map[string]store.Key{},
		hashIndex:  map[string]string{},
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// ---------------------------------------------------------------------------
// Workspace
// ---------------------------------------------------------------------------

func (s *Store) CreateWorkspace(_ context.Context, w store.Workspace) (store.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	w.WorkspaceID = newID("ws")
	w.CreatedAt = now
	w.UpdatedAt = now
	s.workspaces[w.WorkspaceID] = w
	return w, nil
}

func (s *Store) GetWorkspace(_ context.Context, id string) (store.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.workspaces[id]
	if !ok {
		return store.Workspace{}, store.ErrNotFound
	}
	return w, nil
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

func (s *Store) CreateAPI(_ context.Context, a store.API) (store.API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.workspaces[a.WorkspaceID]; !ok {
		return store.API{}, fmt.Errorf("workspace %q: %w", a.WorkspaceID, store.ErrNotFound)
	}
	now := s.now()
	a.APIID = newID("api")
	a.CreatedAt = now
	a.UpdatedAt = now
	s.apis[a.APIID] = a
	return a, nil
}

func (s *Store) GetAPI(_ context.Context, id string) (store.API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.apis[id]
	if !ok {
		return store.API{}, store.ErrNotFound
	}
	return a, nil
}

// ---------------------------------------------------------------------------
// Key
// ---------------------------------------------------------------------------

func (s *Store) CreateKey(_ context.Context, k store.Key) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.apis[k.APIID]; !ok {
		return store.Key{}, fmt.Errorf("api %q: %w", k.APIID, store.ErrNotFound)
	}
	hashKey := string(k.KeyHash)
	if _, exists := s.hashIndex[hashKey]; exists {
		return store.Key{}, store.ErrConflict
	}

	now := s.now()
	k.KeyID = newID("key")
	k.CreatedAt = now
	k.UpdatedAt = now
	// Defensive copy so callers mutating their input slice can't corrupt
	// our stored state.
	k.KeyHash = append([]byte(nil), k.KeyHash...)
	s.keys[k.KeyID] = k
	s.hashIndex[hashKey] = k.KeyID
	return k, nil
}

func (s *Store) GetKeyByID(_ context.Context, id string) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return store.Key{}, store.ErrNotFound
	}
	return k, nil
}

func (s *Store) GetKeyByHash(_ context.Context, hash []byte) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.hashIndex[string(hash)]
	if !ok {
		return store.Key{}, store.ErrNotFound
	}
	return s.keys[id], nil
}

func (s *Store) RevokeKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return store.ErrNotFound
	}
	if !k.Enabled {
		return nil
	}
	k.Enabled = false
	k.UpdatedAt = s.now()
	s.keys[id] = k
	return nil
}

func (s *Store) RotateKey(_ context.Context, id string, newHash []byte) (store.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return store.Key{}, store.ErrNotFound
	}
	newHashKey := string(newHash)
	if existingID, exists := s.hashIndex[newHashKey]; exists && existingID != id {
		return store.Key{}, store.ErrConflict
	}

	delete(s.hashIndex, string(k.KeyHash))
	k.KeyHash = append([]byte(nil), newHash...)
	k.UpdatedAt = s.now()
	s.keys[id] = k
	s.hashIndex[newHashKey] = id
	return k, nil
}

func (s *Store) ListKeys(_ context.Context, opts store.ListKeysOpts) (store.ListKeysResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ids []string
	for id, k := range s.keys {
		if k.APIID != opts.APIID {
			continue
		}
		if opts.OwnerPrincipalID != "" && k.OwnerPrincipalID != opts.OwnerPrincipalID {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	start := 0
	if opts.PageToken != "" {
		// Find the first ID strictly greater than the token. sort.SearchStrings
		// returns the insertion index for the token; advancing by 1 if the token
		// exactly matches gives us "first ID after the token".
		i := sort.SearchStrings(ids, opts.PageToken)
		if i < len(ids) && ids[i] == opts.PageToken {
			i++
		}
		start = i
	}
	pageSize := int(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	end := start + pageSize
	if end > len(ids) {
		end = len(ids)
	}

	out := make([]store.Key, 0, end-start)
	for _, id := range ids[start:end] {
		out = append(out, s.keys[id])
	}

	next := ""
	if end < len(ids) {
		next = ids[end-1]
	}
	return store.ListKeysResult{Keys: out, NextPageToken: next}, nil
}

func (s *Store) TouchLastVerified(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return store.ErrNotFound
	}
	at = at.UTC()
	k.LastVerifiedAt = &at
	s.keys[id] = k
	return nil
}

func (s *Store) DecrementRemainingUses(_ context.Context, id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return 0, store.ErrNotFound
	}
	if k.RemainingUses < 0 {
		return -1, nil
	}
	if k.RemainingUses == 0 {
		return 0, errors.New("memory: remaining uses already 0")
	}
	k.RemainingUses--
	k.UpdatedAt = s.now()
	s.keys[id] = k
	return k.RemainingUses, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newID(prefix string) string {
	return prefix + "_" + uuid.NewString()
}
