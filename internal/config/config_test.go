package config_test

import (
	"strings"
	"testing"

	"github.com/elloloop/keychain/internal/config"
)

func TestLoadDefaultsToMemoryStore(t *testing.T) {
	// Clear vars that might leak from the parent shell.
	for _, k := range []string{
		"KEYCHAIN_GRPC_BIND_ADDR", "KEYCHAIN_METRICS_BIND_ADDR",
		"KEYCHAIN_STORE", "KEYCHAIN_POSTGRES_URL",
		"KEYCHAIN_RATELIMITER_ADDR", "KEYCHAIN_LOG_LEVEL",
	} {
		t.Setenv(k, "")
	}
	c := config.Load()
	if c.Store != config.StoreMemory {
		t.Fatalf("Store = %q, want memory", c.Store)
	}
	if c.GRPCBindAddr != "0.0.0.0:8080" {
		t.Fatalf("GRPCBindAddr = %q", c.GRPCBindAddr)
	}
	if c.LogLevel != "info" {
		t.Fatalf("LogLevel = %q", c.LogLevel)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadAutoSelectsPostgresWhenURLPresent(t *testing.T) {
	t.Setenv("KEYCHAIN_STORE", "")
	t.Setenv("KEYCHAIN_POSTGRES_URL", "postgres://u:p@db:5432/keychain?sslmode=disable")
	c := config.Load()
	if c.Store != config.StorePostgres {
		t.Fatalf("Store auto-select = %q, want postgres", c.Store)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsPostgresWithoutURL(t *testing.T) {
	t.Setenv("KEYCHAIN_STORE", "postgres")
	t.Setenv("KEYCHAIN_POSTGRES_URL", "")
	c := config.Load()
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "KEYCHAIN_POSTGRES_URL") {
		t.Fatalf("Validate err = %v, want one mentioning KEYCHAIN_POSTGRES_URL", err)
	}
}

func TestValidateRejectsUnknownStore(t *testing.T) {
	t.Setenv("KEYCHAIN_STORE", "redis")
	c := config.Load()
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted unsupported store")
	}
}

func TestValidateRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("KEYCHAIN_LOG_LEVEL", "trace")
	c := config.Load()
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted unsupported log level")
	}
}

func TestRedactedScrubsPostgresPassword(t *testing.T) {
	c := config.Config{
		PostgresURL: "postgres://user:supersecret@db:5432/keychain?sslmode=disable",
	}
	out := c.Redacted()
	if strings.Contains(out.PostgresURL, "supersecret") {
		t.Fatalf("password leaked in redacted form: %q", out.PostgresURL)
	}
	if !strings.Contains(out.PostgresURL, "REDACTED") {
		t.Fatalf("redacted marker missing: %q", out.PostgresURL)
	}
}

func TestJSONIncludesEveryField(t *testing.T) {
	c := config.Config{
		GRPCBindAddr:    "0.0.0.0:8080",
		MetricsBindAddr: "0.0.0.0:9090",
		Store:           config.StoreMemory,
		LogLevel:        "info",
	}
	b, err := c.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, field := range []string{"grpc_bind_addr", "metrics_bind_addr", "store", "log_level"} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("JSON missing field %q: %s", field, b)
		}
	}
}
