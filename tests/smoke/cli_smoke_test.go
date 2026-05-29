//go:build smoke

// Package smoke runs the built keychain binary against its non-serving
// subcommands. Building and invoking the actual binary links every package
// (including the generated protobuf descriptors and the embedded SQL
// migrations) and catches init-time or wiring regressions that unit tests
// would miss.
package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var (
	repoRoot string
	binPath  string
)

func TestMain(m *testing.M) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("smoke: cannot determine caller path")
	}
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	dir, err := os.MkdirTemp("", "keychain-smoke")
	if err != nil {
		panic("smoke: mkdir temp: " + err.Error())
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "keychain")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/keychain")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		panic("smoke: build keychain: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

func runKeychain(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = repoRoot
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestVersionSubcommand(t *testing.T) {
	out, err := runKeychain(t, nil, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	if !strings.Contains(out, "keychain") {
		t.Fatalf("version output missing service name: %q", out)
	}
}

func TestHelpSubcommand(t *testing.T) {
	out, err := runKeychain(t, nil, "help")
	if err != nil {
		t.Fatalf("help: %v\n%s", err, out)
	}
	if !strings.Contains(out, "serve") {
		t.Fatalf("help output missing 'serve' subcommand: %q", out)
	}
}

func TestPrintConfigEmitsJSON(t *testing.T) {
	// Empty KEYCHAIN_* env -> memory store, valid defaults, exit 0.
	env := append(os.Environ(),
		"KEYCHAIN_STORE=",
		"KEYCHAIN_POSTGRES_URL=",
		"KEYCHAIN_RATELIMITER_ADDR=",
		"KEYCHAIN_LOG_LEVEL=",
	)
	out, err := runKeychain(t, env, "print-config")
	if err != nil {
		t.Fatalf("print-config: %v\n%s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("print-config did not emit JSON: %q", out)
	}
}
