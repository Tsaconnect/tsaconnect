# Stablecoin Buy (Phase B1) — Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user buy USDC/USDT with naira and receive it, delivered direct from Flutterwave to their own wallet address on one of Flutterwave's four supported chains.

**Architecture:** Mirrors the shipped sell leg. The user pays into their existing standing virtual account; `charge.completed` routes to their open buy intent; the naira is converted to stablecoin inside our Flutterwave balance (`ConvertBalance`); the stablecoin is sent to the user via `direct-transfers` with `type:"crypto"`. A polling watcher drives the state machine, because Flutterwave publishes no webhook for conversion or transfer settlement. No bridge, no hot wallet, no server-side signing.

**Tech Stack:** Go 1.24, Gin, GORM/PostgreSQL (SQLite in most tests), `math/big` for all money.

**Spec:** `docs/superpowers/specs/2026-08-30-stablecoin-buy-design.md`

## Global Constraints

- **Money is never float.** All amounts are `*big.Int` (base/minor units) or `*big.Rat` (rates). Never `float64` for a user-visible amount. The only permitted float is inside the Flutterwave wire structs, which the vendor defines that way.
- **Never advance money before funds are provably present.** `converting` is entered only after the naira is spendable. `delivering` is entered only after the conversion reaches `SUCCESSFUL` — never when merely accepted (`201 {"status":"NEW"}`).
- **Every failure is stuck, not lost.** No path may mark an intent terminal while holding user funds. Non-terminal + visible in the stuck list beats terminal + hidden.
- **Stablecoin base units are 6 decimals** (`services.StableAssetDecimals`). Fiat minor units are 2 decimals for every currency in the registry.
- **Flutterwave references must be 6–42 characters.** Follow `itConversionRef`'s length accounting.
- **Delivery matrix is authoritative:** Polygon/Ethereum/Solana take USDC+USDT; Base takes USDC only; everything else is rejected. Never widen it without re-verifying against Flutterwave's docs.
- **The delivery address is never user-typed.** It comes from `Wallet.Addresses`.
- **Spread is `IT_STABLE_SPREAD_BPS` (default 155), floored against the user.** Phase B1 charges the spread only; there is no separate fee line (see Task 4 note).
- Run `go build ./...`, `go vet ./...` and `go test ./... -count=1` before every commit.

---

### Task 1: Buy statuses, columns, and error sentinels

