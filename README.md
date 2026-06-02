# keychain

API-key issuance and verification service. Internal-only: your gateway and
admin dashboard call it; end users never touch it directly.

Sits between [identity](https://github.com/elloloop/identity) (human auth) and
[rate-limiter](https://github.com/elloloop/rate-limiter) (quota math):

```text
Public internet
    │
    ▼
identity                          your gateway / app APIs
 - login / OAuth                          │ Bearer ck_live_abc…
 - JWKS                                   ▼
 - passkeys                          keychain
                                       │  • CreateKey / Revoke / Rotate
                                       │  • VerifyKey (hot path)
                                       │
                                       │  Consume / Reserve
                                       ▼
                                  rate-limiter
```

## What it provides

- **API keys** — create, verify, revoke, rotate, list.
- **Workspaces and APIs** — keys live under an `API`, which lives under a
  `Workspace`. Each workspace's keys are isolated.
- **Permissions** — string-array scopes (`["chat:write", "rerank:read"]`)
  checked on every `VerifyKey`.
- **Limit refs** — each key can carry references to limits defined in
  `rate-limiter`. `VerifyKey` evaluates them inline and returns a unified
  decision.

## What it does not do

- **No application-layer auth for end users.** That's identity. Keychain's
  callers are your own services.
- **No edge / global verify.** Keychain is regional. Co-locate it with your
  gateway and add a gateway-side cache for sub-ms hot-path lookups.
- **No tokenization.** Token counts are caller-supplied; keychain (via
  rate-limiter) does the math, not the counting.
- **No standalone rate-limiting primitive.** That's rate-limiter's job.
- **No audit sink yet.** The service returns verification decisions inline;
  durable audit/export wiring is not implemented.

## Quick start

```bash
docker compose up -d --build
```

The local stack starts Postgres and keychain:

- gRPC: `localhost:28080`
- Health: `http://localhost:29090/healthz`
- Postgres: `localhost:25432`

## Configuration

```text
KEYCHAIN_GRPC_BIND_ADDR=0.0.0.0:8080
KEYCHAIN_METRICS_BIND_ADDR=0.0.0.0:9090
KEYCHAIN_POSTGRES_URL=postgres://keychain:keychain@db:5432/keychain?sslmode=disable
KEYCHAIN_RATELIMITER_ADDR=rate-limiter:8080
KEYCHAIN_LOG_LEVEL=info
```

## Consuming the API

**Go** — import the generated client:

```bash
go get github.com/elloloop/keychain@v0.2.5
```

```go
import apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"

conn, _ := grpc.NewClient("keychain:8080", grpc.WithTransportCredentials(insecure.NewCredentials()))
client := apikeyv1.NewAPIKeyServiceClient(conn)

resp, _ := client.VerifyKey(ctx, &apikeyv1.VerifyKeyRequest{
    Plaintext: bearer,
    Action:    "chat.completions:write",
    Cost:      2048,
    RequestId: "...",
})
if !resp.Valid {
    // 403 / 429 — see resp.Result and resp.LimitDecisions
}
```

**Other languages** — each release attaches a `keychain-protos-<version>`
bundle (`.tar.gz` / `.zip` + `.sha256`) for codegen against the pinned
contract.

## Embedding keychain in a Go server

Instead of running the container, a Go program can import keychain and
register it on its own `*grpc.Server`. The container and embedders both
construct the service through `keychainserver.New`.

```bash
go get github.com/elloloop/keychain@v0.2.5
```

```go
import (
    "context"
    "log"
    "net"

    "google.golang.org/grpc"

    apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
    "github.com/elloloop/keychain/keychainserver"
    kcpg "github.com/elloloop/keychain/keychainserver/store/postgres"
)

func main() {
    ctx := context.Background()

    // Postgres store: pings the pool and runs embedded migrations
    // synchronously; returns an error on any failure.
    store, err := kcpg.New(ctx, "postgres://keychain:keychain@db:5432/keychain?sslmode=disable")
    if err != nil { log.Fatal(err) }
    defer store.Close()

    kc, err := keychainserver.New(ctx, keychainserver.Options{
        Store: store,
        // RateLimiter: optional; supply your own client to evaluate
        // keys with LimitRefs at VerifyKey time.
    })
    if err != nil { log.Fatal(err) }

    g := grpc.NewServer()
    apikeyv1.RegisterApiKeyServiceServer(g, kc)
    lis, _ := net.Listen("tcp", ":8080")
    g.Serve(lis)
}
```

A runnable end-to-end example lives in
[`examples/embedded`](./examples/embedded). For tests and local dev the
[`memory`](./keychainserver/store/memory) store works too — but it is
**not durable** across restarts and is not for production, even
single-instance deployments. Production uses
[`postgres`](./keychainserver/store/postgres) or another driver
implementing the [`Store`](./keychainserver/store) interface; external
drivers run the shared
[`conformance`](./keychainserver/store/conformance) suite to verify
identical behaviour.

## Local development

```bash
make help          # list targets
make ci            # lint, tidy-check, vuln, build, test, smoke, fuzz
make postgres-up   # throwaway Postgres for the DB-backed paths
make test-cover    # coverage profile + per-package gates
make ci-full       # ci + docker-compose end-to-end
```

CI runs golangci-lint, protobuf checks, `govulncheck`, race-enabled tests
with per-package coverage gates, boot smoke, fuzz smoke, Docker builds, a
Docker Compose e2e, and a Trivy filesystem scan. CodeQL (`codeql.yml`) and
a nightly race/fuzz loop (`nightly.yml`) run on a schedule.

## Releases

Push a `v*` tag. The release re-runs every CI gate, then:

- builds and pushes the multi-arch image
  `ghcr.io/elloloop/keychain:<version>` (and `:latest`) with SBOM and
  provenance attestations;
- signs each tag with cosign keyless OIDC;
- scans the published image with Trivy (HIGH/CRITICAL gate, SARIF to GitHub
  Security);
- pulls the image back and verifies its `version` output;
- creates a GitHub Release with the proto bundle and checksums.

Verify a published image's signature:

```bash
cosign verify ghcr.io/elloloop/keychain:<version> \
  --certificate-identity-regexp '^https://github.com/elloloop/keychain/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## License

Apache-2.0 — see [LICENSE](./LICENSE).
