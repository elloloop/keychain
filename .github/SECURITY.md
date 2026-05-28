# Security policy

## Reporting a vulnerability

**Do not open a public issue for security vulnerabilities.**

Report privately via [GitHub Security Advisories](https://github.com/elloloop/keychain/security/advisories/new).

Include:

- A description of the vulnerability and its impact.
- A repro: steps, payload, affected version.
- Whether you have already disclosed this to anyone else.

We aim to acknowledge reports within 2 business days and to publish a fix within 30 days for high/critical issues.

## Scope note

Keychain is an internal API-key issuance and verification service. It is
designed to be called by your own gateway/backend over a trusted private
network, not exposed directly to untrusted callers. Vulnerabilities that
assume an attacker already has direct unauthenticated access to the
management RPCs without any network-layer control are out of scope.

The verification hot path (`VerifyKey`) is the one RPC that may legitimately
be called per request from your gateway; threats against that path
(timing oracles on key lookups, hash collision attacks, etc.) are in scope
even when the caller is trusted.

## Supported versions

The most recent minor release receives security fixes. Older minor versions are best-effort.

## What we treat as a vulnerability

- Key forgery or bypass — accepting a key that was never issued, or
  treating a revoked/expired key as valid.
- Timing-side-channel that distinguishes between "no such key" and "wrong
  key" in a way that enables enumeration.
- Permission/scope escalation — a key being accepted for an action it
  does not carry permission for.
- Quota-evasion via the keychain layer — the verify path returning
  `allowed: true` when the linked rate-limiter limit is exceeded.
- Information disclosure of key material, key hashes, or owner identifiers
  beyond what the API intends.
- Denial-of-service vectors that an authenticated caller can trigger at
  disproportionately low cost.
- Supply-chain compromise (dependency, container image, GitHub Actions
  workflow).

Bugs that affect correctness but not security should be filed as regular issues.
