package models

import "fmt"

// GenerationPercentages is the canonical TP distribution ladder: the fraction
// of the TP pool credited to each generation, indexed 0..10 (gen 0 = the
// transacting user / self, gen 1 = direct referrer, … gen 10). These are the
// PM-image percentages (gen0 35.4% → gen10), and they MUST sum to EXACTLY 1.0
// so the TP pool is conserved: on a full active 11-gen chain the engine credits
// `tpPool * pct` per generation, so any sum > 1.0 over-mints TP value from
// nothing on every full-chain fee event (design §9 conservation requirement).
//
// The handwritten PM image rounded gen0..gen10 to values summing to 1.002 (a
// 0.2% over-mint). gen10 absorbs the residual so the ladder conserves by
// construction: gen0..gen9 are byte-identical to the image; gen10 = 1.0 −
// Σ(gen0..gen9) = 0.002 (image rounded it to 0.004). gen10's share is the
// smallest and most aspirational rung, so carrying the rounding error there has
// the least economic effect while keeping the pool exact.
//
// The init() below is a hard static assertion: any future ladder edit that
// breaks the sum panics at boot rather than silently leaking value.
//
// This lives in the models package so it is a single source of truth for the
// fee/TP engine (services.FeeDistributionService) without an import cycle
// (handlers → services already exists; services cannot import handlers).
var GenerationPercentages = []float64{
	0.354, // gen 0: self
	0.177, // gen 1: direct referrer
	0.142, // gen 2
	0.106, // gen 3
	0.071, // gen 4
	0.057, // gen 5
	0.042, // gen 6
	0.028, // gen 7
	0.014, // gen 8
	0.007, // gen 9
	0.002, // gen 10 (residual: 1.0 − Σ(gen0..gen9) so the pool conserves)
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
