# P2P Multi-Chain — Plan 5: CreateAd/ListAds chainId + Asset-Curation

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development / executing-plans. Checkbox steps.

**Goal:** Let an ad be created on a chosen chain (validated: chain must be P2P-enabled and the token allowed on it) and let the marketplace filter ads by chain — so Sonic and BSC ads don't intermix and BSC ads can't be created before BSC is deployed.

**Architecture:** `chainId` flows `createAdRequest → CreateAdParams → CreateAd`, defaulting to Sonic (`s.defaultChainID`). `CreateAd` rejects a chain with no configured escrow (`registry.Resolve` errors) and a token not allowed on that chain (`registry.AssetAllowed`). `AdFilter.ChainID` + a `chainId` query param scope `ListAds`. The per-chain asset allow-list lives in config (`P2PChainConfig.Assets`) and is carried into the registry.

**Transition note (important):** Plan 1 set Sonic's allow-list to `["MCGP"]` (the §4.5 end-state). Enforcing that *now* would break live Sonic stablecoin P2P before BSC exists. So this plan sets **Sonic = `["USDT","USDC","MCGP"]`** (current reality) and **BSC = `["USDT","USDC"]`** (rejects MCGP-on-BSC, which is correct + safe). The end-state narrowing (Sonic→`["MCGP"]`) is a config change applied at BSC launch — documented in the Plan 6 hand-off.

**Tech Stack:** Go 1.24. `config.go`, `p2p_escrow_registry.go`, `p2p_trade_service.go`, `p2p_handler.go` (+ tests).

**Depends on:** Plans 1–4.

---

## Task 1: Config — Sonic allow-list = all three tokens (transition)

