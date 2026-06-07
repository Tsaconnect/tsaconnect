package models

// TP reward-level model (fee/TP overhaul SP5).
//
// The PM image defines a 15-level reward ladder. The math is regular:
//
//	threshold[n] = 10^(n-1) TP   →  L1=1, L2=10, L3=100, … L15=1e14
//	rewardUSD[n] = threshold[n] / 100   →  L1=$0.01, L2=$0.10, … L15=$1e12
//
// i.e. each level's cash reward is always 1% of its TP threshold. Because both
// threshold and reward scale as powers of ten, the reward is ~1% of accrued
// TP-value regardless of the 1-TP-=-$1 minting constant — so the constant sets
// level PACING, not the reward ratio (design §3).
//
// Levels are CUMULATIVE: every level a user crosses is rewarded exactly once,
// ever (design "Decisions locked"). Jumping L1→L4 pays L2+L3+L4.
//
// From L6 up the reward also carries a PHYSICAL PRIZE label (Starlink, phone,
// car, …). Prizes are operationally / manually fulfilled — the engine records a
// prize-due row and notifies an admin, it never auto-pays a prize.

// TPLevelCount is the number of reward levels (1..15).
const TPLevelCount = 15

// TPLevel describes one reward level of the ladder. Pure data — the thresholds
// and rewards are deterministic powers of ten, the prize labels come from the
// PM image.
type TPLevel struct {
	// Level is the 1-based level number (1..15).
	Level int

	// ThresholdTP is the cumulative TP a user must reach to CROSS into this
	// level: 10^(Level-1). A user is "at" level n when tp_balance >=
	// ThresholdTP of n and < ThresholdTP of n+1.
	ThresholdTP float64

	// RewardUSD is the cash-equivalent reward paid (in MCGP, converted at the
	// OTC price at award time) when this level is first crossed: ThresholdTP/100.
	RewardUSD float64

	// Prize is the physical-prize label for L6+ (empty for L1..L5). Recorded as
	// a prize_due row + admin notification; never auto-paid.
	Prize string
}

// tpLevels is the materialized 15-level ladder. Index 0 == Level 1.
var tpLevels = buildTPLevels()

// physicalPrizeLabels are the L6..L15 prize labels from the PM image, indexed by
// level (label for level n is at index n-6). L1..L5 have no physical prize.
var physicalPrizeLabels = map[int]string{
	6:  "Starlink",
	7:  "Phone",
	8:  "Car",
	9:  "Laptop",
	10: "House",
	11: "Solar",
	12: "Trip",
	13: "Company",
	14: "Gift",
	15: "Gift", // top of the ladder; PM image trails off — keep a non-empty label
}

// buildTPLevels materializes the deterministic ladder once at package init.
func buildTPLevels() []TPLevel {
	levels := make([]TPLevel, 0, TPLevelCount)
	threshold := 1.0 // 10^0 for level 1
	for n := 1; n <= TPLevelCount; n++ {
		levels = append(levels, TPLevel{
			Level:       n,
			ThresholdTP: threshold,
			RewardUSD:   threshold / 100.0,
			Prize:       physicalPrizeLabels[n],
		})
		threshold *= 10.0
	}
	return levels
}

// TPLevels returns the full ladder (defensive copy so callers can't mutate the
// package-level data).
func TPLevels() []TPLevel {
	out := make([]TPLevel, len(tpLevels))
	copy(out, tpLevels)
	return out
}

// LevelFor returns the highest level whose ThresholdTP is <= tp. Returns 0 when
// tp is below level 1's threshold (i.e. tp < 1 TP) — the user has not reached
// any reward level yet. Caps at TPLevelCount.
func LevelFor(tp float64) int {
	level := 0
	for _, l := range tpLevels {
		if tp >= l.ThresholdTP {
			level = l.Level
		} else {
			break
		}
	}
	return level
}

// RewardUSDForLevel returns the cash-equivalent reward (USD) for level n, or 0
// for an out-of-range level (n < 1 or n > TPLevelCount). 10^(n-1)/100.
func RewardUSDForLevel(n int) float64 {
	if n < 1 || n > TPLevelCount {
		return 0
	}
	return tpLevels[n-1].RewardUSD
}

// ThresholdTPForLevel returns the cumulative-TP threshold to reach level n, or 0
// for an out-of-range level. 10^(n-1).
func ThresholdTPForLevel(n int) float64 {
	if n < 1 || n > TPLevelCount {
		return 0
	}
	return tpLevels[n-1].ThresholdTP
}

// PrizeForLevel returns the physical-prize label for level n, empty when the
// level has no physical prize (L1..L5) or is out of range.
func PrizeForLevel(n int) string {
	if n < 1 || n > TPLevelCount {
		return ""
	}
	return tpLevels[n-1].Prize
}

// LevelHasPhysicalPrize reports whether level n carries a physical prize (L6+).
func LevelHasPhysicalPrize(n int) bool {
	return PrizeForLevel(n) != ""
}

// TPLevelPhysicalPrizeFloor is the first level that carries a physical prize.
const TPLevelPhysicalPrizeFloor = 6
