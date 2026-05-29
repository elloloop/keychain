package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/elloloop/keychain/internal/config"
	"github.com/elloloop/keychain/keychainserver/store"
)

type exitPanic int

type blockingStopper struct {
	gracefulStarted chan struct{}
	stopCalled      chan struct{}
}

func (s *blockingStopper) GracefulStop() {
	close(s.gracefulStarted)
	select {}
}

func (s *blockingStopper) Stop() {
	close(s.stopCalled)
}

type immediateStopper struct{}

func (immediateStopper) GracefulStop() {}
func (immediateStopper) Stop()         { panic("Stop should not be called after successful GracefulStop") }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMainSuccessPath(t *testing.T) {
	origArgs := os.Args
	os.Args = []string{"keychain", "version"}
	t.Cleanup(func() { os.Args = origArgs })

	main()
}

func TestMainErrorPathExitsNonZero(t *testing.T) {
	origArgs := os.Args
	origExit := osExit
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Args = []string{"keychain", "definitely-unknown"}
	os.Stderr = w
	osExit = func(code int) { panic(exitPanic(code)) }
	t.Cleanup(func() {
		os.Args = origArgs
		os.Stderr = origStderr
		osExit = origExit
		_ = r.Close()
	})

	var got exitPanic
	func() {
		defer func() {
			if v := recover(); v != nil {
				var ok bool
				got, ok = v.(exitPanic)
				if !ok {
					panic(v)
				}
			}
		}()
		main()
	}()
	_ = w.Close()
	if got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stderr: %v", err)
	}
	if !strings.Contains(buf.String(), "definitely-unknown") {
		t.Fatalf("stderr = %q, want unknown command", buf.String())
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever was written. Used by subcommand tests that print to stdout.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	errCh := make(chan error, 1)
	go func() { errCh <- fn() }()
	if err := <-errCh; err != nil {
		_ = w.Close()
		t.Fatalf("fn: %v", err)
	}
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String()
}

func TestRunVersion(t *testing.T) {
	out := captureStdout(t, func() error { return run([]string{"version"}) })
	if !strings.Contains(out, "keychain") {
		t.Fatalf("version output missing service name: %q", out)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		out := captureStdout(t, func() error { return run([]string{arg}) })
		for _, command := range []string{"serve", "version", "print-config", "help"} {
			if !strings.Contains(out, command) {
				t.Fatalf("help (%q) missing %q subcommand: %q", arg, command, out)
			}
		}
	}
}

func TestRunUnknownCommandReturnsError(t *testing.T) {
	err := run([]string{"sing"})
	if err == nil {
		t.Fatal("unknown command must return an error")
	}
	if !strings.Contains(err.Error(), "sing") {
		t.Fatalf("error should name the offending command: %v", err)
	}
}

func TestRunPrintConfigEmitsJSON(t *testing.T) {
	// Default env -> memory store, valid config -> exits clean.
	for _, k := range []string{
		"KEYCHAIN_STORE", "KEYCHAIN_POSTGRES_URL",
		"KEYCHAIN_RATELIMITER_ADDR", "KEYCHAIN_LOG_LEVEL",
	} {
		t.Setenv(k, "")
	}
	out := captureStdout(t, func() error { return run([]string{"print-config"}) })
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("print-config did not emit JSON: %q", out)
	}
	if !strings.Contains(out, "grpc_bind_addr") {
		t.Fatalf("print-config output missing expected field: %q", out)
	}
}

func TestRunPrintConfigRedactsPostgresPassword(t *testing.T) {
	t.Setenv("KEYCHAIN_STORE", "memory")
	t.Setenv("KEYCHAIN_POSTGRES_URL", "postgres://user:supersecret@db:5432/keychain?sslmode=disable")
	out := captureStdout(t, func() error { return run([]string{"print-config"}) })
	if strings.Contains(out, "supersecret") {
		t.Fatalf("print-config leaked password: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("print-config missing redacted marker: %q", out)
	}
}

func TestRunDefaultsToServe(t *testing.T) {
	// We don't actually want to start a server in unit tests. We test the
	// dispatch by constructing a bad postgres URL so serve() returns a
	// Validate error fast, without binding to a port.
	t.Setenv("KEYCHAIN_STORE", "postgres")
	t.Setenv("KEYCHAIN_POSTGRES_URL", "")
	err := run(nil) // no args -> defaults to serve
	if err == nil {
		t.Fatal("serve with invalid config must error")
	}
	if !strings.Contains(err.Error(), "KEYCHAIN_POSTGRES_URL") {
		t.Fatalf("error should reference the missing field: %v", err)
	}
}

func TestServeStopsOnInterrupt(t *testing.T) {
	t.Setenv("KEYCHAIN_GRPC_BIND_ADDR", "127.0.0.1:0")
	t.Setenv("KEYCHAIN_METRICS_BIND_ADDR", "127.0.0.1:0")
	t.Setenv("KEYCHAIN_STORE", "memory")
	t.Setenv("KEYCHAIN_LOG_LEVEL", "error")

	errCh := make(chan error, 1)
	go func() { errCh <- serve() }()
	time.Sleep(50 * time.Millisecond)
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after interrupt")
	}
}

func TestServeWithContextShutsDownWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := serveWithContext(ctx, config.Config{
		GRPCBindAddr:    "127.0.0.1:0",
		MetricsBindAddr: "127.0.0.1:0",
		Store:           config.StoreMemory,
		LogLevel:        "error",
	}, discardLogger())
	if err != nil {
		t.Fatalf("serveWithContext: %v", err)
	}
}