**Files:**
- Modify: `internal/models/it_models.go`
- Test: `internal/models/it_models_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces: `models.ITStatusPaymentReceived`, `models.ITStatusAwaitingFunds`, `models.ITStatusDelivering` (all `string`); `ITIntent.DeliveryID string`, `ITIntent.DeliveryAttempts int`

`DestChain` and `DestAddress` already exist on `ITIntent` — reserved for exactly this. Do not re-add them.

- [ ] **Step 1: Write the failing test**

Append to `internal/models/it_models_test.go`:

```go
func TestBuyStatusesAreDistinct(t *testing.T) {
	// A duplicated status string silently merges two states in every switch
	// that drives the buy state machine.
	all := []string{
		models.ITStatusPendingPayment,
		models.ITStatusPaymentReceived,
		models.ITStatusAwaitingFunds,
		models.ITStatusConverting,
		models.ITStatusDelivering,
		models.ITStatusSettled,
	}
	seen := map[string]bool{}
	for _, s := range all {
		if s == "" {
			t.Fatal("empty status constant")
		}
		if seen[s] {
			t.Fatalf("duplicate status %q", s)
		}
		seen[s] = true
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/ -run TestBuyStatusesAreDistinct -count=1`
Expected: FAIL — `undefined: models.ITStatusPaymentReceived`

- [ ] **Step 3: Add the constants**

In `internal/models/it_models.go`, beside the existing stablecoin sell statuses:

```go
// ── Stablecoin buy (Phase B1) ──
// pending_payment is reused from the MCGP buy flow: it means the same thing
// here, and it is the ONLY state that can claim an incoming charge.
const (
	ITStatusPaymentReceived = "payment_received" // charge matched to this intent
	ITStatusAwaitingFunds   = "awaiting_funds"   // naira not yet spendable at FW
	ITStatusDelivering      = "delivering"       // crypto transfer accepted, not settled
)
```

- [ ] **Step 4: Add the columns**

In the `ITIntent` struct, after the `ConversionID` / `ConversionAttempts` fields:

```go
	// DeliveryID is Flutterwave's transfer id for the OUTBOUND crypto payout
	// (type:"crypto"). Persisted for the same reason ConversionID is: the
	// transfer is asynchronous, so the poll that settles it needs the id, and
	// an operator recovering a stuck delivery needs it too.
	DeliveryID string `gorm:"size:64" json:"deliveryId,omitempty"`
	// DeliveryAttempts bounds retries the same way ConversionAttempts does.
	// A fresh attempt mints a fresh reference, so a retry cannot collide with
	// Flutterwave's idempotency on the previous failed transfer.
	DeliveryAttempts int `gorm:"default:0" json:"deliveryAttempts,omitempty"`
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/models/ -count=1 && go build ./...`
Expected: PASS, build clean

- [ ] **Step 6: Commit**

```bash
git add internal/models/it_models.go internal/models/it_models_test.go
git commit -m "feat(it): add stablecoin buy statuses and delivery columns"
```

---

### Task 2: Delivery eligibility — chain, asset, and whether the user can receive

**Files:**
- Modify: `internal/services/stable_assets.go`
- Test: `internal/services/stable_assets_test.go`

**Interfaces:**
- Consumes: `deliveryMatrix`, `SupportsDelivery`, `ValidateDeliveryAddress` (all existing)
- Produces:
  - `func ValidateBuyDelivery(chain, asset, address string) error`
  - `func DeliverableChains(asset string, addresses map[string]string) []string`

- [ ] **Step 1: Write the failing tests**

Append to `internal/services/stable_assets_test.go`:

```go
func TestValidateBuyDelivery(t *testing.T) {
	const evm = "0x1111111111111111111111111111111111111111"
	const sol = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"

	for _, tc := range []struct {
		name          string
		chain, asset  string
		address       string
		wantErr       error
	}{
		{"polygon usdc", "POLYGON", "USDC", evm, nil},
		{"polygon usdt", "POLYGON", "USDT", evm, nil},
		{"ethereum usdt", "ETHEREUM", "USDT", evm, nil},
		{"solana usdc", "SOLANA", "USDC", sol, nil},
		{"base usdc", "BASE", "USDC", evm, nil},
		// Flutterwave rejects Base+USDT. A rejected transfer to a live
		// address is not recoverable, so this must fail before an intent.
		{"base usdt rejected", "BASE", "USDT", evm, ErrUnsupportedChainAsset},
		{"bsc rejected", "BSC", "USDC", evm, ErrUnsupportedChainAsset},
		{"tron rejected", "TRON", "USDT", evm, ErrUnsupportedChainAsset},
		{"evm address on solana", "SOLANA", "USDC", evm, ErrBadDeliveryAddress},
		{"solana address on polygon", "POLYGON", "USDC", sol, ErrBadDeliveryAddress},
		{"empty address", "POLYGON", "USDC", "", ErrBadDeliveryAddress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBuyDelivery(tc.chain, tc.asset, tc.address)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestDeliverableChains(t *testing.T) {
	const evm = "0x1111111111111111111111111111111111111111"
	const sol = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"

	t.Run("only chains the user can actually receive on", func(t *testing.T) {
		// No solana address → solana must not be offered, or funds land
		// somewhere the user cannot see.
		got := DeliverableChains("USDC", map[string]string{"POLYGON": evm, "BASE": evm})
		want := []string{"BASE", "POLYGON"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("usdt hides base", func(t *testing.T) {
		got := DeliverableChains("USDT", map[string]string{"POLYGON": evm, "BASE": evm, "SOLANA": sol})
		for _, c := range got {
			if c == "BASE" {
				t.Fatal("BASE offered for USDT; Flutterwave rejects that pair")
			}
		}
	})

	t.Run("unsupported chains are dropped", func(t *testing.T) {
		got := DeliverableChains("USDC", map[string]string{"BSC": evm, "SONIC": evm})
		if len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run "TestValidateBuyDelivery|TestDeliverableChains" -count=1`
Expected: FAIL — `undefined: ValidateBuyDelivery`

- [ ] **Step 3: Implement**

Append to `internal/services/stable_assets.go`:

```go
// ValidateBuyDelivery gates a buy's delivery target before an intent exists.
//
// Delivery is the one irreversible step in the buy flow: Flutterwave rejects
// an unsupported (chain, asset) pair, and a rejected transfer to a live
// address is not recoverable. Both checks therefore run at intent creation,
// not at delivery time.
func ValidateBuyDelivery(chain, asset, address string) error {
	chain = strings.ToUpper(strings.TrimSpace(chain))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if !SupportsDelivery(chain, asset) {
		return fmt.Errorf("%w: Flutterwave does not deliver %s on %s",
			ErrUnsupportedChainAsset, asset, chain)
	}
	return ValidateDeliveryAddress(chain, address)
}

// DeliverableChains returns the chains this user can be paid on for `asset`,
// sorted for a stable UI ordering.
//
// Takes a plain chain->address map rather than the wallet itself so it stays
// pure and testable; the service layer builds that map from the jsonb entry
// list (see userDeliveryAddresses), filtering out external and inactive
// addresses on the way.
//
// A chain is offered ONLY when Flutterwave delivers the pair AND the user
// actually holds an address on it. Wallet.Addresses is a sparse map — a Solana
// address exists only if the wallet derived one — and offering a chain the
// user cannot receive on would strand funds somewhere they cannot see.
func DeliverableChains(asset string, addresses map[string]string) []string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	out := make([]string, 0, len(deliveryMatrix))
	for chain := range deliveryMatrix {
		if !SupportsDelivery(chain, asset) {
			continue
		}
		if strings.TrimSpace(addresses[chain]) == "" {
			continue
		}
		out = append(out, chain)
	}
	sort.Strings(out)
	return out
}
```

Add `"sort"` to the import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run "TestValidateBuyDelivery|TestDeliverableChains" -count=1 -v`
Expected: PASS, all subtests

- [ ] **Step 5: Commit**

```bash
git add internal/services/stable_assets.go internal/services/stable_assets_test.go
git commit -m "feat(it): gate stablecoin buy delivery on chain, asset and user address"
```

---

### Task 3: Buy-direction rate and spread

**Files:**
- Modify: `internal/services/stable_spread.go`
- Test: `internal/services/stable_spread_test.go`

**Interfaces:**
- Consumes: `FWRate`, `ErrBadFxProbe` (existing)
- Produces:
  - `func DeriveBuyFiatPerUnit(rate FWRate) (*big.Rat, error)`
  - `func ApplyBuySpread(grossAssetUnits *big.Int, spreadBps int64) *big.Int`

**This is the correctness trap in the whole plan.** `DeriveFiatPerUnit` returns `dst/src`, which is fiat-per-asset only when the asset is the SOURCE — true for sell. On buy the fiat is the source, so the same expression yields asset-per-fiat: inverted, and wrong by a factor of the rate squared if used directly.

- [ ] **Step 1: Write the failing tests**

Append to `internal/services/stable_spread_test.go`:

```go
func TestDeriveBuyFiatPerUnit(t *testing.T) {
	// Buy probes source=NGN, destination=asset: "to receive 1 USDC you pay
	// 1650.25 NGN". The answer must be NGN per USDC, i.e. src/dst.
	rate := FWRate{
		Source:      FWMoney{Currency: "NGN", Amount: 1650.25},
		Destination: FWMoney{Currency: "USDC", Amount: 1},
	}
	got, err := DeriveBuyFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := new(big.Rat).SetFloat64(1650.25)
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s, want %s", got.FloatString(6), want.FloatString(6))
	}
}

func TestDeriveBuyFiatPerUnit_IsTheInverseOfSell(t *testing.T) {
	// Guards the inversion bug directly: the same amounts read in opposite
	// directions must produce reciprocal rates.
	rate := FWRate{
		Source:      FWMoney{Currency: "NGN", Amount: 1650.25},
		Destination: FWMoney{Currency: "USDC", Amount: 1},
	}
	buy, err := DeriveBuyFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sell, err := DeriveFiatPerUnit(rate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	product := new(big.Rat).Mul(buy, sell)
	if product.Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatalf("buy * sell = %s, want exactly 1", product.FloatString(10))
	}
}

func TestDeriveBuyFiatPerUnit_RejectsUnusableProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate FWRate
	}{
		{"zero source", FWRate{Source: FWMoney{Amount: 0}, Destination: FWMoney{Amount: 1}}},
		{"zero destination", FWRate{Source: FWMoney{Amount: 1}, Destination: FWMoney{Amount: 0}}},
		{"negative source", FWRate{Source: FWMoney{Amount: -5}, Destination: FWMoney{Amount: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeriveBuyFiatPerUnit(tc.rate); !errors.Is(err, ErrBadFxProbe) {
				t.Fatalf("err = %v, want ErrBadFxProbe", err)
			}
		})
	}
}

func TestApplyBuySpread(t *testing.T) {
	// The user receives LESS asset. Flooring means we never deliver a unit we
	// did not buy — the mirror of ApplySellSpread never paying out one.
	got := ApplyBuySpread(big.NewInt(100_000_000), 155) // 100 USDC, 1.55%
	want := big.NewInt(98_450_000)                      // 98.45 USDC
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s, want %s", got, want)
	}

	if got := ApplyBuySpread(big.NewInt(100), 0); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("zero spread changed the amount: %s", got)
	}
	if got := ApplyBuySpread(nil, 155); got.Sign() != 0 {
		t.Fatalf("nil input = %s, want 0", got)
	}

	// Flooring, never rounding up: 1 unit at 1.55% must round DOWN to 0.
	if got := ApplyBuySpread(big.NewInt(1), 155); got.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("got %s, want 0 (must floor against the user)", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run "TestDeriveBuyFiatPerUnit|TestApplyBuySpread" -count=1`
Expected: FAIL — `undefined: DeriveBuyFiatPerUnit`

- [ ] **Step 3: Implement**

Append to `internal/services/stable_spread.go`:

```go
// DeriveBuyFiatPerUnit returns fiat units per one whole asset unit for the BUY
// direction, where the fiat is the SOURCE and the asset the DESTINATION.
//
// This is deliberately not DeriveFiatPerUnit. That function returns dst/src,
// which is fiat-per-asset only while the asset is the source — true on sell,
// inverted on buy. Reusing it here would price a purchase at the reciprocal of
// the real rate, which for NGN/USDC is wrong by roughly six orders of
// magnitude in the user's favour.
func DeriveBuyFiatPerUnit(rate FWRate) (*big.Rat, error) {
	if rate.Source.Amount <= 0 || rate.Destination.Amount <= 0 {
		return nil, fmt.Errorf("%w: source=%v destination=%v",
			ErrBadFxProbe, rate.Source.Amount, rate.Destination.Amount)
	}
	src := new(big.Rat).SetFloat64(rate.Source.Amount)
	dst := new(big.Rat).SetFloat64(rate.Destination.Amount)
	if src == nil || dst == nil {
		return nil, ErrBadFxProbe
	}
	return new(big.Rat).Quo(src, dst), nil
}

// ApplyBuySpread reduces the asset the user receives by the company spread,
// flooring.
//
// The mirror of ApplySellSpread: there, flooring means never paying out fiat
// we did not receive; here, it means never delivering a stablecoin unit we did
// not buy. The platform holds no position on these trades, so a rounded-up
// unit would have to come from somewhere.
func ApplyBuySpread(grossAssetUnits *big.Int, spreadBps int64) *big.Int {
	if grossAssetUnits == nil {
		return big.NewInt(0)
	}
	if spreadBps <= 0 {
		return new(big.Int).Set(grossAssetUnits)
	}
	out := new(big.Int).Mul(grossAssetUnits, big.NewInt(10000-spreadBps))
	return out.Quo(out, big.NewInt(10000)) // Quo truncates toward zero
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run "TestDeriveBuyFiatPerUnit|TestApplyBuySpread" -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/services/stable_spread.go internal/services/stable_spread_test.go
git commit -m "feat(it): add buy-direction rate derivation and spread"
```

---

### Task 4: QuoteStableBuy

**Files:**
- Create: `internal/services/it_stable_buy_quote.go`
- Test: `internal/services/it_stable_buy_quote_test.go`

**Interfaces:**
- Consumes: `DeriveBuyFiatPerUnit`, `ApplyBuySpread` (Task 3); `s.rateQuoter`, `s.checkRatePlausible`, `s.stableSpreadBps`, `s.stableLockTTL()`, `s.stableMinSell()` (existing)
- Produces:
  - `type StableBuyQuote struct { Asset, Currency string; FiatMinor, AssetUnits, GrossAssetUnits, RatePerUnitMinor *big.Int; ExpiresAt time.Time }`
  - `func (s *ITService) QuoteStableBuy(ctx context.Context, asset, fiatMinor, currency string) (*StableBuyQuote, error)`
  - `var ErrStableBuyBelowMinimum = errors.New("it stable buy: delivery amount is below the provider minimum")`

**Note on fees.** Phase B1 charges the spread only, per spec §4. `StableBuyQuote` deliberately has no `FeeMinor`: adding a fee line is a commercial decision, and inventing one here would silently change pricing. If a fee leg is wanted later it mirrors `applyStableSellFees`.

**Note on the minimum.** Flutterwave's 10 USDC/USDT floor is an OUTBOUND floor, so on buy it binds the asset the user *receives* — check it after the spread, not on the fiat input.

- [ ] **Step 1: Write the failing tests**

Create `internal/services/it_stable_buy_quote_test.go`:

```go
package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

// buyRateQuoter echoes a fixed NGN->asset probe: "to receive 1 USDC, pay 1650.25 NGN".
type buyRateQuoter struct {
	ngnPerUnit float64
	err        error
}

func (q *buyRateQuoter) QuoteRate(_ context.Context, src, dst string, destAmount float64) (FWRate, error) {
	if q.err != nil {
		return FWRate{}, q.err
	}
	return FWRate{
		Source:      FWMoney{Currency: src, Amount: q.ngnPerUnit * destAmount},
		Destination: FWMoney{Currency: dst, Amount: destAmount},
	}, nil
}

// Mirrors newStableSellTestService, but wires fx: checkRatePlausible skips
// entirely when s.fx is nil, so a service built as &ITService{} would make the
// implausible-rate test pass vacuously — worse than failing.
func newBuyQuoteTestService(t *testing.T, q FXRateQuoter) *ITService {
	t.Helper()
	s := NewITService(nil, nil, stubFx{ngn: 1650})
	s.SetRateQuoter(q)
	s.SetStableSpreadBps(155)
	s.SetStableRateLockTTL(15 * time.Minute)
	return s
}

func TestQuoteStableBuy_PricesAndAppliesSpread(t *testing.T) {
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 1650.25})

	// 165,025.00 NGN = 16,502,500 kobo. At 1650.25 NGN/USDC that is exactly
	// 100 USDC gross; the 155bps spread leaves 98.45 USDC.
	q, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := big.NewInt(100_000_000); q.GrossAssetUnits.Cmp(want) != 0 {
		t.Fatalf("gross = %s, want %s", q.GrossAssetUnits, want)
	}
	if want := big.NewInt(98_450_000); q.AssetUnits.Cmp(want) != 0 {
		t.Fatalf("net = %s, want %s", q.AssetUnits, want)
	}
	if q.AssetUnits.Cmp(q.GrossAssetUnits) >= 0 {
		t.Fatal("spread did not reduce what the user receives")
	}
}

func TestQuoteStableBuy_RejectsImplausibleRate(t *testing.T) {
	// The sandbox returned a rate wrong by ~six orders of magnitude. Pricing
	// must refuse rather than transact on it.
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 2})
	_, err := s.QuoteStableBuy(context.Background(), "USDC", "16502500", "NGN")
	if !errors.Is(err, ErrStableRateImplausible) {
		t.Fatalf("err = %v, want ErrStableRateImplausible", err)
	}
}

func TestQuoteStableBuy_EnforcesDeliveryMinimum(t *testing.T) {
	// Flutterwave's floor is on the OUTBOUND leg, so it binds what the user
	// receives — after the spread, not the naira they pay.
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 1650.25})
	_, err := s.QuoteStableBuy(context.Background(), "USDC", "825125", "NGN") // ~5 USDC
	if !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("err = %v, want ErrStableBuyBelowMinimum", err)
	}
}

func TestQuoteStableBuy_RejectsBadInput(t *testing.T) {
	s := newBuyQuoteTestService(t, &buyRateQuoter{ngnPerUnit: 1650.25})
	for _, tc := range []struct {
		name, asset, fiat string
		wantErr           error
	}{
		{"not an asset", "MCGP", "16502500", ErrStableNotAnAsset},
		{"zero fiat", "USDC", "0", ErrStableBuyBelowMinimum},
		{"negative fiat", "USDC", "-1", ErrStableBuyBelowMinimum},
		{"garbage fiat", "USDC", "abc", ErrStableBuyBelowMinimum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.QuoteStableBuy(context.Background(), tc.asset, tc.fiat, "NGN"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
```

`stubFx` is the existing test double used by `newStableSellDBService`; check its
staleness behaviour reports *not* stale, or `stablePlausibilityRateFor` collapses to
"no opinion" and the plausibility test passes for the wrong reason.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run TestQuoteStableBuy -count=1`
Expected: FAIL — `undefined: (*ITService).QuoteStableBuy`

- [ ] **Step 3: Implement**

Create `internal/services/it_stable_buy_quote.go`:

```go
package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ErrStableBuyBelowMinimum means the stablecoin the user would RECEIVE is
// below Flutterwave's outbound floor.
//
// Checked against the post-spread asset amount, because the floor applies to
// the delivery transfer. Unlike the sell leg — where the floor can only be
// enforced after the user has irreversibly sent funds — buy can refuse before
// any money moves at all, which is strictly safer.
var ErrStableBuyBelowMinimum = errors.New("it stable buy: delivery amount is below the provider minimum")

// StableBuyQuote prices naira into stablecoin.
type StableBuyQuote struct {
	Asset    string
	Currency string
	// FiatMinor is what the user pays, in fiat minor units.
	FiatMinor *big.Int
	// GrossAssetUnits is what the fiat buys at Flutterwave's own rate;
	// AssetUnits is that less the company spread — what the user receives.
	// Both in asset base units (6 decimals).
	GrossAssetUnits *big.Int
	AssetUnits      *big.Int
	// RatePerUnitMinor is fiat minor units per one whole asset unit, spread
	// applied. Indicative only: the intent is priced again when money lands.
	RatePerUnitMinor *big.Int
	ExpiresAt        time.Time
}

// QuoteStableBuy prices `fiatMinor` of `currency` into `asset`.
//
// Indicative by design. The binding price is struck when the user's naira
// actually lands (see AutoCreditFromVA's buy branch), which is what removes
// the rate lock and with it every "paid after the quote expired" state.
func (s *ITService) QuoteStableBuy(ctx context.Context, asset, fiatMinor, currency string) (*StableBuyQuote, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	if s.rateQuoter == nil {
		return nil, ErrStableQuoteUnavailable
	}

	fiat, ok := new(big.Int).SetString(strings.TrimSpace(fiatMinor), 10)
	if !ok || fiat.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amount must be a positive integer of minor units", ErrStableBuyBelowMinimum)
	}

	// Probe one whole asset unit. Direction matters and is the inverse of
	// sell: fiat is the SOURCE, the asset the DESTINATION.
	rate, err := s.rateQuoter.QuoteRate(ctx, currency, asset, 1)
	if err != nil {
		return nil, fmt.Errorf("it stable buy quote: %w", err)
	}
	fiatPerUnit, err := DeriveBuyFiatPerUnit(rate)
	if err != nil {
		return nil, err
	}
	if err := s.checkRatePlausible(ctx, fiatPerUnit, currency); err != nil {
		return nil, err
	}

	// grossUnits = fiatMinor / 100 / fiatPerUnit * 10^6, floored.
	exp := new(big.Int).Exp(big.NewInt(10), big.NewInt(StableAssetDecimals), nil)
	grossRat := new(big.Rat).SetInt(fiat)
	grossRat.Quo(grossRat, new(big.Rat).SetInt64(100))
	grossRat.Quo(grossRat, fiatPerUnit)
	grossRat.Mul(grossRat, new(big.Rat).SetInt(exp))
	gross := new(big.Int).Quo(grossRat.Num(), grossRat.Denom())

	net := ApplyBuySpread(gross, s.stableSpreadBps)

	if net.Cmp(s.stableMinSell()) < 0 {
		return nil, fmt.Errorf("%w: minimum delivery is %g %s",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(s.stableMinSell()), asset)
	}

	// Effective per-unit price the user is getting, in fiat minor units.
	perUnit := new(big.Rat).Mul(fiatPerUnit, new(big.Rat).SetInt64(100))
	perUnit.Mul(perUnit, new(big.Rat).SetFrac64(10000, 10000-s.stableSpreadBps))
	ratePerUnit := new(big.Int).Quo(perUnit.Num(), perUnit.Denom())

	return &StableBuyQuote{
		Asset:            asset,
		Currency:         currency,
		FiatMinor:        fiat,
		GrossAssetUnits:  gross,
		AssetUnits:       net,
		RatePerUnitMinor: ratePerUnit,
		ExpiresAt:        time.Now().Add(s.stableLockTTL()),
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run TestQuoteStableBuy -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/services/it_stable_buy_quote.go internal/services/it_stable_buy_quote_test.go
git commit -m "feat(it): price stablecoin buys with a buy-direction spread"
```

---

### Task 5: CreateStableBuyIntent

**Files:**
- Create: `internal/services/it_stable_buy.go`
- Test: `internal/services/it_stable_buy_test.go`

**Interfaces:**
- Consumes: `QuoteStableBuy`, `StableBuyQuote` (Task 4); `ValidateBuyDelivery` (Task 2); buy statuses (Task 1)
- Produces:
  - `type StableBuyInstructions struct { AccountNumber, BankName, AccountName, Reference string }`
  - `func (s *ITService) CreateStableBuyIntent(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, destChain, destAddress string) (*models.ITIntent, *StableBuyInstructions, error)`
  - `var ErrStableBuyAlreadyOpen = errors.New("it stable buy: this user already has a purchase awaiting payment")`
  - `var ErrStableBuyNoVA = errors.New("it stable buy: user has no virtual account; activate one first")`

- [ ] **Step 1: Write the failing tests**

Create `internal/services/it_stable_buy_test.go`:

```go
package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testBuyEVM = "0x2222222222222222222222222222222222222222"

func testBuyQuote() *StableBuyQuote {
	return &StableBuyQuote{
		Asset:            "USDC",
		Currency:         "NGN",
		FiatMinor:        big.NewInt(16502500),
		GrossAssetUnits:  big.NewInt(100_000_000),
		AssetUnits:       big.NewInt(98_450_000),
		RatePerUnitMinor: big.NewInt(167600),
		ExpiresAt:        time.Now().Add(15 * time.Minute),
	}
}

func newStableBuyDBService(t *testing.T) (*ITService, uuid.UUID) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.FiatVirtualAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	userID := uuid.New()
	va := &models.FiatVirtualAccount{
		ID: uuid.New(), UserID: userID, Currency: "NGN",
		AccountNumber: "1234567890", BankName: "Wema Bank", AccountName: "TSA / Ada Okafor",
		Reference: "ITVA-" + userID.String(),
	}
	if err := db.Create(va).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}
	s := NewITService(db, nil, stubFx{ngn: 1500})
	s.SetStableSpreadBps(155)
	return s, userID
}

func TestCreateStableBuyIntent_ReturnsTheUsersStandingVA(t *testing.T) {
	// The user pays into the account they already have. No per-purchase VA is
	// minted: dynamic VAs were removed in July after "Invalid Amount" and
	// silent-expiry failures in production.
	s, userID := newStableBuyDBService(t)

	intent, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pay.AccountNumber != "1234567890" {
		t.Fatalf("account = %q, want the user's standing VA", pay.AccountNumber)
	}
	if intent.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment", intent.Status)
	}
	if intent.DestChain != "POLYGON" || intent.DestAddress != testBuyEVM {
		t.Fatalf("delivery target not persisted: %q %q", intent.DestChain, intent.DestAddress)
	}
	if intent.Direction != models.ITDirectionBuy {
		t.Fatalf("direction = %q, want buy", intent.Direction)
	}
}

func TestCreateStableBuyIntent_RejectsUndeliverableTarget(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	q := testBuyQuote()
	q.Asset = "USDT"

	// Flutterwave rejects Base+USDT, and a rejected transfer to a live address
	// is not recoverable — so this must fail before an intent exists.
	_, _, err := s.CreateStableBuyIntent(context.Background(), userID, q, "BASE", testBuyEVM)
	if !errors.Is(err, ErrUnsupportedChainAsset) {
		t.Fatalf("err = %v, want ErrUnsupportedChainAsset", err)
	}

	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d intents written for a rejected target, want 0", n)
	}
}

func TestCreateStableBuyIntent_OnlyOneAwaitingPayment(t *testing.T) {
	// pending_payment is the only state that can claim an incoming charge, so
	// two at once would make attribution ambiguous.
	s, userID := newStableBuyDBService(t)

	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("first intent: %v", err)
	}
	_, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if !errors.Is(err, ErrStableBuyAlreadyOpen) {
		t.Fatalf("err = %v, want ErrStableBuyAlreadyOpen", err)
	}
}

func TestCreateStableBuyIntent_FundedIntentDoesNotBlockANewOne(t *testing.T) {
	// An intent past pending_payment has already been funded and cannot claim
	// further money, so it must not block a second purchase.
	s, userID := newStableBuyDBService(t)

	first, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("first intent: %v", err)
	}
	if err := s.db.Model(first).Update("status", models.ITStatusConverting).Error; err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("second intent rejected while first was converting: %v", err)
	}
}

func TestCreateStableBuyIntent_RequiresAVirtualAccount(t *testing.T) {
	s, _ := newStableBuyDBService(t)
	_, _, err := s.CreateStableBuyIntent(context.Background(), uuid.New(), testBuyQuote(), "POLYGON", testBuyEVM)
	if !errors.Is(err, ErrStableBuyNoVA) {
		t.Fatalf("err = %v, want ErrStableBuyNoVA", err)
	}
}
```

Check `models.FiatVirtualAccount`'s real field names in `internal/models/fiat_virtual_account.go` and adjust the seed literal to match exactly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run TestCreateStableBuyIntent -count=1`
Expected: FAIL — `undefined: (*ITService).CreateStableBuyIntent`

- [ ] **Step 3: Implement**

Create `internal/services/it_stable_buy.go`:

```go
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	// ErrStableBuyAlreadyOpen enforces one purchase awaiting payment per user.
	// The standing VA's reference identifies the USER, not the purchase, so a
	// second intent in pending_payment would make an incoming charge ambiguous.
	ErrStableBuyAlreadyOpen = errors.New("it stable buy: this user already has a purchase awaiting payment")
	// ErrStableBuyNoVA means the user has no standing virtual account yet.
	ErrStableBuyNoVA = errors.New("it stable buy: user has no virtual account; activate one first")
)

// StableBuyInstructions is where the user sends their naira: their own
// standing virtual account, unchanged between purchases.
type StableBuyInstructions struct {
	AccountNumber string `json:"accountNumber"`
	BankName      string `json:"bankName"`
	AccountName   string `json:"accountName"`
	Reference     string `json:"reference"`
}

// CreateStableBuyIntent opens a purchase and tells the user where to pay.
//
// The delivery target is validated HERE rather than at delivery time, because
// delivery is the one irreversible step: Flutterwave rejects an unsupported
// (chain, asset) pair and a rejected transfer to a live address cannot be
// recovered. Nothing is written if the target is bad.
func (s *ITService) CreateStableBuyIntent(
	ctx context.Context,
	userID uuid.UUID,
	q *StableBuyQuote,
	destChain, destAddress string,
) (*models.ITIntent, *StableBuyInstructions, error) {
	if q == nil {
		return nil, nil, errors.New("it stable buy: nil quote")
	}
	destChain = strings.ToUpper(strings.TrimSpace(destChain))
	destAddress = strings.TrimSpace(destAddress)
	if err := ValidateBuyDelivery(destChain, q.Asset, destAddress); err != nil {
		return nil, nil, err
	}
	if q.AssetUnits == nil || q.AssetUnits.Cmp(s.stableMinSell()) < 0 {
		return nil, nil, fmt.Errorf("%w: minimum delivery is %g %s",
			ErrStableBuyBelowMinimum, StableFromBaseUnits(s.stableMinSell()), q.Asset)
	}

	var va models.FiatVirtualAccount
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND currency = ?", userID, q.Currency).
		First(&va).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrStableBuyNoVA
	} else if err != nil {
		return nil, nil, err
	}

	var open int64
	if err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Count(&open).Error; err != nil {
		return nil, nil, err
	}
	if open > 0 {
		return nil, nil, ErrStableBuyAlreadyOpen
	}

	intent := &models.ITIntent{
		ID:          uuid.New(),
		UserID:      userID,
		Direction:   models.ITDirectionBuy,
		Status:      models.ITStatusPendingPayment,
		Asset:       q.Asset,
		AssetAmount: q.AssetUnits.String(),
		Currency:    q.Currency,
		DestChain:   destChain,
		DestAddress: destAddress,
		LockedRate:  q.RatePerUnitMinor.String(),

		// Every numeric column must carry a value, not "". PostgreSQL rejects
		// ''::numeric outright and GORM sends unset fields in the INSERT.
		// SQLite's type affinity accepts them silently, which is how this bit
		// the sell leg. The values below are correct, not filler:
		//
		//	NgnKobo/AmountMinor  what the user pays, as quoted
		//	McgpAmount           a stablecoin buy has no MCGP leg
		//	FeeNgnKobo/NetNgnKobo  B1 charges the spread only; no fee line
		//	ReceivedAmount       unknown until the naira actually lands
		NgnKobo:        q.FiatMinor.String(),
		AmountMinor:    q.FiatMinor.String(),
		RateKobo:       q.RatePerUnitMinor.String(),
		RateMinor:      q.RatePerUnitMinor.String(),
		McgpAmount:     "0",
		FeeNgnKobo:     "0",
		NetNgnKobo:     q.FiatMinor.String(),
		FeeMinor:       "0",
		NetMinor:       q.FiatMinor.String(),
		ReceivedAmount: "0",
	}
	if err := s.db.WithContext(ctx).Create(intent).Error; err != nil {
		return nil, nil, err
	}

	return intent, &StableBuyInstructions{
		AccountNumber: va.AccountNumber,
		BankName:      va.BankName,
		AccountName:   va.AccountName,
		Reference:     va.Reference,
	}, nil
}
```

Field names on `models.ITIntent` and `models.FiatVirtualAccount` must be checked against the real structs; adjust rather than assume.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run TestCreateStableBuyIntent -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/services/it_stable_buy.go internal/services/it_stable_buy_test.go
git commit -m "feat(it): open a stablecoin buy against the user's standing VA"
```

---

### Task 6: Route an incoming charge to an open buy

**Files:**
- Modify: `internal/services/it_fiat_va.go` (inside `AutoCreditFromVA`, after the VA resolves to a user)
- Create: `internal/services/it_stable_buy_settle.go`
- Test: `internal/services/it_stable_buy_settle_test.go`

**Interfaces:**
- Consumes: buy statuses (Task 1)
- Produces: `func (s *ITService) settleStableBuyFromCharge(ctx context.Context, userID uuid.UUID, chargeID, amountMinor string) (bool, error)` — returns `(true, nil)` when the charge funded a stablecoin buy; `(false, nil)` when there was none and the caller should continue to the MCGP path.

**The charge is priced HERE, not at intent creation.** Spec §4 makes the binding
price the live rate when the naira lands — that is what removes the rate lock and
every "paid after the quote expired" state. Carrying the creation-time amount
forward would be a lock in all but name.

Money that has landed cannot be rejected, so a pricing failure parks the intent in
the existing `ITStatusAwaitingRate` rather than erroring: that status already means
"money is here and we could not price it, an operator must decide", and the MCGP
buy uses it the same way.

**This is the fork.** Getting it wrong either misroutes a user's money or silently changes today's live MCGP behaviour.

- [ ] **Step 1: Write the failing tests**

Create `internal/services/it_stable_buy_settle_test.go`:

```go
package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestSettleStableBuyFromCharge_FundsAnOpenIntent(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_1", "16502500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("charge did not fund the open intent")
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q, want payment_received", got.Status)
	}
	if got.PaymentRef != "chg_1" {
		t.Fatalf("payment ref = %q, want the charge id", got.PaymentRef)
	}
	if got.ReceivedAmount != "16502500" {
		t.Fatalf("received = %q, want what actually landed", got.ReceivedAmount)
	}
}

func TestSettleStableBuyFromCharge_NoOpenIntentFallsThrough(t *testing.T) {
	// A user with no open stablecoin buy must reach today's MCGP path
	// unchanged. Returning true here would silently swallow their payment.
	s, userID := newStableBuyDBService(t)

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_2", "500000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Fatal("claimed a charge with no open stablecoin intent")
	}
}

func TestSettleStableBuyFromCharge_RecordsWhatLandedNotWhatWasQuoted(t *testing.T) {
	// Underpayment is priced on what arrived, mirroring the sell leg's
	// received/quoted scaling. Crediting the quoted amount would deliver
	// stablecoin the user did not pay for.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_3", "8251250"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.ReceivedAmount != "8251250" {
		t.Fatalf("received = %q, want the underpaid amount", got.ReceivedAmount)
	}
}

func TestSettleStableBuyFromCharge_PricesAtTheLiveRateNotTheQuote(t *testing.T) {
	// Spec §4: the binding price is struck when the naira lands. If the rate
	// moved after the user tapped Buy they get the live rate — carrying the
	// quoted asset amount forward would be a rate lock by another name.
	s, userID := newStableBuyDBService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	s.SetStableSpreadBps(155)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	// The rate doubles before the money arrives: the same naira must now buy
	// roughly half the stablecoin.
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 3300.50})

	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_live", "16502500"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.AssetAmount == testBuyQuote().AssetUnits.String() {
		t.Fatal("delivered the amount quoted at creation; the rate was locked in effect")
	}
	if got.AssetAmount != "49225000" { // ~49.225 USDC after the 155bps spread
		t.Fatalf("asset amount = %q, want the live-rate figure", got.AssetAmount)
	}
}

