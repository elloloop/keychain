package config_test

import (
	"encoding/json"
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

func TestLoadNormalizesStoreAndLogLevel(t *testing.T) {
	t.Setenv("KEYCHAIN_STORE", "POSTGRES")
	t.Setenv("KEYCHAIN_POSTGRES_URL", "postgres://u:p@db:5432/keychain?sslmode=disable")
	t.Setenv("KEYCHAIN_LOG_LEVEL", "WARN")

	c := config.Load()
	if c.Store != config.StorePostgres {
		t.Fatalf("Store = %q, want postgres", c.Store)
	}
	if c.LogLevel != "warn" {
		t.Fatalf("LogLevel = %q, want warn", c.LogLevel)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadUsesExplicitAddressesAndRateLimiter(t *testing.T) {
	t.Setenv("KEYCHAIN_GRPC_BIND_ADDR", "127.0.0.1:18080")
	t.Setenv("KEYCHAIN_METRICS_BIND_ADDR", "127.0.0.1:19090")
	t.Setenv("KEYCHAIN_RATELIMITER_ADDR", "ratelimiter:8081")

	c := config.Load()
	if c.GRPCBindAddr != "127.0.0.1:18080" {
		t.Fatalf("GRPCBindAddr = %q", c.GRPCBindAddr)
	}
	if c.MetricsBindAddr != "127.0.0.1:19090" {
		t.Fatalf("MetricsBindAddr = %q", c.MetricsBindAddr)
	}
	if c.RateLimiterAddr != "ratelimiter:8081" {
		t.Fatalf("RateLimiterAddr = %q", c.RateLimiterAddr)
	}
}

func TestValidateRequiresBindAddresses(t *testing.T) {
	c := config.Config{
		MetricsBindAddr: "0.0.0.0:9090",
		Store:           config.StoreMemory,
		LogLevel:        "info",
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "KEYCHAIN_GRPC_BIND_ADDR") {
		t.Fatalf("Validate err = %v, want one mentioning KEYCHAIN_GRPC_BIND_ADDR", err)
	}

	c.GRPCBindAddr = "0.0.0.0:8080"
	c.MetricsBindAddr = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "KEYCHAIN_METRICS_BIND_ADDR") {
		t.Fatalf("Validate err = %v, want one mentioning KEYCHAIN_METRICS_BIND_ADDR", err)
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

func TestValidateRejectsInvalidPostgresURL(t *testing.T) {
	c := config.Config{
		GRPCBindAddr:    "0.0.0.0:8080",
		MetricsBindAddr: "0.0.0.0:9090",
		Store:           config.StorePostgres,
		PostgresURL:     "postgres://[::1",
		LogLevel:        "info",
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "KEYCHAIN_POSTGRES_URL invalid") {
		t.Fatalf("Validate err = %v, want invalid postgres URL", err)
	}
}

func TestValidateAcceptsEverySupportedLogLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			c := config.Config{
				GRPCBindAddr:    "0.0.0.0:8080",
				MetricsBindAddr: "0.0.0.0:9090",
				Store:           config.StoreMemory,
				LogLevel:        level,
			}
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
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

func TestRedactedLeavesPasswordlessAndInvalidURLsAlone(t *testing.T) {
	for _, raw := range []string{
		"postgres://user@db:5432/keychain?sslmode=disable",
		"://not a url",
		"",
	} {
		t.Run(raw, func(t *testing.T) {
			c := config.Config{PostgresURL: raw}
			out := c.Redacted()
			if out.PostgresURL != raw {
				t.Fatalf("PostgresURL = %q, want %q", out.PostgresURL, raw)
			}
		})
	}
}

func TestRedactedDoesNotMutateOriginal(t *testing.T) {
	c := config.Config{PostgresURL: "postgres://user:supersecret@db:5432/keychain"}
	out := c.Redacted()
	if c.PostgresURL != "postgres://user:supersecret@db:5432/keychain" {
		t.Fatalf("original PostgresURL mutated: %q", c.PostgresURL)
	}
	if out.PostgresURL == c.PostgresURL {
		t.Fatalf("redacted URL should differ from original secret URL: %q", out.PostgresURL)
	}
}

func TestJSONIncludesEveryField(t *testing.T) {
	c := config.Config{
		GRPCBindAddr:    "0.0.0.0:8080",
		MetricsBindAddr: "0.0.0.0:9090",
		Store:           config.StoreMemory,
		LogLevel:        "info",
	}
	b := c.JSON()
	for _, field := range []string{"grpc_bind_addr", "metrics_bind_addr", "store", "log_level"} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("JSON missing field %q: %s", field, b)
		}
	}
	var decoded config.Config
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("JSON emitted invalid JSON: %v", err)
	}
}
