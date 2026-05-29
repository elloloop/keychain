// Package keymat is keychain's key material primitive: random key
// generation, sha256 hashing for lookup, and constant-time hash
// comparison. Plaintext keys exist only at issuance time (returned by New)
// and at verify time (re-hashed via Hash before lookup); persisted state
// holds the hash, never the plaintext.
//
// The package is dependency-free beyond crypto/rand, crypto/sha256, and
// encoding/base32, and has no transport or storage dependencies, so it is
// safe to import from any layer.
package keymat
