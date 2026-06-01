package keychainserver_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/keychainserver"
	"github.com/elloloop/keychain/keychainserver/store"
	"github.com/elloloop/keychain/keychainserver/store/memory"
)

// fakeRateLimiter is a programmable RateLimiter for tests. Reads `decisions`
// to return; if `err` is set, returns the error instead. Records every call
// so tests can assert "Consume was called with these refs and this cost."
type fakeRateLimiter struct {
	decisions []keychainserver.LimitDecision
	err       error
	calls     []fakeRLCall
}

type fakeRLCall struct {
	refs []store.LimitRef
	cost int64
	id   string
}

func (f *fakeRateLimiter) Consume(_ context.Context, refs []store.LimitRef, cost int64, id string) ([]keychainserver.LimitDecision, error) {
	f.calls = append(f.calls, fakeRLCall{refs: refs, cost: cost, id: id})
	if f.err != nil {
		return nil, f.err
	}
	return f.decisions, nil
}

// allowDecisions builds a slice where every supplied ref is allowed with
// 1000 remaining — the trivial happy-path fixture.
func allowDecisions(refs []store.LimitRef) []keychainserver.LimitDecision {
	out := make([]keychainserver.LimitDecision, 0, len(refs))
	for _, r := range refs {
		out = append(out, keychainserver.LimitDecision{
			LimitID: r.LimitID, ScopeKey: r.ScopeKey,
			Allowed: true, Remaining: 1000,
		})
	}
	return out
}

// ----- test rig --------------------------------------------------------------

type rig struct {
	svc *keychainserver.Server
	st  store.Store
	rl  *fakeRateLimiter

	apiID       string
	workspaceID string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	rl := &fakeRateLimiter{}
	return newRigWithRateLimiter(t, rl)
}

func newRigWithRateLimiter(t *testing.T, rl keychainserver.RateLimiter) *rig {
	t.Helper()
	st := memory.New()
	svc, err := keychainserver.New(context.Background(), keychainserver.Options{
		Store:       st,
		RateLimiter: rl,
	})
	if err != nil {
		t.Fatalf("keychainserver.New: %v", err)
	}
	fakeRL, _ := rl.(*fakeRateLimiter)

	ws, err := svc.CreateWorkspace(context.Background(), &apikeyv1.CreateWorkspaceRequest{
		Name:             "acme",
		OwnerPrincipalId: "user_owner",
	})
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	api, err := svc.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{
		WorkspaceId: ws.GetWorkspace().GetWorkspaceId(),
		Name:        "prod",
		KeyPrefix:   "ck_test_",
	})
	if err != nil {
		t.Fatalf("seed api: %v", err)
	}
	return &rig{
		svc:         svc,
		st:          st,
		rl:          fakeRL,
		apiID:       api.GetApi().GetApiId(),
		workspaceID: ws.GetWorkspace().GetWorkspaceId(),
	}
}

func (r *rig) createKey(t *testing.T, mut func(*apikeyv1.CreateKeyRequest)) (*apikeyv1.ApiKey, string) {
	t.Helper()
	req := &apikeyv1.CreateKeyRequest{
		ApiId:            r.apiID,
		OwnerPrincipalId: "user_1",
		Name:             "test-key",
		RemainingUses:    -1,
	}
	if mut != nil {
		mut(req)
	}
	resp, err := r.svc.CreateKey(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	return resp.GetKey(), resp.GetPlaintext()
}

func requireStatusCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("err code = %v, want %v; err = %v", status.Code(err), want, err)
	}
}

// ----- Workspace ------------------------------------------------------------

