// Package service implements the apikey.v1 gRPC contract on top of a
// pluggable store.Store and a rate-limiter client. The package contains no
// transport-specific code; cmd/keychain wires it into a real gRPC server.
package service

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/keychainserver/store"
	"github.com/elloloop/keychain/pkg/keymat"
)

// LimitDecision is the service-layer mirror of the wire LimitDecision; the
// rate-limiter client returns these so it does not need to import the proto
// package.
type LimitDecision struct {
	LimitID      string
	ScopeKey     string
	Allowed      bool
	Remaining    int64
	RetryAfterMs int64
}

// RateLimiter is the subset of the rate-limiter gRPC client keychain needs.
// Implementations evaluate every supplied LimitRef and return a decision
// per ref; aggregate failures so the verify handler can report all denied
// limits, not just the first.
type RateLimiter interface {
	Consume(ctx context.Context, refs []store.LimitRef, cost int64, requestID string) ([]LimitDecision, error)
}

// Service implements apikeyv1.ApiKeyServiceServer.
type Service struct {
	apikeyv1.UnimplementedApiKeyServiceServer

	store store.Store
	rl    RateLimiter
	now   func() time.Time
}

// New constructs a Service. rl may be nil; if so, any VerifyKey call with
// a key that has LimitRefs and skip_ratelimit=false will fail closed.
func New(s store.Store, rl RateLimiter) *Service {
	return &Service{
		store: s,
		rl:    rl,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// ---------------------------------------------------------------------------
// Workspace RPCs
// ---------------------------------------------------------------------------

func (s *Service) CreateWorkspace(ctx context.Context, req *apikeyv1.CreateWorkspaceRequest) (*apikeyv1.CreateWorkspaceResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetOwnerPrincipalId() == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_principal_id is required")
	}
	w, err := s.store.CreateWorkspace(ctx, store.Workspace{
		Name:             req.GetName(),
		OwnerPrincipalID: req.GetOwnerPrincipalId(),
		Metadata:         req.GetMetadata(),
	})
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.CreateWorkspaceResponse{Workspace: workspaceToProto(w)}, nil
}

func (s *Service) GetWorkspace(ctx context.Context, req *apikeyv1.GetWorkspaceRequest) (*apikeyv1.GetWorkspaceResponse, error) {
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	w, err := s.store.GetWorkspace(ctx, req.GetWorkspaceId())
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.GetWorkspaceResponse{Workspace: workspaceToProto(w)}, nil
}

// ---------------------------------------------------------------------------
// API RPCs
// ---------------------------------------------------------------------------

func (s *Service) CreateApi(ctx context.Context, req *apikeyv1.CreateApiRequest) (*apikeyv1.CreateApiResponse, error) { //nolint:revive // method name must match the proto-generated ApiKeyServiceServer interface
	if req.GetWorkspaceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	a, err := s.store.CreateAPI(ctx, store.API{
		WorkspaceID: req.GetWorkspaceId(),
		Name:        req.GetName(),
		KeyPrefix:   req.GetKeyPrefix(),
		Metadata:    req.GetMetadata(),
	})
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.CreateApiResponse{Api: apiToProto(a)}, nil
}

func (s *Service) GetApi(ctx context.Context, req *apikeyv1.GetApiRequest) (*apikeyv1.GetApiResponse, error) { //nolint:revive // method name must match the proto-generated ApiKeyServiceServer interface
	if req.GetApiId() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_id is required")
	}
	a, err := s.store.GetAPI(ctx, req.GetApiId())
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.GetApiResponse{Api: apiToProto(a)}, nil
}

// ---------------------------------------------------------------------------
// Key RPCs
// ---------------------------------------------------------------------------

func (s *Service) CreateKey(ctx context.Context, req *apikeyv1.CreateKeyRequest) (*apikeyv1.CreateKeyResponse, error) {
	if req.GetApiId() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_id is required")
	}
	if req.GetOwnerPrincipalId() == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_principal_id is required")
	}

	api, err := s.store.GetAPI(ctx, req.GetApiId())
	if err != nil {
		return nil, mapStoreErr(err)
	}

	plaintext, hash, err := keymat.New(api.KeyPrefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate key material: %v", err)
	}

	// proto3 cannot distinguish "field omitted" from "field set to 0" for
	// scalar fields, so we treat both as "unlimited". Callers that actually
	// want a credit-style key pass a positive integer; the degenerate "0
	// remaining at issuance time" key has no legitimate use case.
	remaining := req.GetRemainingUses()
	if remaining <= 0 {
		remaining = -1
	}

	k := store.Key{
		APIID:            api.APIID,
		WorkspaceID:      api.WorkspaceID,
		OwnerPrincipalID: req.GetOwnerPrincipalId(),
		Name:             req.GetName(),
		KeyHash:          hash[:],
		Permissions:      req.GetPermissions(),
		LimitRefs:        limitRefsFromProto(req.GetLimitRefs()),
		ExpiresAt:        timeFromProto(req.GetExpiresAt()),
		RemainingUses:    remaining,
		Enabled:          true,
		Metadata:         req.GetMetadata(),
	}
	created, err := s.store.CreateKey(ctx, k)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.CreateKeyResponse{
		Key:       keyToProto(created),
		Plaintext: plaintext,
	}, nil
}

