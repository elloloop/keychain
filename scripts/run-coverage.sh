#!/usr/bin/env bash
#
# run-coverage.sh — produce a merged coverage profile (cover.out) for the
# coverage gate. KEYCHAIN_TEST_POSTGRES_URL must be set so Postgres-backed
# behavior is measured against a real database.

set -euo pipefail

if [[ -z "${KEYCHAIN_TEST_POSTGRES_URL:-}" ]]; then
  echo "KEYCHAIN_TEST_POSTGRES_URL is required for coverage. Run 'make postgres-up' and export the printed DSN." >&2
  exit 1
fi

rm -f cover.out cover.*.out

coverpkg="$(
  go list ./internal/... ./cmd/... ./keychainserver/... ./pkg/... \
    | grep -v '/keychainserver/store/conformance$' \
    | paste -sd, -
)"

go test -count=1 -race -timeout=600s \
  -coverprofile=cover.out \
  -coverpkg="$coverpkg" \
  ./...