func TestWorkspaceRPCsValidateRequiredFieldsAndRoundTripMetadata(t *testing.T) {
	r := newRig(t)

	_, err := r.svc.CreateWorkspace(context.Background(), &apikeyv1.CreateWorkspaceRequest{
		OwnerPrincipalId: "owner",
	})
	requireStatusCode(t, err, codes.InvalidArgument)

	_, err = r.svc.CreateWorkspace(context.Background(), &apikeyv1.CreateWorkspaceRequest{
		Name: "acme",
	})
	requireStatusCode(t, err, codes.InvalidArgument)

	created, err := r.svc.CreateWorkspace(context.Background(), &apikeyv1.CreateWorkspaceRequest{
		Name:             "with-metadata",
		OwnerPrincipalId: "owner",
		Metadata:         map[string]string{"tier": "prod"},
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	_, err = r.svc.GetWorkspace(context.Background(), &apikeyv1.GetWorkspaceRequest{})
	requireStatusCode(t, err, codes.InvalidArgument)

	got, err := r.svc.GetWorkspace(context.Background(), &apikeyv1.GetWorkspaceRequest{
		WorkspaceId: created.GetWorkspace().GetWorkspaceId(),
	})
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.GetWorkspace().GetMetadata()["tier"] != "prod" {
		t.Fatalf("metadata = %v, want tier=prod", got.GetWorkspace().GetMetadata())
	}
}

// ----- Api ------------------------------------------------------------------

func TestApiRPCsValidateRequiredFieldsAndRoundTripMetadata(t *testing.T) {
	r := newRig(t)

	_, err := r.svc.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{Name: "prod"})
	requireStatusCode(t, err, codes.InvalidArgument)

	_, err = r.svc.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{WorkspaceId: r.workspaceID})
	requireStatusCode(t, err, codes.InvalidArgument)

	_, err = r.svc.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{
		WorkspaceId: "ws_missing",
		Name:        "prod",
	})
	requireStatusCode(t, err, codes.NotFound)

	created, err := r.svc.CreateApi(context.Background(), &apikeyv1.CreateApiRequest{
		WorkspaceId: r.workspaceID,
		Name:        "metadata-api",
		KeyPrefix:   "ck_meta_",
		Metadata:    map[string]string{"region": "eu"},
	})
	if err != nil {
		t.Fatalf("CreateApi: %v", err)
	}

	_, err = r.svc.GetApi(context.Background(), &apikeyv1.GetApiRequest{})
	requireStatusCode(t, err, codes.InvalidArgument)

	got, err := r.svc.GetApi(context.Background(), &apikeyv1.GetApiRequest{ApiId: created.GetApi().GetApiId()})
	if err != nil {
		t.Fatalf("GetApi: %v", err)
	}
	if got.GetApi().GetKeyPrefix() != "ck_meta_" {
		t.Fatalf("KeyPrefix = %q, want ck_meta_", got.GetApi().GetKeyPrefix())
	}
	if got.GetApi().GetMetadata()["region"] != "eu" {
		t.Fatalf("metadata = %v, want region=eu", got.GetApi().GetMetadata())
	}
}

// ----- CreateKey -------------------------------------------------------------

func TestCreateKeyReturnsPlaintextAndAssignsIDs(t *testing.T) {
	r := newRig(t)
	key, plaintext := r.createKey(t, nil)

	if plaintext == "" {
		t.Fatal("plaintext must be returned exactly once at creation")
	}
	if !strings.HasPrefix(plaintext, "ck_test_") {
		t.Fatalf("plaintext = %q, want prefix from the API", plaintext)
	}
	if key.GetKeyId() == "" {
		t.Fatal("KeyId should be assigned")
	}
	if key.GetApiId() != r.apiID {
		t.Fatalf("ApiId = %q, want %q", key.GetApiId(), r.apiID)
	}
	if key.GetWorkspaceId() != r.workspaceID {
		t.Fatalf("WorkspaceId = %q, want %q", key.GetWorkspaceId(), r.workspaceID)
	}
	if !key.GetEnabled() {
		t.Fatal("new key should be enabled")
	}
}

