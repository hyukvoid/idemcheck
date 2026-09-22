package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewKey generates a fresh idempotency key: idemcheck-<random>.
// Every scenario derives its own base key so checks never contaminate
// each other with leftover server-side key state.
func NewKey() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable in practice; keep it visible.
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return "idemcheck-" + hex.EncodeToString(b)
}

// ScenarioKey derives the per-scenario key from the base key. All requests
// inside one scenario share exactly this value; different scenarios never do.
func ScenarioKey(base, scenario string) string {
	return base + "-" + scenario
}
