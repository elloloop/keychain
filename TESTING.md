# Testing

Every change to this repository must add or update all applicable tests.
The bar is production confidence for a backend that may be called on every
customer request.

## Commands

Fast tests:

```bash
go test ./...
make test
make test-fast
```

Race tests:

```bash
go test -race ./...
make test-race
```

Coverage gate:

```bash
make postgres-up
export KEYCHAIN_TEST_POSTGRES_URL=postgres://keychain:keychain@localhost:5432/keychain?sslmode=disable
make test-cover
```

Postgres integration tests:

```bash
make postgres-up
export KEYCHAIN_TEST_POSTGRES_URL=postgres://keychain:keychain@localhost:5432/keychain?sslmode=disable
make test-integration
```

If `5432` is already occupied locally, choose another host port:

```bash
POSTGRES_TEST_PORT=55432 make postgres-up
export KEYCHAIN_TEST_POSTGRES_URL=postgres://keychain:keychain@localhost:55432/keychain?sslmode=disable
make test-integration
```

The Postgres tests are env-gated instead of build-tagged: they are present in
the normal package test set, skip when `KEYCHAIN_TEST_POSTGRES_URL` is unset,
and run in CI/release with a real Postgres service.

Docker Compose E2E:

```bash
make test-e2e
```

Benchmarks:

```bash
go test -bench=. -benchmem ./...
make test-bench
```

Fuzz smoke:

```bash
bash scripts/run-go-fuzz-targets.sh 15s
make test-fuzz
```

Standard pre-merge checks:

```bash
make verify
```

`make verify` includes race-enabled coverage and therefore requires
`KEYCHAIN_TEST_POSTGRES_URL`.

Strict CI/release checks, including Docker Compose E2E:

```bash
make verify-ci
```

## Current Harness

- Pure unit and table-driven tests live beside their packages, for example
  `internal/config` and `pkg/keymat`.
- Service-layer tests live in `keychainserver/server_test.go` and use the
  memory store plus fake rate-limiter dependencies.
- gRPC integration coverage lives in `keychainserver/grpc_integration_test.go`
  through `bufconn`.
- Store behavior is enforced by `keychainserver/store/conformance`; every
  store implementation must run that same suite.
- Postgres tests run against a real database when
  `KEYCHAIN_TEST_POSTGRES_URL` is set. The conformance test applies embedded
  migrations once, shares a pool, and truncates tables between subtests.
- Docker Compose E2E scripts under `test/e2e` boot Postgres plus the service
  container and exercise the public gRPC API through `grpcurl`.
- CLI smoke tests live under `tests/smoke` and build the binary before running
  non-serving commands.
- Fuzz targets currently cover key material generation and validation.
- Coverage is merged by `scripts/run-coverage.sh` and enforced by
  `scripts/coverage-gate.sh` using `.coverage-gates.yml`. The coverage profile
  instruments production packages; the shared conformance harness still runs
  during coverage but is not itself counted as product code.
- Repository metadata is checked by `scripts/check-codeowners.sh`.

## Required Test Coverage By Change Type

1. Pure logic changes require unit, table-driven, and edge-case tests.
2. Public gRPC API changes require validation, response-shape, error-path,
   generated-code freshness, and E2E coverage where the behavior is externally
   observable.
3. Store or query changes require the shared conformance suite plus
   implementation-specific integration tests when driver failure modes matter.
4. Migration changes require real Postgres migration tests, including
   idempotency and dirty/failure behavior where applicable.
5. Permission changes require positive and negative authorization matrix tests.
6. Tenant or privacy-sensitive changes require cross-tenant negative tests and
   sensitive-field exclusion checks.
7. External client changes require fake clients for service tests and
   `httptest.Server` tests for success, error, timeout, retry, and malformed
   responses. Normal tests must not call real third-party services.
8. Background job or queue changes require synchronous test runners, retry
   assertions, idempotency tests, duplicate-message tests, poison-message
   tests, and cancellation tests.
9. Bug fixes require a regression test that fails before the fix and passes
   after it.
10. Concurrent code requires race tests plus cancellation or goroutine-leak
    oriented tests where feasible.
11. Public contract changes require protobuf lint/generation checks and
    backward-compatible response assertions.
12. Performance-sensitive changes require benchmarks or load-test hooks.
13. Security-sensitive changes require negative and abuse-case tests.

## Adding Tests

Use the existing seams before adding new abstractions. `keychainserver.New`
accepts a concrete `store.Store`, optional `RateLimiter`, logger, and fakeable
clock through package tests. The memory store is the default service-test
fixture; use Postgres only when persistence, migrations, constraints, or
driver errors are the behavior under test.

For store behavior, extend `keychainserver/store/conformance` when every driver
must behave the same way. Add implementation-specific tests only for behavior
that belongs to one driver, such as Postgres migration errors or corrupt rows.

For E2E behavior, add a focused script under `test/e2e` and include it in
`test/e2e/run-docker-compose-suite.sh`. Keep each script isolated with its own
Compose project name and cleanup trap.

For generated protobuf changes, run:

```bash
buf lint
buf generate
git diff --exit-code -- gen
```

## Not Currently Applicable

This service does not currently expose an HTTP application router, application
auth/session system, file upload path, webhook emitter, background worker,
queue consumer, cache, or third-party HTTP client. If any of those surfaces are
added, the same change must add the matching test helpers and examples before
it lands.

The only HTTP endpoint today is `/healthz` on the metrics server, covered from
`cmd/keychain` tests. Application authorization is delegated to upstream
identity/gateway services; keychain's own permission surface is API-key scope
checking in `VerifyKey`.