// Proto3 cannot distinguish "field omitted" from "0", so CreateKey treats
// both as unlimited credit instead of issuing an unusable key.
func TestCreateKeyOmittedRemainingUsesIsUnlimited(t *testing.T) {
	r := newRig(t)
	key, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.RemainingUses = 0
	})
	if key.GetRemainingUses() != -1 {
		t.Fatalf("RemainingUses = %d, want -1 (unlimited)", key.GetRemainingUses())
	}
	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
		t.Fatalf("Result = %v, want VALID", resp.GetResult())
	}
}

func TestCreateKeyRequiresApiID(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{
		OwnerPrincipalId: "user_1",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestCreateKeyRejectsUnknownApi(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{
		ApiId:            "api_missing",
		OwnerPrincipalId: "user_1",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err code = %v, want NotFound", status.Code(err))
	}
}

func TestCreateKeyRequiresOwnerPrincipalID(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{ApiId: r.apiID})
	requireStatusCode(t, err, codes.InvalidArgument)
}

func TestCreateAndGetKeyRoundTripsFields(t *testing.T) {
	r := newRig(t)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	key, _ := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.Name = "full-key"
		req.Permissions = []string{"chat:read", "chat:write"}
		req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "tokens", ScopeKey: "user:user_1"}}
		req.ExpiresAt = timestamppb.New(expires)
		req.RemainingUses = 2
		req.Metadata = map[string]string{"env": "test"}
	})

	got, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: key.GetKeyId()})
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if got.GetKey().GetName() != "full-key" {
		t.Fatalf("Name = %q, want full-key", got.GetKey().GetName())
	}
	if strings.Join(got.GetKey().GetPermissions(), ",") != "chat:read,chat:write" {
		t.Fatalf("Permissions = %v", got.GetKey().GetPermissions())
	}
	if len(got.GetKey().GetLimitRefs()) != 1 || got.GetKey().GetLimitRefs()[0].GetLimitId() != "tokens" {
		t.Fatalf("LimitRefs = %v", got.GetKey().GetLimitRefs())
	}
	if !got.GetKey().GetExpiresAt().AsTime().Equal(expires) {
		t.Fatalf("ExpiresAt = %v, want %v", got.GetKey().GetExpiresAt().AsTime(), expires)
	}
	if got.GetKey().GetRemainingUses() != 2 {
		t.Fatalf("RemainingUses = %d, want 2", got.GetKey().GetRemainingUses())
	}
	if got.GetKey().GetMetadata()["env"] != "test" {
		t.Fatalf("Metadata = %v, want env=test", got.GetKey().GetMetadata())
	}
}

func TestKeyRPCsValidateRequiredIDs(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{})
	requireStatusCode(t, err, codes.InvalidArgument)

	_, err = r.svc.RevokeKey(context.Background(), &apikeyv1.RevokeKeyRequest{})
	requireStatusCode(t, err, codes.InvalidArgument)

	_, err = r.svc.RotateKey(context.Background(), &apikeyv1.RotateKeyRequest{})
	requireStatusCode(t, err, codes.InvalidArgument)

	_, err = r.svc.ListKeys(context.Background(), &apikeyv1.ListKeysRequest{})
	requireStatusCode(t, err, codes.InvalidArgument)
}

// ----- VerifyKey: happy path -------------------------------------------------

func TestVerifyKeyValidNoLimits(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, nil)

	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext: plaintext,
	})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if !resp.GetValid() {
		t.Fatalf("Valid = false, want true; result = %v", resp.GetResult())
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
		t.Fatalf("Result = %v, want VALID", resp.GetResult())
	}
	if len(r.rl.calls) != 0 {
		t.Fatal("rate-limiter should not be called for a key with no limit_refs")
	}
}