func TestSettleStableBuyFromCharge_ParksWhenItCannotPrice(t *testing.T) {
	// The naira has landed and cannot be handed back, so an unusable rate must
	// park for an operator rather than error the webhook or price on a bad feed.
	s, userID := newStableBuyDBService(t)
	s.SetRateQuoter(&buyRateQuoter{err: errors.New("provider down")})
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	claimed, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_norate", "16502500")
	if err != nil {
		t.Fatalf("webhook errored on landed money: %v", err)
	}
	if !claimed {
		t.Fatal("charge fell through to MCGP; the user bought stablecoin")
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusAwaitingRate {
		t.Fatalf("status = %q, want awaiting_rate", got.Status)
	}
	if got.ReceivedAmount != "16502500" {
		t.Fatalf("received = %q, want what landed recorded even when unpriced", got.ReceivedAmount)
	}
}

func TestSettleStableBuyFromCharge_ParksWhenUnderpaidBelowTheFloor(t *testing.T) {
	// Below Flutterwave's outbound floor the delivery would be refused with the
	// user's naira already taken, so park instead of advancing.
	s, userID := newStableBuyDBService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	s.SetStableSpreadBps(155)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_dust", "165025"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusAwaitingRate {
		t.Fatalf("status = %q, want awaiting_rate (parked)", got.Status)
	}
}

