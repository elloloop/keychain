// Package config loads keychain's runtime configuration from
// KEYCHAIN_* environment variables and validates it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// StoreDriver enumerates the supported persistence backends.
type StoreDriver string

const (
	StoreMemory   StoreDriver = "memory"
	StorePostgres StoreDriver = "postgres"
)

// Config is the resolved runtime configuration. Zero-value fields fall
// back to documented defaults; see Load.
type Config struct {
	GRPCBindAddr    string      `json:"grpc_bind_addr"`
	MetricsBindAddr string      `json:"metrics_bind_addr"`
	Store           StoreDriver `json:"store"`
	PostgresURL     string      `json:"postgres_url"`
	RateLimiterAddr string      `json:"rate_limiter_addr"`
	LogLevel        string      `json:"log_level"`
}

// Load reads KEYCHAIN_* env vars and returns a Config with documented
// defaults filled in. Run Validate() before using the result.
func Load() Config {
	c := Config{
		GRPCBindAddr:    getEnv("KEYCHAIN_GRPC_BIND_ADDR", "0.0.0.0:8080"),
		MetricsBindAddr: getEnv("KEYCHAIN_METRICS_BIND_ADDR", "0.0.0.0:9090"),
		Store:           StoreDriver(strings.ToLower(getEnv("KEYCHAIN_STORE", ""))),
		PostgresURL:     getEnv("KEYCHAIN_POSTGRES_URL", ""),
		RateLimiterAddr: getEnv("KEYCHAIN_RATELIMITER_ADDR", ""),
		LogLevel:        strings.ToLower(getEnv("KEYCHAIN_LOG_LEVEL", "info")),
	}
	// Store driver auto-selection: if KEYCHAIN_STORE is unset, use postgres
	// when a URL is provided, otherwise memory (useful for local dev).
	if c.Store == "" {
		if c.PostgresURL != "" {
			c.Store = StorePostgres
		} else {
			c.Store = StoreMemory
		}
	}
	return c
}

// Validate returns an error describing the first invalid setting.
func (c Config) Validate() error {
	if c.GRPCBindAddr == "" {
		return errors.New("KEYCHAIN_GRPC_BIND_ADDR is required")
	}
	if c.MetricsBindAddr == "" {
		return errors.New("KEYCHAIN_METRICS_BIND_ADDR is required")
	}
	switch c.Store {
	case StoreMemory, StorePostgres:
	default:
		return fmt.Errorf("KEYCHAIN_STORE=%q is unsupported; use memory or postgres", c.Store)
	}
	if c.Store == StorePostgres {
		if c.PostgresURL == "" {
			return errors.New("KEYCHAIN_POSTGRES_URL is required when KEYCHAIN_STORE=postgres")
		}
		if _, err := url.Parse(c.PostgresURL); err != nil {
			return fmt.Errorf("KEYCHAIN_POSTGRES_URL invalid: %w", err)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("KEYCHAIN_LOG_LEVEL=%q is unsupported", c.LogLevel)
	}
	return nil
}

// Redacted returns a copy with secret-carrying fields scrubbed for
// human-facing output (print-config, log lines).
func (c Config) Redacted() Config {
	out := c
	out.PostgresURL = redactURL(c.PostgresURL)
	return out
}

// JSON marshals the config (already-redacted form recommended).
func (c Config) JSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

func getEnv(key, fallback string) string {
	// Empty is treated the same as unset so that callers can clear a var
	// from a shell or compose file without being thrown into a validation
	// error; the default applies in either case.
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPwd := u.User.Password(); hasPwd {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	return u.String()
}