func TestVerifyKeyValidWithLimitsCallsRateLimiter(t *testing.T) {
	r := newRig(t)
	refs := []*apikeyv1.LimitRef{
		{LimitId: "user_daily_tokens", ScopeKey: "user:user_1:daily"},
		{LimitId: "org_monthly_tokens", ScopeKey: "org:acme:monthly"},
	}
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.LimitRefs = refs
	})
	r.rl.decisions = allowDecisions([]store.LimitRef{
		{LimitID: "user_daily_tokens", ScopeKey: "user:user_1:daily"},
		{LimitID: "org_monthly_tokens", ScopeKey: "org:acme:monthly"},
	})

	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext: plaintext,
		Cost:      1024,
		RequestId: "req_abc",
	})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if !resp.GetValid() {
		t.Fatalf("Valid = false, want true; result = %v", resp.GetResult())
	}
	if len(r.rl.calls) != 1 {
		t.Fatalf("rate-limiter called %d times, want 1", len(r.rl.calls))
	}
	call := r.rl.calls[0]
	if call.cost != 1024 {
		t.Fatalf("cost forwarded = %d, want 1024", call.cost)
	}
	if call.id != "req_abc" {
		t.Fatalf("request_id forwarded = %q, want %q", call.id, "req_abc")
	}
	if len(call.refs) != 2 {
		t.Fatalf("refs forwarded = %d, want 2", len(call.refs))
	}
	if len(resp.GetLimitDecisions()) != 2 {
		t.Fatalf("LimitDecisions returned = %d, want 2", len(resp.GetLimitDecisions()))
	}
}

func TestVerifyKeyLimitsDefaultCostToOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		cost int64
	}{
		{name: "omitted"},
		{name: "negative", cost: -50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
				req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "requests", ScopeKey: "user:user_1"}}
			})
			r.rl.decisions = allowDecisions([]store.LimitRef{{LimitID: "requests", ScopeKey: "user:user_1"}})

			resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
				Plaintext: plaintext,
				Cost:      tc.cost,
			})
			if err != nil {
				t.Fatalf("VerifyKey: %v", err)
			}
			if !resp.GetValid() {
				t.Fatalf("Valid = false, want true; result = %v", resp.GetResult())
			}
			if len(r.rl.calls) != 1 {
				t.Fatalf("rate-limiter called %d times, want 1", len(r.rl.calls))
			}
			if r.rl.calls[0].cost != 1 {
				t.Fatalf("cost forwarded = %d, want default cost 1", r.rl.calls[0].cost)
			}
		})
	}
}

func TestVerifyKeySkipRatelimitBypassesClient(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "x", ScopeKey: "y"}}
	})

	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext:     plaintext,
		SkipRatelimit: true,
		Cost:          42,
	})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if !resp.GetValid() {
		t.Fatalf("Valid = false; result = %v", resp.GetResult())
	}
	if len(r.rl.calls) != 0 {
		t.Fatal("rate-limiter must not be called when skip_ratelimit is true")
	}
}

func TestVerifyKeyRequiredPermissionsAllPresent(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.Permissions = []string{"chat:read", "chat:write", "rerank:read"}
	})

	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext:           plaintext,
		RequiredPermissions: []string{"chat:read", "rerank:read"},
	})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
		t.Fatalf("Result = %v, want VALID", resp.GetResult())
	}
}

// ----- VerifyKey: rejection paths -------------------------------------------

func TestVerifyKeyUnknownPlaintext(t *testing.T) {
	r := newRig(t)
	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext: "ck_test_does_not_exist",
	})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("Valid should be false for unknown plaintext")
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_NOT_FOUND {
		t.Fatalf("Result = %v, want NOT_FOUND", resp.GetResult())
	}
	if resp.GetKeyId() != "" {
		t.Fatal("KeyId should be empty when key is not found")
	}
}

func TestVerifyKeyEmptyPlaintext(t *testing.T) {
	r := newRig(t)
	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext: "",
	})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_NOT_FOUND {
		t.Fatalf("Result = %v, want NOT_FOUND", resp.GetResult())
	}
}

func TestVerifyKeyRevoked(t *testing.T) {
	r := newRig(t)
	key, plaintext := r.createKey(t, nil)
	if _, err := r.svc.RevokeKey(context.Background(), &apikeyv1.RevokeKeyRequest{KeyId: key.GetKeyId()}); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("Valid should be false for revoked key")
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_REVOKED {
		t.Fatalf("Result = %v, want REVOKED", resp.GetResult())
	}
	if resp.GetKeyId() != key.GetKeyId() {
		t.Fatal("KeyId should be populated on rejected lookup so the gateway can log it")
	}
}

