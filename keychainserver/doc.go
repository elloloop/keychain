// Package keychainserver is keychain's embeddable public API. It lets a
// Go program import keychain and mount the API-key service on its own
// gRPC server instead of running the dedicated container.
//
//	store, err := postgres.New(ctx, dsn)              // pings + migrates
//	if err != nil { return err }
//	defer store.Close()
//
//	kc, err := keychainserver.New(ctx, keychainserver.Options{
//	    Store:       store,
//	    RateLimiter: optionalRateLimiterClient,
//	    Logger:      logger,
//	})
//	if err != nil { return err }
//
//	g := grpc.NewServer()
//	apikeyv1.RegisterApiKeyServiceServer(g, kc)
//	g.Serve(lis)
//
// The Server implements apikeyv1.ApiKeyServiceServer directly — there is
// no Start/Shutdown lifecycle, the host owns the listener and graceful
// stop. cmd/keychain is a thin shim over this package; the container
// behaves identically to embedding keychain in another process.
//
// Stores: any keychainserver/store.Store implementation works.
// keychainserver/store/postgres is the production driver and runs
// embedded migrations synchronously in its New. keychainserver/store/memory
// is the in-process driver — tests and local development only, not durable
// across restarts. External drivers (SQLite, MySQL, etc.) implement the
// Store interface and verify behaviour via the conformance suite in
// keychainserver/store/conformance.
//
// Rate-limiter integration is optional. When Options.RateLimiter is non-nil,
// VerifyKey calls Consume for every LimitRef on the key and aggregates the
// decisions into the response. When nil, a key carrying LimitRefs that is
// verified without SkipRatelimit fails closed with FailedPrecondition;
// keys without LimitRefs are unaffected.
package keychainserver