func TestServeWithContextReturnsListenError(t *testing.T) {
	err := serveWithContext(context.Background(), config.Config{
		GRPCBindAddr:    "not a valid address",
		MetricsBindAddr: "127.0.0.1:0",
		Store:           config.StoreMemory,
		LogLevel:        "error",
	}, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("serveWithContext err = %v, want listen error", err)
	}
}

func TestServeWithContextReturnsOpenStoreError(t *testing.T) {
	err := serveWithContext(context.Background(), config.Config{
		GRPCBindAddr:    "127.0.0.1:0",
		MetricsBindAddr: "127.0.0.1:0",
		Store:           config.StorePostgres,
		PostgresURL:     "postgres://[::1",
		LogLevel:        "error",
	}, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("serveWithContext err = %v, want open store error", err)
	}
}

func TestOpenStoreMemoryCreatesUsableStore(t *testing.T) {
	st, closeStore, err := openStore(context.Background(), config.Config{Store: config.StoreMemory}, discardLogger())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(closeStore)

	w, err := st.CreateWorkspace(context.Background(), store.Workspace{
		Name:             "acme",
		OwnerPrincipalID: "owner",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if w.WorkspaceID == "" {
		t.Fatal("WorkspaceID should be assigned")
	}
}

func TestOpenStoreRejectsUnsupportedStore(t *testing.T) {
	_, _, err := openStore(context.Background(), config.Config{Store: "redis"}, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "unsupported store") {
		t.Fatalf("openStore err = %v, want unsupported store", err)
	}
}

func TestOpenStorePostgresCreatesUsableStoreWhenConfigured(t *testing.T) {
	dsn := os.Getenv("KEYCHAIN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("KEYCHAIN_TEST_POSTGRES_URL not set")
	}
	st, closeStore, err := openStore(context.Background(), config.Config{
		Store:       config.StorePostgres,
		PostgresURL: dsn,
	}, discardLogger())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(closeStore)
	if _, err := st.GetWorkspace(context.Background(), "ws_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetWorkspace err = %v, want ErrNotFound", err)
	}
}

func TestOpenStorePostgresReturnsOpenError(t *testing.T) {
	_, _, err := openStore(context.Background(), config.Config{
		Store:       config.StorePostgres,
		PostgresURL: "postgres://keychain:keychain@127.0.0.1:1/keychain?sslmode=disable",
	}, discardLogger())
	if err == nil {
		t.Fatal("openStore accepted an unreachable Postgres URL")
	}
}

func TestFailClosedRateLimiterDeniesEveryRef(t *testing.T) {
	rl := newFailClosedRateLimiter("", discardLogger())
	refs := []store.LimitRef{
		{LimitID: "tokens", ScopeKey: "user:user_1"},
		{LimitID: "requests", ScopeKey: "workspace:acme"},
	}
	decisions, err := rl.Consume(context.Background(), refs, 10, "req_1")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(decisions) != len(refs) {
		t.Fatalf("decisions = %d, want %d", len(decisions), len(refs))
	}
	for i, decision := range decisions {
		if decision.Allowed {
			t.Fatalf("decision %d allowed, want denied", i)
		}
		if decision.LimitID != refs[i].LimitID || decision.ScopeKey != refs[i].ScopeKey {
			t.Fatalf("decision %d = %+v, want ref %+v", i, decision, refs[i])
		}
	}
}

func TestNewFailClosedRateLimiterAcceptsConfiguredAddress(t *testing.T) {
	rl := newFailClosedRateLimiter("ratelimiter:8081", discardLogger())
	if rl.addr != "ratelimiter:8081" {
		t.Fatalf("addr = %q, want ratelimiter:8081", rl.addr)
	}
}

func TestMetricsHandlerHealthz(t *testing.T) {
	srv := startMetricsServer(context.Background(), "127.0.0.1:0", discardLogger())
	t.Cleanup(func() { _ = srv.Close() })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestStartMetricsServerLogsListenError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := startMetricsServer(ctx, "not a valid address", discardLogger())
	t.Cleanup(func() { _ = srv.Close() })
	time.Sleep(10 * time.Millisecond)
}

func TestGracefulStopWithTimeoutStopsBlockedServer(t *testing.T) {
	stopper := &blockingStopper{
		gracefulStarted: make(chan struct{}),
		stopCalled:      make(chan struct{}),
	}
	metricsSrv := &http.Server{ReadHeaderTimeout: time.Second}
	gracefulStopWithTimeout(stopper, metricsSrv, discardLogger(), time.Millisecond)
	select {
	case <-stopper.gracefulStarted:
	default:
		t.Fatal("GracefulStop was not called")
	}
	select {
	case <-stopper.stopCalled:
	case <-time.After(time.Second):
		t.Fatal("Stop was not called after timeout")
	}
}

func TestGracefulStopWithTimeoutDoesNotForceStoppedServer(t *testing.T) {
	metricsSrv := &http.Server{ReadHeaderTimeout: time.Second}
	gracefulStopWithTimeout(immediateStopper{}, metricsSrv, discardLogger(), time.Second)
}

func TestNewLoggerLevels(t *testing.T) {
	for _, level := range []string{"debug", "warn", "error", "info", "unknown"} {
		t.Run(level, func(t *testing.T) {
			logger := newLogger(level)
			if logger == nil {
				t.Fatal("newLogger returned nil")
			}
		})
	}
}