func TestVerifyKeyExpired(t *testing.T) {
	r := newRig(t)
	past := time.Now().UTC().Add(-time.Hour)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.ExpiresAt = timestamppb.New(past)
	})

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_EXPIRED {
		t.Fatalf("Result = %v, want EXPIRED", resp.GetResult())
	}
}

func TestVerifyKeyDepletedCredit(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.RemainingUses = 1
	})

	// First verify: VALID, decrements to 0.
	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
		t.Fatalf("first verify: Result = %v, want VALID", resp.GetResult())
	}
	// Second verify: DEPLETED.
	resp, _ = r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_DEPLETED {
		t.Fatalf("second verify: Result = %v, want DEPLETED", resp.GetResult())
	}
}

func TestVerifyKeyForbiddenMissingPermission(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.Permissions = []string{"chat:read"}
	})

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext:           plaintext,
		RequiredPermissions: []string{"chat:write"},
	})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_FORBIDDEN {
		t.Fatalf("Result = %v, want FORBIDDEN", resp.GetResult())
	}
}

func TestVerifyKeyForbiddenSubset(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.Permissions = []string{"chat:write", "rerank:read"}
	})

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
		Plaintext:           plaintext,
		RequiredPermissions: []string{"chat:write", "rerank:write"}, // last one missing
	})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_FORBIDDEN {
		t.Fatalf("Result = %v, want FORBIDDEN", resp.GetResult())
	}
}

func TestVerifyKeyRateLimited(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "tpm", ScopeKey: "openai"}}
	})
	r.rl.decisions = []keychainserver.LimitDecision{{LimitID: "tpm", ScopeKey: "openai", Allowed: false, RetryAfterMs: 5000}}

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext, Cost: 10})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_RATE_LIMITED {
		t.Fatalf("Result = %v, want RATE_LIMITED", resp.GetResult())
	}
	if len(resp.GetLimitDecisions()) != 1 || resp.GetLimitDecisions()[0].GetRetryAfterMs() != 5000 {
		t.Fatalf("LimitDecisions not forwarded: %+v", resp.GetLimitDecisions())
	}
}

