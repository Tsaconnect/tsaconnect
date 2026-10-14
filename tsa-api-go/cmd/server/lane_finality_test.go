package main

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// A key lane's cancel is final only at the deepest confirmation depth any
// payer waits for (review item 1).
func TestLaneFinalityDepth_IsTheDeepestConfiguredDepth(t *testing.T) {
	cfg := &config.Config{RampTreasuryConfirmations: 5, NameChangeMinConfirmations: 3,
		NameChangeMinConfirmationsByChain: map[string]int{"polygon": 12, "base": 2}}
	if got := laneFinalityDepth(cfg); got != 12 {
		t.Fatalf("depth = %d, want 12", got)
	}
}
