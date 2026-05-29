package keychainserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/keychainserver/store"
	"github.com/elloloop/keychain/keychainserver/store/memory"
)

func TestNewRejectsMissingStore(t *testing.T) {
	_, err := New(context.Background(), Options{})
	if err == nil {
		t.Fatal("New accepted nil Store")
	}
}

func TestDiscardWriterReportsBytesWritten(t *testing.T) {
	n, err := discardWriter{}.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("hello") {
		t.Fatalf("n = %d, want %d", n, len("hello"))
	}
}

func TestMapStoreErrCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"not found", store.ErrNotFound, codes.NotFound},
		{"conflict", store.ErrConflict, codes.AlreadyExists},
		{"internal", errors.New("boom"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapStoreErr(tc.err)
			if status.Code(got) != tc.want {
				t.Fatalf("code = %v, want %v", status.Code(got), tc.want)
			}
		})
	}
}

func TestCreateKeyMapsKeyGenerationFailure(t *testing.T) {
	svc, apiID := seededInternalServer(t)
	orig := newKeyMaterial
	newKeyMaterial = func(string) (string, [32]byte, error) {
		return "", [32]byte{}, errors.New("entropy unavailable")
	}
	t.Cleanup(func() { newKeyMaterial = orig })

	_, err := svc.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{
		ApiId:            apiID,
		OwnerPrincipalId: "owner",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal; err = %v", status.Code(err), err)
	}
}

func TestRotateKeyMapsKeyGenerationFailure(t *testing.T) {
	svc, apiID := seededInternalServer(t)
	key, err := svc.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{
		ApiId:            apiID,
		OwnerPrincipalId: "owner",
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	orig := newKeyMaterial
	newKeyMaterial = func(string) (string, [32]byte, error) {
		return "", [32]byte{}, errors.New("entropy unavailable")
	}
	t.Cleanup(func() { newKeyMaterial = orig })

	_, err = svc.RotateKey(context.Background(), &apikeyv1.RotateKeyRequest{KeyId: key.GetKey().GetKeyId()})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal; err = %v", status.Code(err), err)
	}
}