func TestVerifyKeyResponseFieldsForDecisionMatrix(t *testing.T) {
	r := newRig(t)

	assertIdentity := func(t *testing.T, resp *apikeyv1.VerifyKeyResponse, key *apikeyv1.ApiKey) {
		t.Helper()
		if resp.GetKeyId() != key.GetKeyId() {
			t.Fatalf("KeyId = %q, want %q", resp.GetKeyId(), key.GetKeyId())
		}
		if resp.GetApiId() != key.GetApiId() {
			t.Fatalf("ApiId = %q, want %q", resp.GetApiId(), key.GetApiId())
		}
		if resp.GetWorkspaceId() != key.GetWorkspaceId() {
			t.Fatalf("WorkspaceId = %q, want %q", resp.GetWorkspaceId(), key.GetWorkspaceId())
		}
		if resp.GetOwnerPrincipalId() != key.GetOwnerPrincipalId() {
			t.Fatalf("OwnerPrincipalId = %q, want %q", resp.GetOwnerPrincipalId(), key.GetOwnerPrincipalId())
		}
	}

	t.Run("not found omits key identity", func(t *testing.T) {
		resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: "ck_test_missing"})
		if err != nil {
			t.Fatalf("VerifyKey: %v", err)
		}
		if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_NOT_FOUND {
			t.Fatalf("Result = %v, want NOT_FOUND", resp.GetResult())
		}
		if resp.GetValid() || resp.GetKeyId() != "" || resp.GetApiId() != "" || resp.GetWorkspaceId() != "" {
			t.Fatalf("not-found response leaked identity fields: %+v", resp)
		}
	})

	t.Run("valid includes identity and permissions", func(t *testing.T) {
		key, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
			req.Permissions = []string{"read", "write"}
		})
		resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
		if err != nil {
			t.Fatalf("VerifyKey: %v", err)
		}
		if !resp.GetValid() || resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
			t.Fatalf("response = %+v, want VALID", resp)
		}
		assertIdentity(t, resp, key)
		if strings.Join(resp.GetPermissions(), ",") != "read,write" {
			t.Fatalf("Permissions = %v, want read,write", resp.GetPermissions())
		}
	})

	t.Run("forbidden includes identity without spending credit", func(t *testing.T) {
		key, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
			req.Permissions = []string{"read"}
			req.RemainingUses = 2
		})
		resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{
			Plaintext:           plaintext,
			RequiredPermissions: []string{"write"},
		})
		if err != nil {
			t.Fatalf("VerifyKey: %v", err)
		}
		if resp.GetValid() || resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_FORBIDDEN {
			t.Fatalf("response = %+v, want FORBIDDEN", resp)
		}
		assertIdentity(t, resp, key)
		got, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: key.GetKeyId()})
		if err != nil {
			t.Fatalf("GetKey: %v", err)
		}
		if got.GetKey().GetRemainingUses() != 2 || got.GetKey().GetLastVerifiedAt() != nil {
			t.Fatalf("forbidden verify mutated key: %+v", got.GetKey())
		}
	})

	t.Run("rate limited returns every limiter decision without spending credit", func(t *testing.T) {
		key, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
			req.RemainingUses = 2
			req.LimitRefs = []*apikeyv1.LimitRef{
				{LimitId: "requests", ScopeKey: "workspace:acme"},
				{LimitId: "tokens", ScopeKey: "user:user_1"},
			}
		})
		r.rl.decisions = []keychainserver.LimitDecision{
			{LimitID: "requests", ScopeKey: "workspace:acme", Allowed: true, Remaining: 9},
			{LimitID: "tokens", ScopeKey: "user:user_1", Allowed: false, Remaining: 0, RetryAfterMs: 250},
		}
		resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext, Cost: 3})
		if err != nil {
			t.Fatalf("VerifyKey: %v", err)
		}
		if resp.GetValid() || resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_RATE_LIMITED {
			t.Fatalf("response = %+v, want RATE_LIMITED", resp)
		}
		assertIdentity(t, resp, key)
		if len(resp.GetLimitDecisions()) != 2 {
			t.Fatalf("LimitDecisions = %d, want 2", len(resp.GetLimitDecisions()))
		}
		if resp.GetLimitDecisions()[1].GetRetryAfterMs() != 250 {
			t.Fatalf("retry_after_ms = %d, want 250", resp.GetLimitDecisions()[1].GetRetryAfterMs())
		}
		got, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: key.GetKeyId()})
		if err != nil {
			t.Fatalf("GetKey: %v", err)
		}
		if got.GetKey().GetRemainingUses() != 2 || got.GetKey().GetLastVerifiedAt() != nil {
			t.Fatalf("rate-limited verify mutated key: %+v", got.GetKey())
		}
	})
}

func TestVerifyKeyRateLimiterErrorMapsUnavailable(t *testing.T) {
	r := newRig(t)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "tpm", ScopeKey: "openai"}}
	})
	r.rl.err = errors.New("rate limiter unavailable")

	_, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	requireStatusCode(t, err, codes.Unavailable)
}

func TestVerifyKeyLimitsWithoutRateLimiterFailsClosed(t *testing.T) {
	r := newRigWithRateLimiter(t, nil)
	_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "tpm", ScopeKey: "openai"}}
	})

	_, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	requireStatusCode(t, err, codes.FailedPrecondition)
}