**Files:** `internal/config/config.go` (the Sonic entry in `Load()`'s `P2PChains` assembly).

- [ ] **Step 1 — change + comment.** In `Load()`, the Sonic `P2PChainConfig` currently has `Assets: []string{"MCGP"}`. Change to:
```go
			// Transition: Sonic serves all three today. At BSC launch, narrow this
			// to []string{"MCGP"} (stablecoin P2P moves to BSC) — spec §4.5.
			Assets: []string{"USDT", "USDC", "MCGP"},
```
- [ ] **Step 2 — update the Plan-1 config test.** In `config_test.go`, `TestLoad_SonicAlways_BSCDormantByDefault` asserts `sonic.Assets == []string{"MCGP"}`. Change the expectation to `[]string{"USDT", "USDC", "MCGP"}`.
- [ ] **Step 3 — run.** `go test ./internal/config/ -count=1` PASS. `go build ./...` clean.
- [ ] **Step 4 — commit.** `git commit -am "fix(config): Sonic P2P serves all 3 tokens in transition (narrow at BSC launch)"`

---

## Task 2: Registry asset allow-list

**Files:** `internal/services/p2p_escrow_registry.go` (+ test).

- [ ] **Step 1 — failing test.** Append to `p2p_escrow_registry_test.go`:
```go
func TestP2PEscrowRegistry_AssetAllowed(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"), eip712Version: "1", domainChainID: 146, assets: []string{"USDT", "USDC", "MCGP"}},
		56:  {service: NewP2PEscrowServiceForChain(nil, nil, "0xBSC"), eip712Version: "2", domainChainID: 56, assets: []string{"USDT", "USDC"}},
	}}
	if !reg.AssetAllowed(146, "MCGP") {
		t.Errorf("MCGP must be allowed on Sonic")
	}
	if reg.AssetAllowed(56, "MCGP") {
		t.Errorf("MCGP must NOT be allowed on BSC")
	}
	if !reg.AssetAllowed(56, "usdt") { // case-insensitive
		t.Errorf("USDT must be allowed on BSC (case-insensitive)")
	}
	if reg.AssetAllowed(999, "USDT") { // unconfigured chain
		t.Errorf("no chain → not allowed")
	}
}
```
- [ ] **Step 2 — run, expect fail** (`assets` field + `AssetAllowed` missing).
- [ ] **Step 3 — implement.** Add `assets []string` to `chainEscrow`. Add:
```go
// AssetAllowed reports whether `symbol` may be traded on chainID per the
// per-chain curation. An empty allow-list means "allow all" (used by the
// test/ops map constructor). An unconfigured chain returns false.
func (r *P2PEscrowRegistry) AssetAllowed(chainID int64, symbol string) bool {
	if r == nil || r.byChain == nil {
		return false
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return false
	}
	if len(e.assets) == 0 {
		return true
	}
	sym := strings.ToUpper(symbol)
	for _, a := range e.assets {
		if strings.ToUpper(a) == sym {
			return true
		}
	}
	return false
}
```
(add `"strings"` import if missing.) In `NewP2PEscrowRegistry`, populate `assets: cc.Assets`. Leave `NewP2PEscrowRegistryFromMap` with `assets` nil (→ allow-all, so existing service tests that create various tokens on chain 146 still pass).
- [ ] **Step 4 — run, expect pass.** `go test ./internal/services/ -run 'P2PEscrowRegistry' -v` PASS; `go build ./...` clean.
- [ ] **Step 5 — commit.** `git commit -am "feat(p2p): per-chain asset allow-list on the escrow registry"`

---

## Task 3: chainId on CreateAd + ListAds

**Files:** `internal/services/p2p_trade_service.go` (CreateAdParams, CreateAd, AdFilter, ListAds), `internal/handlers/p2p_handler.go` (createAdRequest, CreateAd handler, ListAds handler) (+ tests).

- [ ] **Step 1 — types.** Add `ChainID int64` to `CreateAdParams` and `AdFilter`. Add `ChainID int64 \`json:"chainId"\`` to `createAdRequest`.
- [ ] **Step 2 — CreateAd service.** At the top of `CreateAd`, resolve the chain:
```go
	chainID := params.ChainID
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	// Chain must be P2P-enabled (escrow configured) and the token allowed on it.
	if _, _, _, err := s.resolve(chainID); err != nil {
		return nil, fmt.Errorf("%w: chain %d not available for P2P", ErrInvalidParams, chainID)
	}
	if !s.escrows.AssetAllowed(chainID, params.Token) {
		return nil, fmt.Errorf("%w: %s is not tradeable on chain %d", ErrInvalidParams, params.Token, chainID)
	}
```
Use `chainID` (not `s.defaultChainID`) for the ad's `ChainID` field and for the voucher-domain resolve in CreateAd. (`createBuyAd` similarly uses `chainID`.) Confirm `ErrInvalidParams` is the right sentinel (it's used elsewhere in this file for 400s).
- [ ] **Step 3 — handler.** In the `CreateAd` handler, pass `ChainID: req.ChainID` into `CreateAdParams`. In `ListAds`, parse `chainId`:
```go
	if cid := c.Query("chainId"); cid != "" {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			f.ChainID = n
		}
	}
```
- [ ] **Step 4 — ListAds service query.** In `ListAds`, add `if f.ChainID != 0 { q = q.Where("chain_id = ?", f.ChainID) }` alongside the other filters.
- [ ] **Step 5 — tests.** In `p2p_trade_service_test.go` add:
  - `TestCreateAd_RejectsUnconfiguredChain`: build the service with `newService` (registry chain 146 only), call `CreateAd` with `CreateAdParams{ChainID: 56, Token:"USDT", ...}` → expect error (chain not available). (Reuse an existing CreateAd-happy-path test for the param shape.)
  - `TestCreateAd_RejectsCuratedAssetMismatch`: build a service whose registry (use `NewP2PEscrowRegistryFromMap` won't enforce — instead construct a registry literal with `assets`) has chain 56 allowing only USDT/USDC; `CreateAd` with `{ChainID:56, Token:"MCGP"}` → expect error. (If wiring a curated registry into `newService` is heavy, assert at the `AssetAllowed` level in Task 2 and keep this test minimal/at the service boundary only if cheap; do NOT contort the fixtures.)
  - Existing CreateAd tests must still pass (they use chain-146 ads via the allow-all FromMap registry).
- [ ] **Step 6 — run + verify.** `go test ./internal/services/ -count=1` PASS; `go test ./internal/handlers/ -run P2P -count=1` P2P tests PASS; `go build ./... && go vet ./internal/services/ ./internal/handlers/` clean.
- [ ] **Step 7 — commit.** `git commit -am "feat(p2p): CreateAd accepts chainId (validated) + ListAds chainId filter"`

---

## Self-review (completed)
- **Spec coverage:** §5.2.7 (chainId on CreateAd/AdFilter/ListAds, validated against enabled chains + asset curation). The §4.5 end-state (Sonic→MCGP) is achieved via a config flip at BSC launch (Plan 6 hand-off), not enforced prematurely here.
- **Non-breaking:** Sonic stays permissive (all 3) in the transition; the only curation enforced now is "no MCGP on BSC" (correct + safe) and "no ads on unconfigured chains" (so BSC ads can't predate the deploy). Existing tests use the allow-all FromMap registry → unaffected.
- **Type consistency:** `ChainID int64` on `CreateAdParams`/`AdFilter`/`createAdRequest`; `AssetAllowed(chainID, symbol) bool`; CreateAd resolves `chainID` once and uses it for ad.ChainID + voucher domain + validation.
- **Default behavior:** `chainId` omitted → Sonic (146); back-compatible with the current app until Plan 7 adds the selector.
