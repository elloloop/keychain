package memory_test

import (
	"testing"

	"github.com/elloloop/keychain/keychainserver/store"
	"github.com/elloloop/keychain/keychainserver/store/conformance"
	"github.com/elloloop/keychain/keychainserver/store/memory"
)

// TestConformance runs the shared Store conformance suite against the
// in-memory driver. Every Store implementation runs the same suite.
func TestConformance(t *testing.T) {
	conformance.Run(t, func(_ *testing.T) store.Store {
		return memory.New()
	})
}