func TestVerifyKeyRateLimitDenialDoesNotDecrementOrTouch(t *testing.T) {
	r := newRig(t)
	key, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.RemainingUses = 2
		req.LimitRefs = []*apikeyv1.LimitRef{{LimitId: "tpm", ScopeKey: "openai"}}
	})
	r.rl.decisions = []keychainserver.LimitDecision{{LimitID: "tpm", ScopeKey: "openai", Allowed: false}}

	resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext, Cost: 1})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_RATE_LIMITED {
		t.Fatalf("Result = %v, want RATE_LIMITED", resp.GetResult())
	}

	got, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: key.GetKeyId()})
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if got.GetKey().GetRemainingUses() != 2 {
		t.Fatalf("RemainingUses = %d, want 2", got.GetKey().GetRemainingUses())
	}
	if got.GetKey().GetLastVerifiedAt() != nil {
		t.Fatal("LastVerifiedAt should remain nil after rate-limit denial")
	}
}

type depletedOnDecrementStore struct {
	store.Store
}

func (s depletedOnDecrementStore) DecrementRemainingUses(context.Context, string) (int64, error) {
	return 0, store.ErrDepleted
}

func TestVerifyKeyDepletedDuringAtomicDecrementReturnsDepleted(t *testing.T) {
	base := memory.New()
	svc, err := keychainserver.New(context.Background(), keychainserver.Options{
		Store: depletedOnDecrementStore{Store: base},
	})
	if err != nil {
		t.Fatalf("keychainserver.New: %v", err)
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
	key, err := svc.CreateKey(context.Background(), &apikeyv1.CreateKeyRequest{
		ApiId:            api.GetApi().GetApiId(),
		OwnerPrincipalId: "user_1",
		RemainingUses:    1,
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	resp, err := svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: key.GetPlaintext()})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_DEPLETED {
		t.Fatalf("Result = %v, want DEPLETED", resp.GetResult())
	}
	if resp.GetValid() {
		t.Fatal("Valid should be false when atomic decrement reports depletion")
	}
}

func TestVerifyKeyRejectionDoesNotDecrementOrTouch(t *testing.T) {
	r := newRig(t)
	key, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
		req.RemainingUses = 3
	})
	// Revoke so we hit a rejection path that comes after the lookup but
	// before any side effects.
	if _, err := r.svc.RevokeKey(context.Background(), &apikeyv1.RevokeKeyRequest{KeyId: key.GetKeyId()}); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_REVOKED {
		t.Fatalf("Result = %v, want REVOKED", resp.GetResult())
	}

	got, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: key.GetKeyId()})
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if got.GetKey().GetRemainingUses() != 3 {
		t.Fatalf("RemainingUses = %d after rejection, want 3 (unchanged)", got.GetKey().GetRemainingUses())
	}
	if got.GetKey().GetLastVerifiedAt() != nil {
		t.Fatal("LastVerifiedAt should remain nil after a rejected verify")
	}
}

func TestVerifyKeyValidUpdatesLastVerified(t *testing.T) {
	r := newRig(t)
	key, plaintext := r.createKey(t, nil)

	before := time.Now().UTC()
	_, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}

	got, _ := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: key.GetKeyId()})
	last := got.GetKey().GetLastVerifiedAt()
	if last == nil {
		t.Fatal("LastVerifiedAt should be populated after a valid verify")
	}
	if last.AsTime().Before(before.Add(-time.Second)) {
		t.Fatalf("LastVerifiedAt = %v looks stale (before = %v)", last.AsTime(), before)
	}
}

