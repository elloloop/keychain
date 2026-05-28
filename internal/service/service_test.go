package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/internal/service"
	"github.com/elloloop/keychain/internal/store"
	"github.com/elloloop/keychain/internal/store/memory"
)

// fakeRateLimiter is a programmable RateLimiter for tests. Reads `decisions`
// to return; if `err` is set, returns the error instead. Records every call
// so tests can assert "Consume was called with these refs and this cost."
type fakeRateLimiter struct {
	decisions []service.LimitDecision
	err       error
	calls     []fakeRLCall
}

type fakeRLCall struct {
	refs []store.LimitRef
	cost int64
	id   string
}

func (f *fakeRateLimiter) Consume(_ context.Context, refs []store.LimitRef, cost int64, id string) ([]service.LimitDecision, error) {
	f.calls = append(f.calls, fakeRLCall{refs: refs, cost: cost, id: id})
	if f.err != nil {
		return nil, f.err
	}
	return f.decisions, nil
}

// allowDecisions builds a slice where every supplied ref is allowed with
// 1000 remaining — the trivial happy-path fixture.
func allowDecisions(refs []store.LimitRef) []service.LimitDecision {
	out := make([]service.LimitDecision, 0, len(refs))
	for _, r := range refs {
		out = append(out, service.LimitDecision{
			LimitID: r.LimitID, ScopeKey: r.ScopeKey,
			Allowed: true, Remaining: 1000,
		})
	}
	return out
}

// ----- test rig --------------------------------------------------------------

type rig struct {
	svc *service.Service
	st  store.Store
	rl  *fakeRateLimiter

	apiID       string
	workspaceID string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st := memory.New()
	rl := &fakeRateLimiter{}
	svc := service.New(st, rl)

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
		rl:          rl,
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
	r.rl.decisions = []service.LimitDecision{{LimitID: "tpm", ScopeKey: "openai", Allowed: false, RetryAfterMs: 5000}}

	resp, _ := r.svc.VerifyKey(context.Background(), &apikeyv1.VerifyKeyRequest{Plaintext: plaintext, Cost: 10})
	if resp.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_RATE_LIMITED {
		t.Fatalf("Result = %v, want RATE_LIMITED", resp.GetResult())
	}
	if len(resp.GetLimitDecisions()) != 1 || resp.GetLimitDecisions()[0].GetRetryAfterMs() != 5000 {
		t.Fatalf("LimitDecisions not forwarded: %+v", resp.GetLimitDecisions())
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

// ----- error-mapping spot check ---------------------------------------------

func TestNotFoundMapsToNotFoundStatus(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.GetKey(context.Background(), &apikeyv1.GetKeyRequest{KeyId: "key_missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err code = %v, want NotFound", status.Code(err))
	}
}
