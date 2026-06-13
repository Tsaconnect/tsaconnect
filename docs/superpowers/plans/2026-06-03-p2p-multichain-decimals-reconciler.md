# P2P Multi-Chain — Plan 4: Per-(chain,token) Decimals + Reconciler Fail-Loud Guard

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development / executing-plans. Checkbox steps.

**Goal:** Make backend token-decimals resolution chain-aware (BSC USDT/USDC are 18 decimals vs Sonic's 6 — a 10¹² amount bug otherwise), and lock the reconciler's fail-loud-on-unconfigured-chain guarantee with a test.

**Architecture:** `tokenDecimalsFor` gains a `chainID` argument and returns 18 for BSC (56/97) stables, 6 for Sonic (146/14601) stables, 18 for MCGP/default. The 4 call sites already have the ad's `ChainID` in scope. Plus a service-level test that `ReconcileTrade` on a trade whose chain isn't configured errors rather than orphan-reclaiming.

**Tech Stack:** Go 1.24, `internal/services/p2p_trade_service.go` (+ test). App-side decimals (the 4 RN maps) are **Plan 7**, not here.

**Depends on:** Plan 3 (per-entity-chain resolution; `ReconcileTrade` already resolves by `trade.ChainID`).

---

## Task 1: Chain-aware `tokenDecimalsFor`

**Files:** `internal/services/p2p_trade_service.go` (def ~line 3504 + 4 call sites: ~908, ~1066, ~1068, ~2690); test in `p2p_trade_service_test.go`.

- [ ] **Step 1 — failing test.** Append to `p2p_trade_service_test.go`:

```go
func TestTokenDecimalsFor_PerChain(t *testing.T) {
	cases := []struct {
		chainID int64
		symbol  string
		want    int
	}{
		{146, "USDT", 6}, {146, "USDC", 6}, {146, "MCGP", 18}, // Sonic mainnet
		{14601, "USDT", 6},                                     // Sonic testnet
		{56, "USDT", 18}, {56, "USDC", 18},                     // BSC mainnet (Binance-Peg = 18)
		{97, "USDT", 18}, {97, "USDC", 18},                     // BSC testnet
		{146, "WTF", 18},                                       // unknown symbol → 18
	}
	for _, c := range cases {
		if got := tokenDecimalsFor(c.chainID, c.symbol); got != c.want {
			t.Errorf("tokenDecimalsFor(%d,%q) = %d, want %d", c.chainID, c.symbol, got, c.want)
		}
	}
}
```

- [ ] **Step 2 — run, expect fail** (`tokenDecimalsFor` takes 1 arg today). `go test ./internal/services/ -run TestTokenDecimalsFor -v`.

- [ ] **Step 3 — implement.** Replace the existing `tokenDecimalsFor`:

```go
// tokenDecimalsFor returns the on-chain ERC-20 decimals for a token on a given
// chain. Stablecoins differ by chain family: Sonic USDT/USDC are 6 decimals,
// the Binance-Peg BSC USDT/USDC are 18. MCGP (Sonic-only) is 18. Unknown chains
// fall back to the Sonic-style assumption (6 for stables) — the only configured
// P2P chains are Sonic (146/14601) and BSC (56/97).
func tokenDecimalsFor(chainID int64, symbol string) int {
	sym := strings.ToUpper(symbol)
	switch chainID {
	case 56, 97: // BSC mainnet / testnet
		switch sym {
		case "USDT", "USDC":
			return 18
		}
	}
	switch sym {
	case "USDT", "USDC":
		return 6
	case "MCGP":
		return 18
	default:
		return 18
	}
}
```

Update the 4 call sites to pass the ad's chain: `tokenDecimalsFor(ad.Token)` → `tokenDecimalsFor(ad.ChainID, ad.Token)` (all four sites have `ad` in scope).

- [ ] **Step 4 — run, expect pass.** `go test ./internal/services/ -run TestTokenDecimalsFor -v` PASS; `go build ./...` clean.

- [ ] **Step 5 — commit.** `git commit -am "fix(p2p): chain-aware token decimals (BSC stables = 18, Sonic = 6)"`

---

## Task 2: Reconciler fail-loud guard test

**Files:** `internal/services/p2p_trade_service_test.go`.

- [ ] **Step 1 — write the test** (mirror `TestPrepareRelease_UnconfiguredChain_FailsLoud`):

```go
// TestReconcileTrade_UnconfiguredChain_FailsLoud locks §8.5: a non-terminal
// trade whose ChainID isn't in the escrow registry must error out of
// reconciliation (never read the wrong chain's escrow and orphan-reclaim it).
func TestReconcileTrade_UnconfiguredChain_FailsLoud(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db) // registry has chain 146 only
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), SellerID: uuid.New(), BuyerID: uuid.New(),
		Token: "USDT", ChainID: 56, // BSC — not configured in the test registry
		Funding: models.P2PTradeFundingPooled,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	if _, err := svc.ReconcileTrade(context.Background(), trade.ID); err == nil {
		t.Fatalf("ReconcileTrade on unconfigured chain 56 must error, got nil")
	}
	// The trade must NOT have been orphan-reclaimed/cancelled by mis-reading Sonic.
	var after models.P2PTrade
	db.First(&after, "id = ?", trade.ID)
	if after.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q after failed reconcile, want unchanged 'pending_payment'", after.Status)
	}
}
```

- [ ] **Step 2 — run.** `go test ./internal/services/ -run 'TestReconcileTrade_UnconfiguredChain' -v` PASS. If it does NOT pass (e.g. ReconcileTrade returns nil for an unconfigured chain instead of erroring), STOP and report — that would mean Plan 3's reconciler fail-loud is incomplete and needs fixing first.

- [ ] **Step 3 — full suite.** `go test ./internal/services/ -count=1` green.

- [ ] **Step 4 — commit.** `git commit -am "test(p2p): reconciler fails loud on unconfigured chain (no orphan-reclaim)"`

---

## Self-review (completed)
- **Spec coverage:** §5.8/§8.6 backend half (per-(chain,token) decimals) + §8.5 reconciler fail-loud guard. App-side decimals (the 4 RN maps in `chains.ts`/`index.tsx`/`my-trades.tsx`/`services/p2p.ts`) are explicitly Plan 7.
- **No placeholder:** complete code; the decimals map covers the exact configured chains; unknown-chain fallback documented.
- **Type consistency:** `tokenDecimalsFor(chainID int64, symbol string) int` updated at the definition + all 4 call sites (each has `ad.ChainID`).
- **Guard value:** the reconciler test asserts both the error AND that no DB mutation/orphan-reclaim happened — locking the destructive-mis-dispatch risk from spec §12.
