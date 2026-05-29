// Command embedded shows how a host program mounts keychain into its own
// gRPC server instead of running the dedicated container.
//
// It opens a Postgres-backed store, constructs a keychainserver.Server,
// registers it on a host-owned *grpc.Server alongside the host's own
// services, and serves. A real host typically supplies its own
// RateLimiter implementation too; this example leaves it nil so keys
// without LimitRefs verify normally and keys with LimitRefs fail closed.
//
// Run it with:
//
//	docker run -d --rm -p 5432:5432 \
//	    -e POSTGRES_USER=keychain -e POSTGRES_PASSWORD=keychain \
//	    -e POSTGRES_DB=keychain postgres:16.13-alpine3.23
//	go run ./examples/embedded
//
// The example builds in CI (`go build ./examples/embedded/...`) but is
// not run by default because it needs a real Postgres.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/keychainserver"
	kcpg "github.com/elloloop/keychain/keychainserver/store/postgres"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("KEYCHAIN_POSTGRES_URL")
	if dsn == "" {
		// Local-dev defaults that match the docker run line in the
		// package doc. A real host always supplies its own DSN.
		dsn = "postgres://keychain:keychain@localhost:5432/keychain?sslmode=disable" //nolint:gosec // G101: example default for local dev only
	}

	// postgres.New pings the pool and runs embedded migrations
	// synchronously, returning an error on any failure. The Store is
	// safe for concurrent use and the host owns the lifetime via Close.
	store, err := kcpg.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	kc, err := keychainserver.New(ctx, keychainserver.Options{
		Store:  store,
		Logger: logger,
		// RateLimiter: optional; supply your own client to evaluate
		// keys with LimitRefs.
	})
	if err != nil {
		return fmt.Errorf("keychainserver.New: %w", err)
	}

	g := grpc.NewServer()
	apikeyv1.RegisterApiKeyServiceServer(g, kc)
	// The host registers its own services alongside keychain here:
	//   myservicepb.RegisterMyServiceServer(g, myImpl)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthgrpc.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("apikey.v1.ApiKeyService", healthgrpc.HealthCheckResponse_SERVING)
	healthgrpc.RegisterHealthServer(g, healthSrv)
	reflection.Register(g)

	lis, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("grpc_listening", "addr", lis.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		if err := g.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("grpc serve: %w", err)
		}
	}

	stopped := make(chan struct{})
	go func() {
		g.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		logger.Warn("graceful stop timed out; forcing")
		g.Stop()
	}
	return nil
}
