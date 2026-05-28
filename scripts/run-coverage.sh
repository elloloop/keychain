#!/usr/bin/env bash
#
# run-coverage.sh — produce a merged coverage profile (cover.out) for the
# coverage gate. Postgres-backed tests run inline when
# KEYCHAIN_TEST_POSTGRES_URL is set (CI sets it; locally start a Postgres
# and export it). Without it the Postgres-dependent paths simply report
# lower coverage.

set -euo pipefail

rm -f cover.out cover.*.out

go test -count=1 -race -timeout=600s \
  -coverprofile=cover.out \
  -coverpkg=./internal/...,./cmd/... \
  ./...