func TestSettleStableBuyFromCharge_IsIdempotent(t *testing.T) {
	// Webhook redelivery must not double-settle.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)

	for i := 0; i < 2; i++ {
		if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_4", "16502500"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Where("payment_ref = ?", "chg_4").Count(&n)
	if n != 1 {
		t.Fatalf("%d intents claim the charge, want 1", n)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q after redelivery, want payment_received", got.Status)
	}
}

```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run TestSettleStableBuyFromCharge -count=1`
Expected: FAIL — `undefined: settleStableBuyFromCharge`

- [ ] **Step 3: Implement the claim**

Create `internal/services/it_stable_buy_settle.go`:

```go
package services

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// settleStableBuyFromCharge attributes an incoming naira charge to this user's
// open stablecoin purchase, if they have one.
//
// Returns (true, nil) when the charge funded a buy and the caller must stop.
// Returns (false, nil) when there was no open stablecoin buy — the caller then
// continues to the existing MCGP credit path, which is left untouched.
//
// "Open" means pending_payment specifically. That is the only state which can
// claim money: an intent already converting or delivering has been funded and
// cannot absorb more, so it must not block or capture a later payment.
func (s *ITService) settleStableBuyFromCharge(ctx context.Context, userID uuid.UUID, chargeID, amountMinor string) (bool, error) {
	chargeID = strings.TrimSpace(chargeID)
	if chargeID == "" {
		return false, errors.New("it stable buy: empty charge id")
	}

	// Idempotency first: a redelivered webhook must not settle twice.
	var existing models.ITIntent
	err := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).
		First(&existing).Error
	if err == nil {
		return IsStableAsset(existing.Asset), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	// Claim atomically: the WHERE carries the expected status, so two
	// concurrent webhooks cannot both move the same intent. Only the amount is
	// written here — the price is struck below, once the intent is ours.
	amountMinor = strings.TrimSpace(amountMinor)
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}).
		Updates(map[string]any{
			"status":          models.ITStatusPaymentReceived,
			"payment_ref":     chargeID,
			"received_amount": amountMinor,
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil // no open stablecoin buy — fall through to MCGP
	}

	var intent models.ITIntent
	if err := s.db.WithContext(ctx).
		First(&intent, "payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).Error; err != nil {
		return false, err
	}

	// Price at the LIVE rate on what actually landed. This is the binding
	// price; the figure quoted at creation was indicative. Pricing on what
	// arrived rather than what was quoted also handles under- and overpayment
	// with no special case — the mirror of the sell leg's received/quoted.
	q, err := s.QuoteStableBuy(ctx, intent.Asset, amountMinor, intent.Currency)
	if err != nil {
		// The money is here and cannot be handed back, so this must NOT error
		// the webhook — that would leave the charge unprocessed and re-driven
		// forever. Park for an operator with the reason recorded. Covers an
		// unusable rate and an underpayment below the provider floor alike:
		// both mean "money landed, a human must decide".
		log.Printf("[it stable buy] ALARM charge=%s landed but could not be priced: %v", chargeID, err)
		if uErr := s.db.WithContext(ctx).Model(&intent).Updates(map[string]any{
			"status":         models.ITStatusAwaitingRate,
			"failure_reason": "could not price at settlement: " + err.Error(),
		}).Error; uErr != nil {
			return false, uErr
		}
		return true, nil
	}

	if err := s.db.WithContext(ctx).Model(&intent).Updates(map[string]any{
		"asset_amount": q.AssetUnits.String(),
		"locked_rate":  q.RatePerUnitMinor.String(),
		"rate_minor":   q.RatePerUnitMinor.String(),
		"rate_kobo":    q.RatePerUnitMinor.String(),
	}).Error; err != nil {
		return false, err
	}

	log.Printf("[it stable buy] charge=%s funded %s %s for user=%s",
		chargeID, q.AssetUnits, intent.Asset, userID)
	return true, nil
}
```

- [ ] **Step 4: Wire the fork into `AutoCreditFromVA`**

In `internal/services/it_fiat_va.go`, immediately after the VA has resolved to a user (after the `resolveFiatVA` call and the `va.UserID` lookup succeeds) and **before** any MCGP pricing or crediting:

```go
	// Fork: a stablecoin purchase awaiting payment claims this charge. If the
	// user has none, fall through to the MCGP credit path below, unchanged.
	claimed, err := s.settleStableBuyFromCharge(ctx, va.UserID, chargeID, amountNGN)
	if err != nil {
		return fmt.Errorf("it va credit: stable buy: %w", err)
	}
	if claimed {
		return nil
	}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/services/ -count=1`
Expected: PASS, including every existing `AutoCreditFromVA` test — if any MCGP test now fails, the fork is capturing charges it should not.

- [ ] **Step 6: Commit**

```bash
git add internal/services/it_stable_buy_settle.go internal/services/it_stable_buy_settle_test.go internal/services/it_fiat_va.go
git commit -m "feat(it): route an incoming charge to an open stablecoin buy"
```

---

### Task 7: The spendable-funds gate and conversion

**Files:**
- Modify: `internal/services/it_stable_buy.go`
- Test: `internal/services/it_stable_buy_convert_test.go`

**Interfaces:**
- Consumes: `s.stableBalances` / `FetchBalances`, `s.stableConverter` / `ConvertBalance`, `s.stableTransfers` / `GetTransfer` (all existing, wired for sell)
- Produces:
  - `func (s *ITService) ConvertStableBuy(ctx context.Context, intentID uuid.UUID) error` — `payment_received`/`awaiting_funds` → `converting`
  - `func (s *ITService) FinalizeBuyConversion(ctx context.Context, intentID uuid.UUID) error` — `converting` → `delivering`
  - `func itBuyConversionRef(intentID uuid.UUID, attempts int) string` — prefix `ITBC-`

Reuse the sell leg's existing converter/transfer-reader interfaces and stubs; do not introduce parallel ones.

- [ ] **Step 1: Write the failing tests**

Create `internal/services/it_stable_buy_convert_test.go` with these cases (mirror the stub types used in `it_stable_convert_test.go` exactly rather than inventing new ones):

```go
package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestConvertStableBuy_WaitsUntilTheNairaIsSpendable(t *testing.T) {
	// The brokerage guarantee: money moves only once Flutterwave shows it
	// actually holds spendable funds. A charge arriving is NOT that — VA
	// credits may sit in collection balance awaiting settlement.
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Updates(map[string]any{
		"status": models.ITStatusPaymentReceived, "received_amount": "16502500",
	})

	// Balance shows nothing spendable yet. There is no separate balances
	// dependency — stableConverter supplies both FetchBalances and
	// ConvertBalance, exactly as the sell leg uses it.
	s.SetStableConverter(&stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 0}},
	}, "merchant_1")

	err := s.ConvertStableBuy(context.Background(), intent.ID)
	if !errors.Is(err, ErrStableCreditNotVisible) {
		t.Fatalf("err = %v, want ErrStableCreditNotVisible", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusAwaitingFunds {
		t.Fatalf("status = %q, want awaiting_funds", got.Status)
	}
}

func TestConvertStableBuy_ConvertsOnceFundsAreSpendable(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Updates(map[string]any{
		"status": models.ITStatusPaymentReceived, "received_amount": "16502500",
	})
	conv := &stubConverter{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 200000}},
		transfer: FWTransfer{ID: "trf_buy_1", Status: "NEW"},
	}
	s.SetStableConverter(conv, "merchant_1")

	if err := s.ConvertStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusConverting {
		t.Fatalf("status = %q, want converting", got.Status)
	}
	if got.ConversionID != "trf_buy_1" {
		t.Fatalf("conversion id = %q", got.ConversionID)
	}
	// Accepted is NOT settled: nothing may be delivered yet.
	if got.DeliveryID != "" {
		t.Fatal("delivery started against an unsettled conversion")
	}
}

func TestFinalizeBuyConversion_OnlySuccessfulAdvances(t *testing.T) {
	for _, tc := range []struct {
		name, fwStatus, wantStatus string
		wantErr                    error
	}{
		{"successful", "SUCCESSFUL", models.ITStatusDelivering, nil},
		{"still pending", "PENDING", models.ITStatusConverting, ErrStableConversionPending},
		{"new is not settled", "NEW", models.ITStatusConverting, ErrStableConversionPending},
		{"failed retries", "FAILED", models.ITStatusAwaitingFunds, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, userID := newStableBuyDBService(t)
			intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
			s.db.Model(intent).Updates(map[string]any{
				"status": models.ITStatusConverting, "conversion_id": "trf_buy_2",
			})
			s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_buy_2", Status: tc.fwStatus}})

			err := s.FinalizeBuyConversion(context.Background(), intent.ID)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got models.ITIntent
			s.db.First(&got, "id = ?", intent.ID)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run "TestConvertStableBuy|TestFinalizeBuyConversion" -count=1`
Expected: FAIL — `undefined: (*ITService).ConvertStableBuy`

- [ ] **Step 3: Implement**

Append to `internal/services/it_stable_buy.go`:

```go
// itBuyConversionRef mints the reference for a buy's NGN→asset conversion.
//
// Same length accounting as itConversionRef: Flutterwave rejects references
// outside 6–42 chars. "ITBC-" (5) + hyphen-less UUID (32) = 37, leaving 5 for
// "-" plus the attempt count.
func itBuyConversionRef(intentID uuid.UUID, attempts int) string {
	return fmt.Sprintf("ITBC-%s-%d", strings.ReplaceAll(intentID.String(), "-", ""), attempts)
}

// ConvertStableBuy converts the user's naira into the stablecoin they bought.
//
// The balance check is the invariant in code: we convert only once Flutterwave
// reports the fiat as SPENDABLE. A charge.completed proves money was received,
// not that it settled into available_balance — and available_balance is what
// ConvertBalance actually draws on. If VA credits turn out to be immediately
// spendable this check passes on the first tick and is invisible; if they wait
// for T+1 it holds the intent correctly. That is the whole point of the state.
func (s *ITService) ConvertStableBuy(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusConverting, models.ITStatusDelivering, models.ITStatusSettled:
		return nil
	case models.ITStatusPaymentReceived, models.ITStatusAwaitingFunds:
	default:
		return ErrITBadState
	}
	if intent.ConversionAttempts >= maxStableConversionAttempts {
		return ErrStableConversionExhausted
	}
	if s.stableConverter == nil {
		return errors.New("it stable buy: converter not configured")
	}

	paid, ok := new(big.Int).SetString(strings.TrimSpace(intent.ReceivedAmount), 10)
	if !ok || paid.Sign() <= 0 {
		return errors.New("it stable buy: intent has no received amount")
	}

	// Read the spendable balance exactly as the sell leg does: FetchBalances
	// reports major units and AvailableBalance is the post-settlement figure
	// ConvertBalance actually draws on.
	balances, err := s.stableConverter.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	var available float64
	found := false
	for _, b := range balances {
		if strings.EqualFold(b.Currency, intent.Currency) {
			available, found = b.AvailableBalance, true
			break
		}
	}
	if !found {
		// Distinct from an ordinary not-yet-settled wait: a missing line can
		// mean the integration itself is not surfacing this currency. Both
		// fail the same way; the log makes them distinguishable.
		log.Printf("[IT] it_stable_balance_line_missing currency=%s intent=%s", intent.Currency, intent.ID)
	}
	neededRat := new(big.Rat).SetFrac(paid, big.NewInt(100))
	needed, _ := neededRat.Float64()
	if !found || available < needed {
		if intent.Status != models.ITStatusAwaitingFunds {
			if err := s.db.WithContext(ctx).Model(intent).
				Update("status", models.ITStatusAwaitingFunds).Error; err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: %s spendable=%v needed=%v",
			ErrStableCreditNotVisible, intent.Currency, available, needed)
	}

	ref := itBuyConversionRef(intent.ID, intent.ConversionAttempts)
	amountMajor, _ := new(big.Rat).SetFrac(paid, big.NewInt(100)).Float64()
	tr, err := s.stableConverter.ConvertBalance(ctx, s.stableMerchantID,
		intent.Currency, intent.Asset,
		FWAmount{Value: amountMajor, AppliesTo: "source_currency"}, ref)
	if err != nil {
		return fmt.Errorf("it stable buy convert: %w", err)
	}

	// Stop at converting. Flutterwave returns 201 {"status":"NEW"} and settles
	// later, so the asset does not exist yet — delivering here would send
	// stablecoin we have not bought.
	return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
		"status":        models.ITStatusConverting,
		"conversion_id": tr.ID,
	}).Error
}

// FinalizeBuyConversion advances a buy once its NGN→asset conversion settles.
//
// FAILED drops back to awaiting_funds rather than a terminal state: the user's
// naira is still in our balance, so retrying is both safe and correct. The
// attempt counter is bumped alongside so the next call mints a fresh
// reference — reusing the old one would hit Flutterwave's idempotency, return
// the same FAILED transfer, and loop forever.
func (s *ITService) FinalizeBuyConversion(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	switch intent.Status {
	case models.ITStatusDelivering, models.ITStatusSettled:
		return nil
	case models.ITStatusConverting:
	default:
		return ErrITBadState
	}
	if intent.ConversionID == "" {
		return errors.New("it stable buy: converting intent has no conversion id")
	}
	if s.stableTransfers == nil {
		return errors.New("it stable buy: no transfer reader configured")
	}

	tr, err := s.stableTransfers.GetTransfer(ctx, intent.ConversionID)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	switch strings.ToUpper(tr.Status) {
	case "SUCCESSFUL":
		return s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusDelivering).Error
	case "FAILED":
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"status":              models.ITStatusAwaitingFunds,
			"failure_reason":      "buy conversion failed: " + tr.ID,
			"conversion_id":       "",
			"conversion_attempts": intent.ConversionAttempts + 1,
		}).Error
	default:
		return fmt.Errorf("%w: conversion %s is %s", ErrStableConversionPending, tr.ID, tr.Status)
	}
}
```

There is deliberately no shared balance helper: the sell leg reads balances inline
(`it_stable_sell.go`, in `ConvertAndQueuePayout`) and this mirrors it rather than
refactoring a live money path as a side effect of adding buy. Keep the
`it_stable_balance_line_missing` log key identical so both legs alert the same way.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run "TestConvertStableBuy|TestFinalizeBuyConversion" -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/services/it_stable_buy.go internal/services/it_stable_buy_convert_test.go
git commit -m "feat(it): gate stablecoin buy conversion on spendable naira"
```

---

### Task 8: Deliver the stablecoin

**Files:**
- Modify: `internal/services/it_stable_buy.go`
- Test: `internal/services/it_stable_buy_deliver_test.go`

**Interfaces:**
- Consumes: `FWDirectTransferRequest`, `FWRecipientCrypto`, `FWAmount` (existing); `DeliveryID`/`DeliveryAttempts` (Task 1)
- Produces:
  - `type stableDeliverer interface { DirectTransfer(ctx context.Context, req FWDirectTransferRequest) (FWTransfer, error) }`
  - `func (s *ITService) SetStableDeliverer(d stableDeliverer)`
  - `func (s *ITService) DeliverStableBuy(ctx context.Context, intentID uuid.UUID) error` — `delivering` → sets `DeliveryID`
  - `func (s *ITService) FinalizeBuyDelivery(ctx context.Context, intentID uuid.UUID) error` — `delivering` → `settled`
  - `func itBuyDeliveryRef(intentID uuid.UUID, attempts int) string` — prefix `ITBD-`
  - `const maxStableDeliveryAttempts = 10`

- [ ] **Step 1: Write the failing tests**

Create `internal/services/it_stable_buy_deliver_test.go`:

```go
package services

import (
	"context"
	"errors"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

type stubDeliverer struct {
	got FWDirectTransferRequest
	id  string
	err error
}

func (d *stubDeliverer) DirectTransfer(_ context.Context, req FWDirectTransferRequest) (FWTransfer, error) {
	d.got = req
	if d.err != nil {
		return FWTransfer{}, d.err
	}
	return FWTransfer{ID: d.id, Status: "NEW"}, nil
}

func deliveringIntent(t *testing.T) (*ITService, *models.ITIntent) {
	t.Helper()
	s, userID := newStableBuyDBService(t)
	intent, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(intent).Update("status", models.ITStatusDelivering)
	s.db.First(intent, "id = ?", intent.ID)
	return s, intent
}

func TestDeliverStableBuy_SendsCryptoToTheUsersAddress(t *testing.T) {
	s, intent := deliveringIntent(t)
	d := &stubDeliverer{id: "trf_deliver_1"}
	s.SetStableDeliverer(d)

	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if d.got.Type != "crypto" {
		t.Fatalf("type = %q, want crypto", d.got.Type)
	}
	c := d.got.PaymentInstruction.Recipient.Crypto
	if c == nil {
		t.Fatal("no crypto recipient set")
	}
	if c.Address != testBuyEVM {
		t.Fatalf("address = %q, want the user's own address", c.Address)
	}
	if c.Network != "POLYGON" {
		t.Fatalf("network = %q, want POLYGON", c.Network)
	}
	// The user is delivered what they bought, in the destination asset.
	if d.got.PaymentInstruction.DestinationCurrency != "USDC" {
		t.Fatalf("destination = %q, want USDC", d.got.PaymentInstruction.DestinationCurrency)
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.DeliveryID != "trf_deliver_1" {
		t.Fatalf("delivery id = %q", got.DeliveryID)
	}
	if got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %q, want delivering (accepted is not settled)", got.Status)
	}
}

func TestDeliverStableBuy_IsIdempotent(t *testing.T) {
	// A second call must not send a second transfer — that would deliver twice.
	s, intent := deliveringIntent(t)
	d := &stubDeliverer{id: "trf_deliver_2"}
	s.SetStableDeliverer(d)

	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("first: %v", err)
	}
	d.id = "trf_deliver_SHOULD_NOT_HAPPEN"
	if err := s.DeliverStableBuy(context.Background(), intent.ID); err != nil {
		t.Fatalf("second: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.DeliveryID != "trf_deliver_2" {
		t.Fatalf("delivery id = %q, want the first transfer", got.DeliveryID)
	}
}

func TestFinalizeBuyDelivery_OnlySuccessfulSettles(t *testing.T) {
	for _, tc := range []struct {
		name, fwStatus, wantStatus string
	}{
		{"successful", "SUCCESSFUL", models.ITStatusSettled},
		{"pending holds", "PENDING", models.ITStatusDelivering},
		{"failed retries", "FAILED", models.ITStatusDelivering},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, intent := deliveringIntent(t)
			s.db.Model(intent).Update("delivery_id", "trf_deliver_3")
			s.SetStableTransfers(&stubTransfers{tr: FWTransfer{ID: "trf_deliver_3", Status: tc.fwStatus}})

			_ = s.FinalizeBuyDelivery(context.Background(), intent.ID)

			var got models.ITIntent
			s.db.First(&got, "id = ?", intent.ID)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.fwStatus == "FAILED" && got.DeliveryID != "" {
				t.Fatal("failed delivery kept its id; the retry would reuse the reference")
			}
		})
	}
}