func (s *Service) GetKey(ctx context.Context, req *apikeyv1.GetKeyRequest) (*apikeyv1.GetKeyResponse, error) {
	if req.GetKeyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "key_id is required")
	}
	k, err := s.store.GetKeyByID(ctx, req.GetKeyId())
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.GetKeyResponse{Key: keyToProto(k)}, nil
}

func (s *Service) RevokeKey(ctx context.Context, req *apikeyv1.RevokeKeyRequest) (*apikeyv1.RevokeKeyResponse, error) {
	if req.GetKeyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "key_id is required")
	}
	if err := s.store.RevokeKey(ctx, req.GetKeyId()); err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.RevokeKeyResponse{}, nil
}

func (s *Service) RotateKey(ctx context.Context, req *apikeyv1.RotateKeyRequest) (*apikeyv1.RotateKeyResponse, error) {
	if req.GetKeyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "key_id is required")
	}
	existing, err := s.store.GetKeyByID(ctx, req.GetKeyId())
	if err != nil {
		return nil, mapStoreErr(err)
	}
	api, err := s.store.GetAPI(ctx, existing.APIID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	plaintext, hash, err := keymat.New(api.KeyPrefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate key material: %v", err)
	}
	rotated, err := s.store.RotateKey(ctx, req.GetKeyId(), hash[:])
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return &apikeyv1.RotateKeyResponse{
		Key:       keyToProto(rotated),
		Plaintext: plaintext,
	}, nil
}

func (s *Service) ListKeys(ctx context.Context, req *apikeyv1.ListKeysRequest) (*apikeyv1.ListKeysResponse, error) {
	if req.GetApiId() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_id is required")
	}
	res, err := s.store.ListKeys(ctx, store.ListKeysOpts{
		APIID:            req.GetApiId(),
		OwnerPrincipalID: req.GetOwnerPrincipalId(),
		PageSize:         req.GetPageSize(),
		PageToken:        req.GetPageToken(),
	})
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := make([]*apikeyv1.ApiKey, 0, len(res.Keys))
	for _, k := range res.Keys {
		out = append(out, keyToProto(k))
	}
	return &apikeyv1.ListKeysResponse{
		Keys:          out,
		NextPageToken: res.NextPageToken,
	}, nil
}

// VerifyKey is the hot path. Order of checks is cheapest-first: hash
// lookup, enabled/expiry/credit, permission, rate-limit. Side effects
// (touch + decrement) happen only on a VALID decision.
func (s *Service) VerifyKey(ctx context.Context, req *apikeyv1.VerifyKeyRequest) (*apikeyv1.VerifyKeyResponse, error) {
	if err := keymat.Validate(req.GetPlaintext(), ""); err != nil {
		return notFound(), nil //nolint:nilerr // a malformed plaintext is a verify-time NOT_FOUND, not an RPC-level error
	}
	hash := keymat.Hash(req.GetPlaintext())

	k, err := s.store.GetKeyByHash(ctx, hash[:])
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound(), nil //nolint:nilerr // verify reports not-found as response data with no RPC error, matching the rate-limiter health-status contract
		}
		return nil, status.Errorf(codes.Internal, "lookup key: %v", err)
	}

	resp := &apikeyv1.VerifyKeyResponse{
		KeyId:            k.KeyID,
		ApiId:            k.APIID,
		WorkspaceId:      k.WorkspaceID,
		OwnerPrincipalId: k.OwnerPrincipalID,
		Permissions:      k.Permissions,
	}

	if !k.Enabled {
		resp.Result = apikeyv1.VerifyResult_VERIFY_RESULT_REVOKED
		return resp, nil
	}
	if k.ExpiresAt != nil && !s.now().Before(*k.ExpiresAt) {
		resp.Result = apikeyv1.VerifyResult_VERIFY_RESULT_EXPIRED
		return resp, nil
	}
	if k.RemainingUses == 0 {
		resp.Result = apikeyv1.VerifyResult_VERIFY_RESULT_DEPLETED
		return resp, nil
	}
	if !hasAllPermissions(k.Permissions, req.GetRequiredPermissions()) {
		resp.Result = apikeyv1.VerifyResult_VERIFY_RESULT_FORBIDDEN
		return resp, nil
	}

	if !req.GetSkipRatelimit() && len(k.LimitRefs) > 0 {
		if s.rl == nil {
			return nil, status.Error(codes.FailedPrecondition, "rate-limiter client not configured but key has limit_refs")
		}
		decisions, err := s.rl.Consume(ctx, k.LimitRefs, req.GetCost(), req.GetRequestId())
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "rate-limiter consume: %v", err)
		}
		resp.LimitDecisions = limitDecisionsToProto(decisions)
		for _, d := range decisions {
			if !d.Allowed {
				resp.Result = apikeyv1.VerifyResult_VERIFY_RESULT_RATE_LIMITED
				return resp, nil
			}
		}
	}

	if k.RemainingUses > 0 {
		if _, err := s.store.DecrementRemainingUses(ctx, k.KeyID); err != nil {
			return nil, status.Errorf(codes.Internal, "decrement remaining uses: %v", err)
		}
	}
	// TouchLastVerified is observational. cmd/keychain owns logging; the
	// verify decision has already been made and must not be reversed by a
	// failure to record the timestamp.
	_ = s.store.TouchLastVerified(ctx, k.KeyID, s.now())

	resp.Valid = true
	resp.Result = apikeyv1.VerifyResult_VERIFY_RESULT_VALID
	return resp, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func notFound() *apikeyv1.VerifyKeyResponse {
	return &apikeyv1.VerifyKeyResponse{Result: apikeyv1.VerifyResult_VERIFY_RESULT_NOT_FOUND}
}

