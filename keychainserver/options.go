package keychainserver

import (
	"context"
	"log/slog"

	"github.com/elloloop/keychain/keychainserver/store"
)

// Options is the programmatic configuration for New. Store is required;
// RateLimiter and Logger are optional.
type Options struct {
	// Store is the persistence backend. Required. Pass
	// keychainserver/store/postgres.New(ctx, dsn) for production or
	// keychainserver/store/memory.New() for tests and local development
	// (see that package's doc — not durable, not for production).
	Store store.Store

	// RateLimiter evaluates a key's LimitRefs at VerifyKey time. Optional:
	// when nil, VerifyKey for a key carrying LimitRefs fails closed with
	// FailedPrecondition. Hosts that issue keys without LimitRefs can
	// leave this nil.
	RateLimiter RateLimiter

	// Logger receives the server's structured logs. Optional; nil disables
	// logging.
	Logger *slog.Logger
}

// LimitDecision is the service-layer mirror of the wire LimitDecision; the
// RateLimiter implementation returns these so it does not need to import
// the proto package.
type LimitDecision struct {
	LimitID      string
	ScopeKey     string
	Allowed      bool
	Remaining    int64
	RetryAfterMs int64
}

// RateLimiter is the subset of the rate-limiter gRPC client keychain
// needs. Implementations evaluate every supplied LimitRef and return a
// decision per ref; aggregate failures so the verify handler can report
// all denied limits, not just the first.
type RateLimiter interface {
	Consume(ctx context.Context, refs []store.LimitRef, cost int64, requestID string) ([]LimitDecision, error)
}