func TestDeliverStableBuy_StopsAfterMaxAttempts(t *testing.T) {
	// Bounded retries. The intent stays non-terminal so it remains visible in
	// the stuck list — the money is ours to recover by hand.
	s, intent := deliveringIntent(t)
	s.db.Model(intent).Update("delivery_attempts", maxStableDeliveryAttempts)
	s.SetStableDeliverer(&stubDeliverer{id: "nope"})

	if err := s.DeliverStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrStableDeliveryExhausted) {
		t.Fatalf("err = %v, want ErrStableDeliveryExhausted", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/services/ -run "TestDeliverStableBuy|TestFinalizeBuyDelivery" -count=1`
Expected: FAIL — `undefined: (*ITService).DeliverStableBuy`

- [ ] **Step 3: Implement**

Append to `internal/services/it_stable_buy.go`:

```go
// maxStableDeliveryAttempts caps outbound delivery retries, mirroring
// maxStableConversionAttempts. The intent is deliberately left non-terminal
// when exhausted: the stuck list filters to non-terminal statuses, so marking
// it terminal would hide the intent an operator most needs to see.
const maxStableDeliveryAttempts = 10

// ErrStableDeliveryExhausted means delivery failed too many times and needs an
// operator. The stablecoin is in our Flutterwave balance — stuck, not lost.
var ErrStableDeliveryExhausted = errors.New("it stable buy: delivery attempts exhausted; needs operator review")

// stableDeliverer is the slice of the Flutterwave client used to pay a user
// their stablecoin.
type stableDeliverer interface {
	DirectTransfer(ctx context.Context, req FWDirectTransferRequest) (FWTransfer, error)
}

// SetStableDeliverer wires the outbound crypto payout path.
func (s *ITService) SetStableDeliverer(d stableDeliverer) { s.stableDeliverer = d }

// itBuyDeliveryRef mints the reference for a buy's outbound crypto transfer.
// "ITBD-" (5) + hyphen-less UUID (32) = 37, inside Flutterwave's 6–42 range.
func itBuyDeliveryRef(intentID uuid.UUID, attempts int) string {
	return fmt.Sprintf("ITBD-%s-%d", strings.ReplaceAll(intentID.String(), "-", ""), attempts)
}

// DeliverStableBuy sends the purchased stablecoin to the user's own address.
//
// This is the one irreversible step in the flow, which is why the (chain,
// asset, address) triple was validated at intent creation and the address was
// never typed by the user. Reaching here means the conversion already settled
// SUCCESSFUL, so the asset provably exists in our balance.
func (s *ITService) DeliverStableBuy(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusSettled {
		return nil
	}
	if intent.Status != models.ITStatusDelivering {
		return ErrITBadState
	}
	// Already sent — never send twice.
	if intent.DeliveryID != "" {
		return nil
	}
	if intent.DeliveryAttempts >= maxStableDeliveryAttempts {
		return ErrStableDeliveryExhausted
	}
	if s.stableDeliverer == nil {
		return errors.New("it stable buy: no deliverer configured")
	}

	// Re-validate immediately before the irreversible call. The matrix is
	// config-adjacent and an intent may have been created under an older one.
	if err := ValidateBuyDelivery(intent.DestChain, intent.Asset, intent.DestAddress); err != nil {
		return err
	}

	units, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || units.Sign() <= 0 {
		return errors.New("it stable buy: intent has no asset amount")
	}
	amountMajor, _ := new(big.Rat).SetFrac(units,
		new(big.Int).Exp(big.NewInt(10), big.NewInt(StableAssetDecimals), nil)).Float64()

	ref := itBuyDeliveryRef(intent.ID, intent.DeliveryAttempts)
	tr, err := s.stableDeliverer.DirectTransfer(ctx, FWDirectTransferRequest{
		Action:    "instant",
		Type:      "crypto",
		Reference: ref,
		Narration: "TSA Connect stablecoin purchase",
		PaymentInstruction: FWPaymentInstruction{
			SourceCurrency:      intent.Asset,
			DestinationCurrency: intent.Asset,
			Amount:              FWAmount{Value: amountMajor, AppliesTo: "source_currency"},
			Recipient: FWTransferRecipient{
				Crypto: &FWRecipientCrypto{
					Network: intent.DestChain,
					Address: intent.DestAddress,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("it stable buy deliver: %w", err)
	}
	return s.db.WithContext(ctx).Model(intent).
		Update("delivery_id", tr.ID).Error
}

// FinalizeBuyDelivery settles a buy once the outbound transfer completes.
func (s *ITService) FinalizeBuyDelivery(ctx context.Context, intentID uuid.UUID) error {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if intent.Status == models.ITStatusSettled {
		return nil
	}
	if intent.Status != models.ITStatusDelivering || intent.DeliveryID == "" {
		return ErrITBadState
	}
	if s.stableTransfers == nil {
		return errors.New("it stable buy: no transfer reader configured")
	}

	tr, err := s.stableTransfers.GetTransfer(ctx, intent.DeliveryID)
	if err != nil {
		return fmt.Errorf("it stable buy: %w", err)
	}
	switch strings.ToUpper(tr.Status) {
	case "SUCCESSFUL":
		return s.db.WithContext(ctx).Model(intent).
			Update("status", models.ITStatusSettled).Error
	case "FAILED":
		// Clear the id and bump the counter so the retry mints a fresh
		// reference instead of colliding with Flutterwave's idempotency on the
		// transfer that just failed. Stays in `delivering`: the asset is in our
		// balance and the user has not been paid.
		return s.db.WithContext(ctx).Model(intent).Updates(map[string]any{
			"failure_reason":    "delivery failed: " + tr.ID,
			"delivery_id":       "",
			"delivery_attempts": intent.DeliveryAttempts + 1,
		}).Error
	default:
		return fmt.Errorf("%w: delivery %s is %s", ErrStableConversionPending, tr.ID, tr.Status)
	}
}
```

Add `stableDeliverer stableDeliverer` to the `ITService` struct beside the other stable fields.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/services/ -run "TestDeliverStableBuy|TestFinalizeBuyDelivery" -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/services/it_stable_buy.go internal/services/it_stable_buy_deliver_test.go
git commit -m "feat(it): deliver purchased stablecoin to the user's address"
```

---

### Task 9: Watcher and wiring

**Files:**
- Create: `internal/jobs/stable_buy_watcher.go`
- Test: `internal/jobs/stable_buy_watcher_test.go`
- Modify: `internal/services/it_stable_buy.go` (add `PendingStableBuys`)
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: Tasks 7 and 8
- Produces:
  - `func (s *ITService) PendingStableBuys(ctx context.Context) (received, awaiting, converting, delivering []uuid.UUID, err error)`
  - `func jobs.NewStableBuyWatcher(driver stableBuyDriver, interval time.Duration) *StableBuyWatcher`

- [ ] **Step 1: Write the failing test**

Create `internal/jobs/stable_buy_watcher_test.go`, mirroring `stable_sell_watcher_test.go`'s fake-driver shape:

```go
package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeBuyDriver struct {
	received, awaiting, converting, delivering []uuid.UUID
	convertCalls, finalizeConvCalls           []uuid.UUID
	deliverCalls, finalizeDelCalls            []uuid.UUID
	convertErr                                error
}

func (f *fakeBuyDriver) PendingStableBuys(context.Context) ([]uuid.UUID, []uuid.UUID, []uuid.UUID, []uuid.UUID, error) {
	return f.received, f.awaiting, f.converting, f.delivering, nil
}
func (f *fakeBuyDriver) ConvertStableBuy(_ context.Context, id uuid.UUID) error {
	f.convertCalls = append(f.convertCalls, id)
	return f.convertErr
}
func (f *fakeBuyDriver) FinalizeBuyConversion(_ context.Context, id uuid.UUID) error {
	f.finalizeConvCalls = append(f.finalizeConvCalls, id)
	return nil
}
func (f *fakeBuyDriver) DeliverStableBuy(_ context.Context, id uuid.UUID) error {
	f.deliverCalls = append(f.deliverCalls, id)
	return nil
}
func (f *fakeBuyDriver) FinalizeBuyDelivery(_ context.Context, id uuid.UUID) error {
	f.finalizeDelCalls = append(f.finalizeDelCalls, id)
	return nil
}

func TestStableBuyWatcher_AdvancesEveryBucket(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := &fakeBuyDriver{received: []uuid.UUID{a}, awaiting: []uuid.UUID{b}, converting: []uuid.UUID{c}, delivering: []uuid.UUID{d}}

	if err := NewStableBuyWatcher(f, 0).Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.convertCalls) != 2 { // received AND awaiting both feed conversion
		t.Fatalf("convert calls = %v, want a and b", f.convertCalls)
	}
	if len(f.finalizeConvCalls) != 1 || f.finalizeConvCalls[0] != c {
		t.Fatalf("finalize conversion calls = %v", f.finalizeConvCalls)
	}
	// A delivering intent is both sent and polled on the same tick; DeliverStableBuy
	// is a no-op once DeliveryID is set, so this is safe and saves a cycle.
	if len(f.deliverCalls) != 1 || len(f.finalizeDelCalls) != 1 {
		t.Fatalf("deliver=%v finalize=%v", f.deliverCalls, f.finalizeDelCalls)
	}
}

func TestStableBuyWatcher_OneFailureDoesNotStopTheTick(t *testing.T) {
	// One user's stalled provider call must not block every other user.
	a, b := uuid.New(), uuid.New()
	f := &fakeBuyDriver{received: []uuid.UUID{a, b}, convertErr: errors.New("boom")}

	if err := NewStableBuyWatcher(f, 0).Run(context.Background()); err != nil {
		t.Fatalf("watcher returned an error instead of swallowing it: %v", err)
	}
	if len(f.convertCalls) != 2 {
		t.Fatalf("stopped after the first failure: %v", f.convertCalls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/jobs/ -run TestStableBuyWatcher -count=1`
Expected: FAIL — `undefined: NewStableBuyWatcher`

- [ ] **Step 3: Implement `PendingStableBuys`**

Append to `internal/services/it_stable_buy.go`:

```go
// PendingStableBuys lists brokered buy intents that can still advance.
func (s *ITService) PendingStableBuys(ctx context.Context) (received, awaiting, converting, delivering []uuid.UUID, err error) {
	type row struct {
		ID     uuid.UUID
		Status string
	}
	var rows []row
	err = s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Select("id", "status").
		Where("direction = ? AND asset IN ? AND status IN ?",
			models.ITDirectionBuy,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT},
			[]string{
				models.ITStatusPaymentReceived,
				models.ITStatusAwaitingFunds,
				models.ITStatusConverting,
				models.ITStatusDelivering,
			}).
		Scan(&rows).Error
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for _, r := range rows {
		switch r.Status {
		case models.ITStatusPaymentReceived:
			received = append(received, r.ID)
		case models.ITStatusAwaitingFunds:
			awaiting = append(awaiting, r.ID)
		case models.ITStatusConverting:
			converting = append(converting, r.ID)
		case models.ITStatusDelivering:
			delivering = append(delivering, r.ID)
		}
	}
	return received, awaiting, converting, delivering, nil
}
```

- [ ] **Step 4: Implement the watcher**

Create `internal/jobs/stable_buy_watcher.go`:

```go
package jobs

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
)

type stableBuyDriver interface {
	PendingStableBuys(ctx context.Context) (received, awaiting, converting, delivering []uuid.UUID, err error)
	ConvertStableBuy(ctx context.Context, id uuid.UUID) error
	FinalizeBuyConversion(ctx context.Context, id uuid.UUID) error
	DeliverStableBuy(ctx context.Context, id uuid.UUID) error
	FinalizeBuyDelivery(ctx context.Context, id uuid.UUID) error
}

// StableBuyWatcher drives the brokered stablecoin buy state machine.
//
// Polling is the only signal available: Flutterwave publishes no webhook for a
// conversion settling, for a crypto transfer completing, or for collected
// naira becoming spendable. Every step it calls is idempotent, so a redelivered
// tick is harmless.
type StableBuyWatcher struct {
	driver   stableBuyDriver
	interval time.Duration
}

func NewStableBuyWatcher(driver stableBuyDriver, interval time.Duration) *StableBuyWatcher {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &StableBuyWatcher{driver: driver, interval: interval}
}

func (w *StableBuyWatcher) Name() string            { return "it-stable-buy-watcher" }
func (w *StableBuyWatcher) Interval() time.Duration { return w.interval }

func (w *StableBuyWatcher) Run(ctx context.Context) error {
	received, awaiting, converting, delivering, err := w.driver.PendingStableBuys(ctx)
	if err != nil {
		return err
	}

	// Errors are logged and swallowed per intent, never returned: one user's
	// unsettled conversion must not stop every other user from advancing.
	for _, id := range append(received, awaiting...) {
		if err := w.driver.ConvertStableBuy(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] convert intent=%s: %v", id, err)
		}
	}
	for _, id := range converting {
		if err := w.driver.FinalizeBuyConversion(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] finalize conversion intent=%s: %v", id, err)
		}
	}
	for _, id := range delivering {
		// Send first (a no-op once DeliveryID is set), then poll — so a newly
		// converted intent is dispatched and checked on the same tick.
		if err := w.driver.DeliverStableBuy(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] deliver intent=%s: %v", id, err)
		}
		if err := w.driver.FinalizeBuyDelivery(ctx, id); err != nil {
			log.Printf("[it-stable-buy-watcher] finalize delivery intent=%s: %v", id, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Wire into `cmd/server/main.go`**

Inside the existing stablecoin-ramp block, immediately after `scheduler.Register(jobs.NewStableSellWatcher(...))`:

```go
			itService.SetStableDeliverer(fwClient)
			scheduler.Register(jobs.NewStableBuyWatcher(itService, 30*time.Second))
```

The buy leg rides the same dormant-by-default gate as sell: if the ramp is not configured, neither watcher is registered.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/jobs/ ./internal/services/ -count=1 && go build ./...`
Expected: PASS, build clean

- [ ] **Step 7: Commit**

```bash
git add internal/jobs/stable_buy_watcher.go internal/jobs/stable_buy_watcher_test.go internal/services/it_stable_buy.go cmd/server/main.go
git commit -m "feat(it): drive the stablecoin buy state machine with a watcher"
```

---

### Task 10: HTTP surface

**Files:**
- Modify: `internal/handlers/it_handler.go`
- Modify: `internal/routes/routes.go`
- Test: `internal/handlers/it_stable_buy_handler_test.go`

**Interfaces:**
- Consumes: `QuoteStableBuy` (Task 4), `CreateStableBuyIntent` (Task 5), `DeliverableChains` (Task 2)
- Produces: `POST /api/it/stable/buy/quote`, `POST /api/it/stable/buy`, `GET /api/it/stable/buy/chains`

- [ ] **Step 1: Write the failing test**

Create `internal/handlers/it_stable_buy_handler_test.go`, following the shape of the existing `it_stable_code_test.go`:

```go
package handlers

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestStableCodeFor_CoversBuyErrors(t *testing.T) {
	// An unmapped error reaches the app as an empty code, which the client
	// cannot branch on — it becomes a generic failure on a money path.
	for _, tc := range []struct {
		err  error
		want string
	}{
		{services.ErrStableBuyBelowMinimum, "stable_buy_below_minimum"},
		{services.ErrStableBuyAlreadyOpen, "stable_buy_already_open"},
		{services.ErrStableBuyNoVA, "stable_buy_no_va"},
		{services.ErrUnsupportedChainAsset, "stable_unsupported_chain_asset"},
		{services.ErrStableDeliveryExhausted, "stable_delivery_exhausted"},
	} {
		if got := stableCodeFor(tc.err); got != tc.want {
			t.Fatalf("code for %v = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestStableStatusFor_BuyInputErrorsAre400(t *testing.T) {
	for _, err := range []error{
		services.ErrStableBuyBelowMinimum,
		services.ErrStableBuyAlreadyOpen,
		services.ErrStableBuyNoVA,
		services.ErrUnsupportedChainAsset,
	} {
		if got := stableStatusFor(err); got != 400 {
			t.Fatalf("status for %v = %d, want 400", err, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run "TestStableCodeFor_CoversBuyErrors|TestStableStatusFor_Buy" -count=1`
Expected: FAIL — codes return `""`

- [ ] **Step 3: Extend the error mapping**

In `internal/handlers/it_handler.go`, add to `stableCodeFor`'s switch, before `default`:

```go
	case errors.Is(err, services.ErrStableBuyBelowMinimum):
		return "stable_buy_below_minimum"
	case errors.Is(err, services.ErrStableBuyAlreadyOpen):
		return "stable_buy_already_open"
	case errors.Is(err, services.ErrStableBuyNoVA):
		return "stable_buy_no_va"
	case errors.Is(err, services.ErrUnsupportedChainAsset):
		return "stable_unsupported_chain_asset"
	case errors.Is(err, services.ErrStableDeliveryExhausted):
		return "stable_delivery_exhausted"
```

And to `stableStatusFor`'s 400 group:

```go
		errors.Is(err, services.ErrStableBuyBelowMinimum),
		errors.Is(err, services.ErrStableBuyAlreadyOpen),
		errors.Is(err, services.ErrStableBuyNoVA),
		errors.Is(err, services.ErrUnsupportedChainAsset),
```

`ErrStableDeliveryExhausted` belongs in the 5xx group beside `ErrStableQuoteUnavailable`: it is our failure, not the caller's.

- [ ] **Step 4: Add the handlers**

Append to `internal/handlers/it_handler.go`:

```go
type stableBuyQuoteRequest struct {
	Asset    string `json:"asset" binding:"required"`
	Amount   string `json:"amount" binding:"required"` // fiat MINOR units
	Currency string `json:"currency" binding:"required"`
}

type stableBuyRequest struct {
	stableBuyQuoteRequest
	DestChain string `json:"destChain" binding:"required"`
}

// POST /api/it/stable/buy/quote — indicative price for a stablecoin purchase.
func (h *ITHandler) PostStableBuyQuote(c *gin.Context) {
	var req stableBuyQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	q, err := h.it.QuoteStableBuy(c.Request.Context(), req.Asset, req.Amount, req.Currency)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Quote", gin.H{
		"asset":       q.Asset,
		"currency":    q.Currency,
		"fiatMinor":   q.FiatMinor.String(),
		"grossUnits":  q.GrossAssetUnits.String(),
		"assetUnits":  q.AssetUnits.String(),
		"ratePerUnit": q.RatePerUnitMinor.String(),
		"expiresAt":   q.ExpiresAt,
		// Indicative, not locked: the binding price is struck when the naira
		// lands. Say so in the payload so the app cannot present it as firm.
		"indicative": true,
	})
}

// POST /api/it/stable/buy — opens a purchase and returns where to pay.
func (h *ITHandler) PostStableBuy(c *gin.Context) {
	var req stableBuyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if !h.requireITAgreement(c, user.ID) {
		return
	}
	ctx := c.Request.Context()

	q, err := h.it.QuoteStableBuy(ctx, req.Asset, req.Amount, req.Currency)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	// The address is OURS to supply, never the caller's to send: it is read
	// from the user's wallet. There is deliberately no address field on the
	// request body.
	addr, err := h.it.UserDeliveryAddress(ctx, user.ID, req.DestChain)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	intent, payInto, err := h.it.CreateStableBuyIntent(ctx, user.ID, q, req.DestChain, addr)
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Purchase created", gin.H{
		"intent": intent, "payInto": payInto,
	})
}

// GET /api/it/stable/buy/chains?asset=USDC — chains this user can receive on.
func (h *ITHandler) GetStableBuyChains(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	chains, err := h.it.DeliverableChainsForUser(c.Request.Context(), user.ID, c.Query("asset"))
	if err != nil {
		utils.ErrorResponseWithCode(c, stableStatusFor(err), err.Error(), stableCodeFor(err))
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Chains", gin.H{"chains": chains})
}
```

Add these to `internal/services/it_stable_buy.go`. Note `Wallet.Addresses` is a jsonb
**list** of `models.WalletAddrEntry`, not a map — the map that `DeliverableChains`
(Task 2) consumes is built here:

```go
// ErrStableBuyNoWallet means the user has no wallet to receive into.
var ErrStableBuyNoWallet = errors.New("it stable buy: user has no wallet")

// userDeliveryAddresses maps CHAIN -> the user's own address.
//
// External and inactive entries are excluded deliberately. An imported
// watch-only address is one the user may not hold the keys for, and delivery
// is irreversible — so it must never be a payout target, even though it sits
// in the same list.
func (s *ITService) userDeliveryAddresses(ctx context.Context, userID uuid.UUID) (map[string]string, error) {
	var w models.Wallet
	if err := s.db.WithContext(ctx).First(&w, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrStableBuyNoWallet
		}
		return nil, err
	}
	var entries []models.WalletAddrEntry
	if len(w.Addresses) > 0 {
		if err := json.Unmarshal(w.Addresses, &entries); err != nil {
			return nil, fmt.Errorf("it stable buy: decode wallet addresses: %w", err)
		}
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsExternal || !e.IsActive {
			continue
		}
		if addr := strings.TrimSpace(e.Address); addr != "" {
			out[strings.ToUpper(strings.TrimSpace(e.Blockchain))] = addr
		}
	}
	return out, nil
}

// UserDeliveryAddress returns the user's own address on `chain`.
//
// The address is never accepted from the request body. A typed address is the
// single way this flow could lose funds irrecoverably, so there is no code
// path that reads one from the caller.
func (s *ITService) UserDeliveryAddress(ctx context.Context, userID uuid.UUID, chain string) (string, error) {
	chain = strings.ToUpper(strings.TrimSpace(chain))
	addrs, err := s.userDeliveryAddresses(ctx, userID)
	if err != nil {
		return "", err
	}
	addr := addrs[chain]
	if addr == "" {
		return "", fmt.Errorf("%w: you have no %s address to receive on", ErrBadDeliveryAddress, chain)
	}
	return addr, nil
}

// DeliverableChainsForUser lists the chains this user can be paid `asset` on.
func (s *ITService) DeliverableChainsForUser(ctx context.Context, userID uuid.UUID, asset string) ([]string, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if !IsStableAsset(asset) {
		return nil, fmt.Errorf("%w: got %q", ErrStableNotAnAsset, asset)
	}
	addrs, err := s.userDeliveryAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}
	return DeliverableChains(asset, addrs), nil
}
```

Add `"encoding/json"` to the file's imports, and map `ErrStableBuyNoWallet` in
`stableCodeFor` (`"stable_buy_no_wallet"`) and in `stableStatusFor`'s 400 group.

- [ ] **Step 5: Register the routes**

In `internal/routes/routes.go`, in the `it` group beside the existing stable routes:

```go
			it.POST("/stable/buy/quote", ith.PostStableBuyQuote)
			it.POST("/stable/buy", itIdem, ith.PostStableBuy)
			it.GET("/stable/buy/chains", ith.GetStableBuyChains)
```

`itIdem` on the create call is required for the same reason as sell: a retried create must not mint a second purchase.

- [ ] **Step 6: Run the full suite**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: build clean, vet clean, all packages pass

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/it_handler.go internal/handlers/it_stable_buy_handler_test.go internal/routes/routes.go internal/services/it_stable_buy.go
git commit -m "feat(it): expose the stablecoin buy endpoints"
```

---

### Task 11: Operator visibility

**Files:**
- Modify: `internal/services/it_stable_sell.go` (extend `ListStuckStableSells`) or add to `it_stable_buy.go`
- Modify: `internal/handlers/it_handler.go`
- Test: `internal/services/it_stable_buy_stuck_test.go`

**Interfaces:**
- Consumes: buy statuses (Task 1)
- Produces: `func (s *ITService) ListStuckStableBuys(ctx context.Context, olderThan time.Duration) ([]StuckStableBuy, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/services/it_stable_buy_stuck_test.go`:

```go
package services

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestListStuckStableBuys(t *testing.T) {
	s, userID := newStableBuyDBService(t)

	fresh, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(fresh).Update("status", models.ITStatusAwaitingFunds)

	stuck, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	s.db.Model(stuck).Updates(map[string]any{
		"status":     models.ITStatusConverting,
		"updated_at": time.Now().Add(-2 * time.Hour),
	})

	got, err := s.ListStuckStableBuys(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != stuck.ID {
		t.Fatalf("got %+v, want only the stale intent", got)
	}
}

func TestListStuckStableBuys_IncludesEveryNonTerminalState(t *testing.T) {
	// A state missing here is an intent holding user funds that no operator
	// ever sees.
	s, userID := newStableBuyDBService(t)
	for _, st := range []string{
		models.ITStatusPaymentReceived,
		models.ITStatusAwaitingFunds,
		models.ITStatusConverting,
		models.ITStatusDelivering,
		// Parked by settleStableBuyFromCharge when the money landed but could
		// not be priced. This is the state that MOST needs an operator, so it
		// must never be missing from the stuck list.
		models.ITStatusAwaitingRate,
	} {
		i, _, _ := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
		s.db.Model(i).Updates(map[string]any{"status": st, "updated_at": time.Now().Add(-2 * time.Hour)})
		s.db.Model(i).Update("payment_ref", string(st)) // free the pending_payment slot
	}
	got, err := s.ListStuckStableBuys(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d stuck intents, want 5 — a non-terminal state is unmonitored", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/services/ -run TestListStuckStableBuys -count=1`
Expected: FAIL — `undefined: ListStuckStableBuys`

- [ ] **Step 3: Implement**

Append to `internal/services/it_stable_buy.go`, mirroring `StuckStableSell`'s struct and query shape:

```go
// StuckStableBuy is a buy holding user funds that has not advanced.
type StuckStableBuy struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"userId"`
	Status      string    `json:"status"`
	Asset       string    `json:"asset"`
	Currency    string    `json:"currency"`
	DestChain   string    `json:"destChain"`
	AmountMinor string    `json:"amountMinor"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ListStuckStableBuys surfaces buys stalled longer than olderThan.
//
// Every non-terminal state is included, because each one is an intent where we
// are holding either the user's naira or their stablecoin. pending_payment is
// excluded: nothing has been paid, so there is nothing at risk.
func (s *ITService) ListStuckStableBuys(ctx context.Context, olderThan time.Duration) ([]StuckStableBuy, error) {
	var out []StuckStableBuy
	err := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Select("id", "user_id", "status", "asset", "currency", "dest_chain",
			"received_amount AS amount_minor", "updated_at").
		Where("direction = ? AND asset IN ? AND status IN ? AND updated_at < ?",
			models.ITDirectionBuy,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT},
			[]string{
				models.ITStatusPaymentReceived,
				models.ITStatusAwaitingFunds,
				models.ITStatusConverting,
				models.ITStatusDelivering,
				// Money landed but unpriced — parked for a human. The watcher
				// deliberately does NOT auto-advance these, so the stuck list
				// is the only way anyone learns they exist.
				models.ITStatusAwaitingRate,
			},
			time.Now().Add(-olderThan)).
		Scan(&out).Error
	return out, err
}
```

- [ ] **Step 4: Expose it**

Add a handler mirroring `GetStuckStableSells` and register it beside the existing admin route:

```go
			adminIT.GET("/stable/buy/stuck", ith.GetStuckStableBuys)
```

- [ ] **Step 5: Run the full suite**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: build clean, vet clean, all packages pass

- [ ] **Step 6: Commit**

```bash
git add internal/services/it_stable_buy.go internal/services/it_stable_buy_stuck_test.go internal/handlers/it_handler.go internal/routes/routes.go
git commit -m "feat(it): surface stuck stablecoin buys to operators"
```

---

## Launch gates (not code)

These belong to the operator, not the implementer, and none is closed by this plan:

1. **`type:"crypto"` has never been exercised.** Before enabling buy, make one small real delivery on **each** offered chain. The request shape is right; that it works is unverified.
2. **Conversion has never reached SUCCESSFUL.** Inherited from Phase A — the sandbox does not settle wallet conversions at all. Only a production canary closes it.
3. **Measure time in `awaiting_funds` during the canary.** That number answers whether VA credits are immediately spendable, and therefore whether buy is a minutes-long or next-day product.
4. **`FLUTTERWAVE_BASE_URL` must be set in production.** Unset routes all Flutterwave traffic, including live NGN payouts, to sandbox.