func hasAllPermissions(have, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, p := range have {
		set[p] = struct{}{}
	}
	for _, p := range required {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}

func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// ---------------------------------------------------------------------------
// Converters
// ---------------------------------------------------------------------------

func workspaceToProto(w store.Workspace) *apikeyv1.Workspace {
	return &apikeyv1.Workspace{
		WorkspaceId:      w.WorkspaceID,
		Name:             w.Name,
		OwnerPrincipalId: w.OwnerPrincipalID,
		CreatedAt:        timestamppb.New(w.CreatedAt),
		UpdatedAt:        timestamppb.New(w.UpdatedAt),
		Metadata:         w.Metadata,
	}
}

func apiToProto(a store.API) *apikeyv1.Api {
	return &apikeyv1.Api{
		ApiId:       a.APIID,
		WorkspaceId: a.WorkspaceID,
		Name:        a.Name,
		KeyPrefix:   a.KeyPrefix,
		CreatedAt:   timestamppb.New(a.CreatedAt),
		UpdatedAt:   timestamppb.New(a.UpdatedAt),
		Metadata:    a.Metadata,
	}
}

func keyToProto(k store.Key) *apikeyv1.ApiKey {
	out := &apikeyv1.ApiKey{
		KeyId:            k.KeyID,
		ApiId:            k.APIID,
		WorkspaceId:      k.WorkspaceID,
		OwnerPrincipalId: k.OwnerPrincipalID,
		Name:             k.Name,
		Permissions:      k.Permissions,
		LimitRefs:        limitRefsToProto(k.LimitRefs),
		RemainingUses:    k.RemainingUses,
		Enabled:          k.Enabled,
		CreatedAt:        timestamppb.New(k.CreatedAt),
		UpdatedAt:        timestamppb.New(k.UpdatedAt),
		Metadata:         k.Metadata,
	}
	if k.ExpiresAt != nil {
		out.ExpiresAt = timestamppb.New(*k.ExpiresAt)
	}
	if k.LastVerifiedAt != nil {
		out.LastVerifiedAt = timestamppb.New(*k.LastVerifiedAt)
	}
	return out
}

func limitRefsToProto(in []store.LimitRef) []*apikeyv1.LimitRef {
	out := make([]*apikeyv1.LimitRef, 0, len(in))
	for _, r := range in {
		out = append(out, &apikeyv1.LimitRef{LimitId: r.LimitID, ScopeKey: r.ScopeKey})
	}
	return out
}

func limitRefsFromProto(in []*apikeyv1.LimitRef) []store.LimitRef {
	out := make([]store.LimitRef, 0, len(in))
	for _, r := range in {
		out = append(out, store.LimitRef{LimitID: r.GetLimitId(), ScopeKey: r.GetScopeKey()})
	}
	return out
}

func limitDecisionsToProto(in []LimitDecision) []*apikeyv1.LimitDecision {
	out := make([]*apikeyv1.LimitDecision, 0, len(in))
	for _, d := range in {
		out = append(out, &apikeyv1.LimitDecision{
			LimitId:      d.LimitID,
			ScopeKey:     d.ScopeKey,
			Allowed:      d.Allowed,
			Remaining:    d.Remaining,
			RetryAfterMs: d.RetryAfterMs,
		})
	}
	return out
}

func timeFromProto(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime()
	return &v
}