func TestRPCsMapInternalStoreErrors(t *testing.T) {
	base := memory.New()
	api := store.API{APIID: "api_1", WorkspaceID: "ws_1", KeyPrefix: "ck_test_"}
	key := store.Key{KeyID: "key_1", APIID: api.APIID, WorkspaceID: api.WorkspaceID, KeyHash: []byte("hash"), Enabled: true, RemainingUses: -1}
	internalErr := errors.New("database unavailable")

	cases := []struct {
		name string
		st   store.Store
		call func(*Server) error
	}{
		{
			name: "CreateWorkspace",
			st: overrideStore{
				Store: base,
				createWorkspace: func(context.Context, store.Workspace) (store.Workspace, error) {
					return store.Workspace{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.CreateWorkspace(context.Background(), &apikeyv1.CreateWorkspaceRequest{Name: "acme", OwnerPrincipalId: "owner"})
				return err
			},
		},
		{
			name: "GetWorkspace",
			st: overrideStore{
				Store: base,
				getWorkspace: func(context.Context, string) (store.Workspace, error) {
					return store.Workspace{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.GetWorkspace(context.Background(), &apikeyv1.GetWorkspaceRequest{WorkspaceId: "ws_1"})
				return err
			},
		},
		{
			name: "CreateApi",
			st: overrideStore{
				Store: base,
				createAPI: func(context.Context, store.API) (store.API, error) {
					return store.API{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{WorkspaceId: "ws_1", Name: "prod"})
				return err
			},
		},
		{
			name: "GetApi",
			st: overrideStore{
				Store: base,
				getAPI: func(context.Context, string) (store.API, error) {
					return store.API{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.GetApi(context.Background(), &apikeyv1.GetApiRequest{ApiId: "api_1"})
				return err
			},
		},
		{
			name: "CreateKeyCreateFails",
			st: overrideStore{
				Store: base,
				getAPI: func(context.Context, string) (store.API, error) {
					return api, nil
				},
				createKey: func(context.Context, store.Key) (store.Key, error) {
					return store.Key{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{ApiId: api.APIID, OwnerPrincipalId: "owner"})
				return err
			},
		},
		{
			name: "RevokeKey",
			st: overrideStore{
				Store: base,
				revokeKey: func(context.Context, string) error {
					return internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.RevokeKey(context.Background(), &apikeyv1.RevokeKeyRequest{KeyId: key.KeyID})
				return err
			},
		},
		{
			name: "RotateKeyGetKeyFails",
			st: overrideStore{
				Store: base,
				getKeyByID: func(context.Context, string) (store.Key, error) {
					return store.Key{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.RotateKey(context.Background(), &apikeyv1.RotateKeyRequest{KeyId: key.KeyID})
				return err
			},
		},
		{
			name: "RotateKeyGetApiFails",
			st: overrideStore{
				Store: base,
				getKeyByID: func(context.Context, string) (store.Key, error) {
					return key, nil
				},
				getAPI: func(context.Context, string) (store.API, error) {
					return store.API{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.RotateKey(context.Background(), &apikeyv1.RotateKeyRequest{KeyId: key.KeyID})
				return err
			},
		},
		{
			name: "RotateKeyUpdateFails",
			st: overrideStore{
				Store: base,
				getKeyByID: func(context.Context, string) (store.Key, error) {
					return key, nil
				},
				getAPI: func(context.Context, string) (store.API, error) {
					return api, nil
				},
				rotateKey: func(context.Context, string, []byte) (store.Key, error) {
					return store.Key{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.RotateKey(context.Background(), &apikeyv1.RotateKeyRequest{KeyId: key.KeyID})
				return err
			},
		},
		{
			name: "ListKeys",
			st: overrideStore{
				Store: base,
				listKeys: func(context.Context, store.ListKeysOpts) (store.ListKeysResult, error) {
					return store.ListKeysResult{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.ListKeys(context.Background(), &apikeyv1.ListKeysRequest{ApiId: api.APIID})
				return err
			},
		},
		{
			name: "VerifyLookupFails",
			st: overrideStore{
				Store: base,
				getKeyByHash: func(context.Context, []byte) (store.Key, error) {
					return store.Key{}, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: "ck_test_plaintext"})
				return err
			},
		},
		{
			name: "VerifyDecrementFails",
			st: overrideStore{
				Store: base,
				getKeyByHash: func(context.Context, []byte) (store.Key, error) {
					k := key
					k.RemainingUses = 1
					return k, nil
				},
				decrementRemainingUses: func(context.Context, string) (int64, error) {
					return 0, internalErr
				},
			},
			call: func(s *Server) error {
				_, err := s.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: "ck_test_plaintext"})
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := New(context.Background(), Options{Store: tc.st})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = tc.call(svc)
			if status.Code(err) != codes.Internal {
				t.Fatalf("code = %v, want Internal; err = %v", status.Code(err), err)
			}
		})
	}
}

type overrideStore struct {
	store.Store
	createWorkspace        func(context.Context, store.Workspace) (store.Workspace, error)
	getWorkspace           func(context.Context, string) (store.Workspace, error)
	createAPI              func(context.Context, store.API) (store.API, error)
	getAPI                 func(context.Context, string) (store.API, error)
	createKey              func(context.Context, store.Key) (store.Key, error)
	getKeyByID             func(context.Context, string) (store.Key, error)
	getKeyByHash           func(context.Context, []byte) (store.Key, error)
	revokeKey              func(context.Context, string) error
	rotateKey              func(context.Context, string, []byte) (store.Key, error)
	listKeys               func(context.Context, store.ListKeysOpts) (store.ListKeysResult, error)
	decrementRemainingUses func(context.Context, string) (int64, error)
}

func (s overrideStore) CreateWorkspace(ctx context.Context, w store.Workspace) (store.Workspace, error) {
	if s.createWorkspace != nil {
		return s.createWorkspace(ctx, w)
	}
	return s.Store.CreateWorkspace(ctx, w)
}

func (s overrideStore) GetWorkspace(ctx context.Context, id string) (store.Workspace, error) {
	if s.getWorkspace != nil {
		return s.getWorkspace(ctx, id)
	}
	return s.Store.GetWorkspace(ctx, id)
}

func (s overrideStore) CreateAPI(ctx context.Context, a store.API) (store.API, error) {
	if s.createAPI != nil {
		return s.createAPI(ctx, a)
	}
	return s.Store.CreateAPI(ctx, a)
}

func (s overrideStore) GetAPI(ctx context.Context, id string) (store.API, error) {
	if s.getAPI != nil {
		return s.getAPI(ctx, id)
	}
	return s.Store.GetAPI(ctx, id)
}

func (s overrideStore) CreateKey(ctx context.Context, k store.Key) (store.Key, error) {
	if s.createKey != nil {
		return s.createKey(ctx, k)
	}
	return s.Store.CreateKey(ctx, k)
}

func (s overrideStore) GetKeyByID(ctx context.Context, id string) (store.Key, error) {
	if s.getKeyByID != nil {
		return s.getKeyByID(ctx, id)
	}
	return s.Store.GetKeyByID(ctx, id)
}

func (s overrideStore) GetKeyByHash(ctx context.Context, hash []byte) (store.Key, error) {
	if s.getKeyByHash != nil {
		return s.getKeyByHash(ctx, hash)
	}
	return s.Store.GetKeyByHash(ctx, hash)
}

func (s overrideStore) RevokeKey(ctx context.Context, id string) error {
	if s.revokeKey != nil {
		return s.revokeKey(ctx, id)
	}
	return s.Store.RevokeKey(ctx, id)
}

func (s overrideStore) RotateKey(ctx context.Context, id string, newHash []byte) (store.Key, error) {
	if s.rotateKey != nil {
		return s.rotateKey(ctx, id, newHash)
	}
	return s.Store.RotateKey(ctx, id, newHash)
}

func (s overrideStore) ListKeys(ctx context.Context, opts store.ListKeysOpts) (store.ListKeysResult, error) {
	if s.listKeys != nil {
		return s.listKeys(ctx, opts)
	}
	return s.Store.ListKeys(ctx, opts)
}

func (s overrideStore) DecrementRemainingUses(ctx context.Context, id string) (int64, error) {
	if s.decrementRemainingUses != nil {
		return s.decrementRemainingUses(ctx, id)
	}
	return s.Store.DecrementRemainingUses(ctx, id)
}

func (s overrideStore) TouchLastVerified(ctx context.Context, id string, at time.Time) error {
	return s.Store.TouchLastVerified(ctx, id, at)
}

func seededInternalServer(t *testing.T) (*Server, string) {
	t.Helper()
	svc, err := New(context.Background(), Options{Store: memory.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ws, err := svc.CreateWorkspace(context.Background(), &apikeyv1.CreateWorkspaceRequest{
		Name:             "acme",
		OwnerPrincipalId: "owner",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	api, err := svc.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{
		WorkspaceId: ws.GetWorkspace().GetWorkspaceId(),
		Name:        "prod",
	})
	if err != nil {
		t.Fatalf("CreateApi: %v", err)
	}
	return svc, api.GetApi().GetApiId()
}
