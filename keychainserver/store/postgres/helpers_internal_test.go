package postgres

import (
	"embed"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNonNilStrings(t *testing.T) {
	if got := nonNilStrings(nil); got == nil || len(got) != 0 {
		t.Fatalf("nonNilStrings(nil) = %#v, want empty non-nil slice", got)
	}
	in := []string{"read"}
	got := nonNilStrings(in)
	if len(got) != 1 || got[0] != "read" {
		t.Fatalf("nonNilStrings = %v, want [read]", got)
	}
}

func TestUnmarshalMetadataEmptyAndInvalid(t *testing.T) {
	if got := unmarshalMetadata(nil); got != nil {
		t.Fatalf("unmarshalMetadata(nil) = %v, want nil", got)
	}
	if got := unmarshalMetadata([]byte(`{}`)); got != nil {
		t.Fatalf("unmarshalMetadata({}) = %v, want nil", got)
	}
	if got := unmarshalMetadata([]byte(`not json`)); got != nil {
		t.Fatalf("unmarshalMetadata(invalid) = %v, want nil", got)
	}
}

func TestMarshalMetadata(t *testing.T) {
	if got := string(marshalMetadata(nil)); got != "{}" {
		t.Fatalf("marshalMetadata(nil) = %q, want {}", got)
	}
	got := string(marshalMetadata(map[string]string{"tier": "prod"}))
	if got != `{"tier":"prod"}` {
		t.Fatalf("marshalMetadata = %q, want tier json", got)
	}
}

func TestPostgresViolationHelpers(t *testing.T) {
	if !isFKViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("foreign-key violation was not detected")
	}
	if isFKViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("unique violation reported as foreign-key violation")
	}
	if !isUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("unique violation was not detected")
	}
	if isUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("foreign-key violation reported as unique violation")
	}
}

func TestRunMigrationsReportsMissingEmbeddedSource(t *testing.T) {
	dsn := os.Getenv("KEYCHAIN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("KEYCHAIN_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	orig := migrationsFS
	migrationsFS = embed.FS{}
	t.Cleanup(func() { migrationsFS = orig })

	err := RunMigrations(dsn)
	if err == nil || !strings.Contains(err.Error(), "migrate source") {
		t.Fatalf("RunMigrations err = %v, want migrate source error", err)
	}
}
