package models

import "fmt"

// GenerationPercentages is the canonical TP distribution split: the fraction of
// the TP pool credited to each generation, indexed 0..10 (gen 0 = the
// transacting user / self, gen 1 = direct referrer, … gen 10).
//
// Per the PM spec, the pool is shared EQUALLY across "you + 10 generations" —
// 11 participants — so every generation's share is 1/11. The values MUST sum to
// EXACTLY 1.0 so the TP pool is conserved (boot-asserted in init() below): on a
// full active 11-gen chain the engine credits `tpPool * pct` per generation, and
// any generation with no live ancestor routes its 1/11 share to the global pool
// (which SP4 redistributes equally to all registered users).
//
// The init() below is a hard static assertion: any future edit that breaks the
// sum panics at boot rather than silently leaking value.
//
// This lives in the models package so it is a single source of truth for the
// fee/TP engine (services.FeeDistributionService) without an import cycle
// (handlers → services already exists; services cannot import handlers).
var GenerationPercentages = []float64{
	1.0 / 11.0, // gen 0: self
	1.0 / 11.0, // gen 1: direct referrer
	1.0 / 11.0, // gen 2
	1.0 / 11.0, // gen 3
	1.0 / 11.0, // gen 4
	1.0 / 11.0, // gen 5
	1.0 / 11.0, // gen 6
	1.0 / 11.0, // gen 7
	1.0 / 11.0, // gen 8
	1.0 / 11.0, // gen 9
	1.0 / 11.0, // gen 10
}

// init enforces the conservation invariant at boot: the TP ladder MUST sum to
// exactly 1.0 (within float tolerance). A drift here would over- or under-mint
// TP on every full-chain fee event, so fail loud rather than leak value.
func init() {
	var sum float64
	for _, p := range GenerationPercentages {
		sum += p
	}
	if d := sum - 1.0; d > 1e-9 || d < -1e-9 {
		panic(fmt.Sprintf(
			"models.GenerationPercentages must sum to 1.0 for TP-pool conservation, got %.6f (drift %.6f)",
			sum, d))
	}
}
