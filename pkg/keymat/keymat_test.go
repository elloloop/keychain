package keymat

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func TestNewProducesUniqueKeys(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		plaintext, _, err := New("ck_test_")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[plaintext] {
			t.Fatalf("duplicate key generated after %d iterations: %q", i, plaintext)
		}
		seen[plaintext] = true
	}
}

func TestNewObeysPrefix(t *testing.T) {
	plaintext, _, err := New("ck_live_")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.HasPrefix(plaintext, "ck_live_") {
		t.Fatalf("plaintext = %q, want prefix %q", plaintext, "ck_live_")
	}
}

func TestNewHandlesEmptyPrefix(t *testing.T) {
	plaintext, _, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if plaintext == "" {
		t.Fatal("plaintext should not be empty")
	}
}

func TestNewHashRoundTrips(t *testing.T) {
	plaintext, hash, err := New("ck_test_")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := Hash(plaintext)
	if got != hash {
		t.Fatal("Hash(plaintext) must equal the hash returned by New")
	}
}

func TestNewProducesExpectedLength(t *testing.T) {
	plaintext, _, err := New("ck_test_")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 32 bytes -> 52 base32 chars (no padding) + prefix
	wantLen := len("ck_test_") + b32.EncodedLen(RandomBytes)
	if len(plaintext) != wantLen {
		t.Fatalf("len(plaintext) = %d, want %d", len(plaintext), wantLen)
	}
}

func TestHashDeterministic(t *testing.T) {
	a := Hash("ck_live_constant")
	b := Hash("ck_live_constant")
	if a != b {
		t.Fatal("Hash is not deterministic")
	}
}

func TestHashSizeMatchesSHA256(t *testing.T) {
	if HashSize != sha256.Size {
		t.Fatalf("HashSize = %d, want %d", HashSize, sha256.Size)
	}
}

func TestHashEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b []byte
		want bool
	}{
		{"identical", []byte("abcd"), []byte("abcd"), true},
		{"different same length", []byte("abcd"), []byte("abce"), false},
		{"different length", []byte("abcd"), []byte("abcde"), false},
		{"both empty", []byte{}, []byte{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HashEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("HashEqual = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
		prefix    string
		want      error
	}{
		{"empty", "", "ck_live_", ErrEmpty},
		{"valid with prefix", "ck_live_ABCD", "ck_live_", nil},
		{"valid no prefix", "anything", "", nil},
		{"wrong prefix", "sk_test_ABCD", "ck_live_", ErrPrefixMissing},
		{"prefix is exactly the input", "ck_live_", "ck_live_", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate(tc.plaintext, tc.prefix)
			if !errors.Is(got, tc.want) {
				t.Fatalf("Validate(%q, %q) = %v, want %v", tc.plaintext, tc.prefix, got, tc.want)
			}
		})
	}
}

// FuzzNewPrefix asserts New tolerates arbitrary prefix bytes without
// panicking and that the resulting key always carries the prefix.
func FuzzNewPrefix(f *testing.F) {
	f.Add("ck_live_")
	f.Add("")
	f.Add(strings.Repeat("p", 256))
	f.Fuzz(func(t *testing.T, prefix string) {
		plaintext, hash, err := New(prefix)
		if err != nil {
			t.Fatalf("New(%q) returned error: %v", prefix, err)
		}
		if !strings.HasPrefix(plaintext, prefix) {
			t.Fatalf("plaintext missing prefix")
		}
		if Hash(plaintext) != hash {
			t.Fatal("Hash(plaintext) != returned hash")
		}
	})
}

// FuzzValidate asserts Validate never panics on arbitrary input.
func FuzzValidate(f *testing.F) {
	f.Add("ck_live_abc", "ck_live_")
	f.Add("", "")
	f.Add("\x00\x01\x02", "ck_live_")
	f.Fuzz(func(t *testing.T, plaintext, prefix string) {
		_ = Validate(plaintext, prefix)
	})
}
