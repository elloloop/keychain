package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

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
		if !strings.Contains(out, "serve") {
			t.Fatalf("help (%q) missing 'serve' subcommand: %q", arg, out)
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