func TestVerifyKeySingleUseConcurrentCallsDoNotBothValidate(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		r := newRig(t)
		_, plaintext := r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
			req.RemainingUses = 1
		})

		start := make(chan struct{})
		results := make(chan apikeyv1.VerifyResult, 2)
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				<-start
				resp, err := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext})
				if err != nil {
					errs <- err
					results <- apikeyv1.VerifyResult_VERIFY_RESULT_UNSPECIFIED
					return
				}
				errs <- nil
				results <- resp.GetResult()
			}()
		}
		close(start)

		valid := 0
		depleted := 0
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("attempt %d VerifyKey: %v", attempt, err)
			}
			switch got := <-results; got {
			case apikeyv1.VerifyResult_VERIFY_RESULT_VALID:
				valid++
			case apikeyv1.VerifyResult_VERIFY_RESULT_DEPLETED:
				depleted++
			default:
				t.Fatalf("attempt %d result = %v, want VALID or DEPLETED", attempt, got)
			}
		}
		if valid != 1 || depleted != 1 {
			t.Fatalf("attempt %d got valid=%d depleted=%d, want one of each", attempt, valid, depleted)
		}
	}
}

// ----- Rotate ---------------------------------------------------------------

func TestRotateKeyInvalidatesOldVerifiesNewKeepsID(t *testing.T) {
	r := newRig(t)
	key, oldPlaintext := r.createKey(t, nil)

	rotated, err := r.svc.RotateKey(context.Background(), &apikeyv1.RotateKeyRequest{KeyId: key.GetKeyId()})
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	newPlaintext := rotated.GetPlaintext()
	if newPlaintext == "" || newPlaintext == oldPlaintext {
		t.Fatal("RotateKey must return a fresh plaintext")
	}
	if rotated.GetKey().GetKeyId() != key.GetKeyId() {
		t.Fatalf("KeyId changed across rotation: %q -> %q", key.GetKeyId(), rotated.GetKey().GetKeyId())
	}

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: oldPlaintext})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_NOT_FOUND {
		t.Fatalf("old plaintext should be NOT_FOUND after rotate, got %v", resp.GetResult())
	}
	resp, _ = r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: newPlaintext})
	if !resp.GetValid() {
		t.Fatalf("new plaintext should be VALID, got %v", resp.GetResult())
	}
}

// ----- ListKeys -------------------------------------------------------------

func TestListKeysFiltersByApiAndOwner(t *testing.T) {
	r := newRig(t)
	r.createKey(t, func(req *apikeyv1.CreateKeyRequest) { req.OwnerPrincipalId = "user_1" })
	r.createKey(t, func(req *apikeyv1.CreateKeyRequest) { req.OwnerPrincipalId = "user_1" })
	r.createKey(t, func(req *apikeyv1.CreateKeyRequest) { req.OwnerPrincipalId = "user_2" })

	resp, err := r.svc.ListKeys(context.Background(), &apikeyv1.ListKeysRequest{
		ApiId:            r.apiID,
		OwnerPrincipalId: "user_1",
		PageSize:         100,
	})
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(resp.GetKeys()) != 2 {
		t.Fatalf("got %d keys, want 2", len(resp.GetKeys()))
	}
	for _, k := range resp.GetKeys() {
		if k.GetOwnerPrincipalId() != "user_1" {
			t.Fatalf("filter leaked: %q", k.GetOwnerPrincipalId())
		}
	}
}

func TestListKeysPaginates(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		r.createKey(t, func(req *apikeyv1.CreateKeyRequest) {
			req.OwnerPrincipalId = "user_page"
		})
	}

	seen := map[string]bool{}
	token := ""
	for page := 0; page < 10; page++ {
		resp, err := r.svc.ListKeys(context.Background(), &apikeyv1.ListKeysRequest{
			ApiId:     r.apiID,
			PageSize:  2,
			PageToken: token,
		})
		if err != nil {
			t.Fatalf("ListKeys page %d: %v", page, err)
		}
		for _, k := range resp.GetKeys() {
			if seen[k.GetKeyId()] {
				t.Fatalf("duplicate key across pages: %s", k.GetKeyId())
			}
			seen[k.GetKeyId()] = true
		}
		if resp.GetNextPageToken() == "" {
			break
		}
		token = resp.GetNextPageToken()
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d keys, want 5", len(seen))
	}
}

// ----- error-mapping spot check ---------------------------------------------

func TestNotFoundMapsToNotFoundStatus(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: "key_missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err code = %v, want NotFound", status.Code(err))
	}
}
