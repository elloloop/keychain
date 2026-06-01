// Command keychain runs the API-key issuance and verification service.
//
// Subcommands:
//
//	serve         start the gRPC server (default)
//	version       print build version and commit
//	print-config  print resolved KEYCHAIN_* configuration as JSON
//	help          this message
//
// cmd/keychain wires config loading, store selection, gRPC registration, and
// signal-driven shutdown around keychainserver.New. A host program that embeds
// keychain in its own gRPC server drives that same entry point directly.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/internal/config"
	"github.com/elloloop/keychain/keychainserver"
	"github.com/elloloop/keychain/keychainserver/store"
	"github.com/elloloop/keychain/keychainserver/store/memory"
	"github.com/elloloop/keychain/keychainserver/store/postgres"
)

var (
	version = "dev"
	commit  = "unknown"
	osExit  = os.Exit
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		osExit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "serve":
		return serve()
	case "version":
		fmt.Printf("keychain %s (%s)\n", version, commit)
		return nil
	case "print-config":
		return printConfig()
	case "help", "-h", "--help":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printHelp() {
	fmt.Println(`keychain commands:
  serve          Start the gRPC service (default)
  version        Print build version and commit
  print-config   Print resolved KEYCHAIN_* configuration as JSON
  help           Show this help`)
}

func printConfig() error {
	cfg := config.Load()
	fmt.Println(string(cfg.Redacted().JSON()))
	return cfg.Validate()
}

func serve() error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serveWithContext(ctx, cfg, logger)
}

func serveWithContext(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	st, closeStore, err := openStore(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore()

	kc, err := keychainserver.New(ctx, keychainserver.Options{
		Store:       st,
		RateLimiter: newFailClosedRateLimiter(cfg.RateLimiterAddr, logger),
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("keychainserver.New: %w", err)
	}

	server := grpc.NewServer()
	apikeyv1.RegisterApiKeyServiceServer(server, kc)
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthgrpc.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("apikey.v1.ApiKeyService", healthgrpc.HealthCheckResponse_SERVING)
	healthgrpc.RegisterHealthServer(server, healthSrv)
	reflection.Register(server)

	lis, err := net.Listen("tcp", cfg.GRPCBindAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCBindAddr, err)
	}
	logger.Info("gRPC listening", "addr", cfg.GRPCBindAddr)

	metricsSrv := startMetricsServer(ctx, cfg.MetricsBindAddr, logger)

	return serveUntilDone(ctx, server, lis, metricsSrv, logger)
}

func serveUntilDone(ctx context.Context, server *grpc.Server, lis net.Listener, metricsSrv *http.Server, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(lis) }()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			shutdownMetrics(metricsSrv) //nolint:contextcheck // shutdown uses a fresh timeout context because ctx may already be canceled
			return fmt.Errorf("gRPC serve: %w", err)
		}
	}

	gracefulStop(server, metricsSrv, logger) //nolint:contextcheck // shutdown uses fresh contexts after ctx is canceled
	return nil
}

// ---------------------------------------------------------------------------
// store wiring
// ---------------------------------------------------------------------------

func openStore(ctx context.Context, cfg config.Config, logger *slog.Logger) (store.Store, func(), error) {
	switch cfg.Store {
	case config.StoreMemory:
		logger.Warn("using in-memory store; keys do not survive restarts")
		return memory.New(), func() {}, nil
	case config.StorePostgres:
		s, err := postgres.New(ctx, cfg.PostgresURL)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	default:
		return nil, nil, fmt.Errorf("unsupported store %q", cfg.Store)
	}
}

// ---------------------------------------------------------------------------
// rate-limiter wiring
// ---------------------------------------------------------------------------

// failClosedRateLimiter is the v0.1.0 placeholder. It does not call any
// real rate-limiter; if a key carries limit_refs, verify is denied with
// a clear "v0.2.0 catalog feature required" message. Keys without
// limit_refs are unaffected.
type failClosedRateLimiter struct {
	addr   string
	logger *slog.Logger
}

func newFailClosedRateLimiter(addr string, logger *slog.Logger) *failClosedRateLimiter {
	if addr != "" {
		logger.Warn("KEYCHAIN_RATELIMITER_ADDR is set but limit-catalog wiring is a v0.2.0 feature; keys with limit_refs will fail closed", "addr", addr)
	}
	return &failClosedRateLimiter{addr: addr, logger: logger}
}

func (r *failClosedRateLimiter) Consume(_ context.Context, refs []store.LimitRef, cost int64, _ string) ([]keychainserver.LimitDecision, error) {
	out := make([]keychainserver.LimitDecision, 0, len(refs))
	for _, ref := range refs {
		out = append(out, keychainserver.LimitDecision{
			LimitID:   ref.LimitID,
			ScopeKey:  ref.ScopeKey,
			Allowed:   false,
			Remaining: 0,
		})
	}
	r.logger.Debug("fail-closed rate-limiter denying refs", "count", len(refs), "cost", cost)
	return out, nil
}

// ---------------------------------------------------------------------------
// metrics + lifecycle
// ---------------------------------------------------------------------------

func startMetricsServer(ctx context.Context, addr string, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "error", err)
		}
	}()
	go func() { //nolint:gosec // shutdown runs after ctx is canceled and must use a fresh context
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:contextcheck // shutdown intentionally uses a fresh context after ctx is canceled
	}()
	return srv
}

func shutdownMetrics(srv *http.Server) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

type grpcStopper interface {
	GracefulStop()
	Stop()
}

func gracefulStop(grpcSrv *grpc.Server, metricsSrv *http.Server, logger *slog.Logger) {
	gracefulStopWithTimeout(grpcSrv, metricsSrv, logger, 15*time.Second)
}

func gracefulStopWithTimeout(grpcSrv grpcStopper, metricsSrv *http.Server, logger *slog.Logger, timeout time.Duration) {
	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(timeout):
		logger.Warn("gRPC graceful stop timed out; forcing stop")
		grpcSrv.Stop()
	}
	shutdownMetrics(metricsSrv)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
