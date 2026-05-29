package keymat

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"io"
	"strings"
)

// RandomBytes is the amount of entropy in every generated key. 32 bytes =
// 256 bits, matching the sha256 output. Encoded as unpadded base32 this
// becomes 52 characters; with the prefix the full key is typically 60–70
// chars.
const RandomBytes = 32

// HashSize is the persisted hash length (sha256).
const HashSize = sha256.Size

// b32 is the alphabet used to encode the random payload. RFC 4648 base32
// with no padding — case-insensitive, URL-safe, no `0`/`O` ambiguity when
// transcribed (uppercase only).
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

var randomReader = rand.Reader

// Errors returned by Validate.
var (
	ErrEmpty         = errors.New("keymat: plaintext is empty")
	ErrPrefixMissing = errors.New("keymat: plaintext does not match expected prefix")
)

// New generates a fresh key with the given prefix and returns:
//   - plaintext: shown to the caller exactly once at issuance time
//   - hash:     persisted by the store for hash-based lookup at verify time
//
// The prefix is included in the plaintext AND in the hashed payload —
// rotating a key's prefix invalidates the old key's hash, which is the
// intended behaviour.
func New(prefix string) (plaintext string, hash [HashSize]byte, err error) {
	var b [RandomBytes]byte
	if _, err = io.ReadFull(randomReader, b[:]); err != nil {
		return "", [HashSize]byte{}, err
	}
	plaintext = prefix + b32.EncodeToString(b[:])
	hash = sha256.Sum256([]byte(plaintext))
	return plaintext, hash, nil
}

// Hash returns sha256(plaintext) — the lookup form keychain uses
// internally. Exposed so callers (the gRPC handler) can hash inbound
// plaintexts the same way.
func Hash(plaintext string) [HashSize]byte {
	return sha256.Sum256([]byte(plaintext))
}

// HashEqual compares two hashes in constant time. Both arguments must be
// HashSize bytes; mismatched lengths are reported as not-equal without
// short-circuit.
func HashEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// Validate is a cheap structural reject for obviously-malformed plaintext
// before it reaches the store. It does NOT verify the secret — that
// requires a hash lookup. Use this in the verify handler to fail fast on
// junk inputs.
func Validate(plaintext, prefix string) error {
	if plaintext == "" {
		return ErrEmpty
	}
	if prefix != "" && !strings.HasPrefix(plaintext, prefix) {
		return ErrPrefixMissing
	}
	return nil
}
