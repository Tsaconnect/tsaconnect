# P2P Multi-Chain — Plan 3: Per-Chain Trade Service (Option B)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development / executing-plans. Checkbox steps.

**Goal:** Make `P2PTradeService` chain-aware by resolving escrow + EIP-712 domain (chainId + version) **per-operation from the loaded entity's `ChainID`** via the `P2PEscrowRegistry`, removing the single-chain scalars so any missed site is a **compile error, not a silent Sonic fallback**.

**Architecture (Option B):** The service drops `escrow P2PEscrowAdapter`, `chainID int64`, `eip712Version string` and instead holds `escrows *P2PEscrowRegistry` + a `defaultChainID int64` (Sonic, for ops with no entity chain yet). The registry stores the escrow as the **`P2PEscrowAdapter` interface** (mock-injectable) plus the **RPC-probed** domain chainId, and exposes `Resolve(chainID) (P2PEscrowAdapter, int64 domainChainID, string version, error)`. Every escrow-touching method resolves from the entity it already loads.

**Tech Stack:** Go 1.24. `internal/services/p2p_escrow_registry.go`, `p2p_trade_service.go`, `cmd/server/main.go`, in-package `services` + `handlers` tests.

**Depends on:** Plans 1 (`Config.P2PChains`) + 2 (`P2PEscrowRegistry`).

---

## File structure
- **Modify** `internal/services/p2p_escrow_registry.go` — store escrow as `P2PEscrowAdapter` + add `domainChainID`; `For`→`(P2PEscrowAdapter,error)`; add `Resolve`; capture probed chainId.
- **Modify** `internal/services/p2p_escrow_registry_test.go` — adjust to interface type; test `Resolve`.
- **Modify** `internal/services/p2p_trade_service.go` — struct + `NewP2PTradeService` signature; private `resolve` helper; convert ~20 `s.escrow`/`s.chainID`/`s.eip712Version` sites; `buildVoucherTypedData` takes resolved domain.
- **Modify** `internal/services/p2p_trade_service_test.go` — new constructor signature via a registry helper.
- **Modify** `internal/handlers/p2p_handler_test.go` — same constructor change.
- **Modify** `cmd/server/main.go` — wire the registry into the trade service; drop the inline single-escrow + chainId probe.

---

## Task 1: Registry resolves the full per-chain domain (interface-typed + RPC chainId)

**Files:** `internal/services/p2p_escrow_registry.go` (+ test).

- [ ] **Step 1 — failing test.** Append to `p2p_escrow_registry_test.go`:

```go
func TestP2PEscrowRegistry_Resolve(t *testing.T) {
	esc := NewP2PEscrowServiceForChain(nil, nil, "0xSonic") // a P2PEscrowAdapter
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: esc, eip712Version: "2", domainChainID: 146},
	}}
	got, domainID, ver, err := reg.Resolve(146)
	if err != nil {
		t.Fatalf("Resolve(146) err=%v", err)
	}
	if got.ContractAddress() != "0xSonic" || domainID != 146 || ver != "2" {
		t.Errorf("Resolve(146) = (%q,%d,%q), want (0xSonic,146,2)", got.ContractAddress(), domainID, ver)
	}
	if _, _, _, err := reg.Resolve(56); err == nil {
		t.Errorf("Resolve(56) on unconfigured chain must fail loud")
	}
}
```

- [ ] **Step 2 — run, expect fail** (`Resolve` undefined; `chainEscrow` lacks `domainChainID`; `service` may be wrong type). `go test ./internal/services/ -run TestP2PEscrowRegistry -v`.

- [ ] **Step 3 — implement.** In `p2p_escrow_registry.go`:

(a) Change the entry struct to hold the **interface** + the probed chainId:
```go
type chainEscrow struct {
	service       P2PEscrowAdapter // interface, so tests can inject a mock
	eip712Version string
	domainChainID int64 // RPC-reported chainId for the EIP-712 domain
}
```
(b) Change `byChain` to `map[int64]*chainEscrow` (unchanged) and `For` to return the interface:
```go
func (r *P2PEscrowRegistry) For(chainID int64) (P2PEscrowAdapter, error) { /* same body, returns e.service */ }
```
(c) Add `Resolve`:
```go
// Resolve returns everything the trade service needs to act on a chain: the
// escrow adapter, the EIP-712 domain chainId (RPC-probed), and the domain
// version. Fail-loud for an unconfigured chain — never default to another chain.
func (r *P2PEscrowRegistry) Resolve(chainID int64) (P2PEscrowAdapter, int64, string, error) {
	if r == nil || r.byChain == nil {
		return nil, 0, "", fmt.Errorf("p2p escrow registry not initialized")
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return nil, 0, "", fmt.Errorf("p2p: no escrow configured for chain %d", chainID)
	}
	return e.service, e.domainChainID, e.eip712Version, nil
}
```
(d) In `NewP2PEscrowRegistry`, capture the probed chainId. Refactor `verifyP2PChainAtBoot` to **return** the effective domain chainId (RPC value when the probe succeeds, else the configured chainID as a fallback) and keep its warning logs:
```go
func verifyP2PChainAtBoot(client *blockchain.EVMClient, chainID int64, cc config.P2PChainConfig) int64 {
	domainChainID := chainID
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if rpcID, err := client.Client.ChainID(ctx); err != nil {
		log.Printf("[config] WARNING: eth_chainId probe failed for P2P chain %d (%s): %v — using configured %d for the EIP-712 domain", chainID, cc.ChainKey, err, chainID)
	} else {
		domainChainID = rpcID.Int64()
		if domainChainID != chainID {
			log.Printf("[config] WARNING: P2P chain %s configured chainID=%d but RPC reports %d (EIP712Version=%s) — using the RPC value for the domain so signatures stay valid", cc.ChainKey, chainID, domainChainID, cc.EIP712Version)
		}
	}
	// ...bytecode check unchanged (early-return when EscrowAddress=="")...
	return domainChainID
}
```
And in the build loop, store it:
```go
		reg.byChain[chainID] = &chainEscrow{
			service:       NewP2PEscrowServiceForChain(client, cfg, cc.EscrowAddress),
			eip712Version: cc.EIP712Version,
			domainChainID: verifyP2PChainAtBoot(client, chainID, cc),
		}
```

- [ ] **Step 4 — run, expect pass.** `go test ./internal/services/ -run 'P2PEscrowRegistry|NewP2PEscrowServiceForChain' -v` → PASS. `go build ./...` clean.

- [ ] **Step 5 — commit.** `git commit -am "feat(p2p): registry Resolve() returns escrow+domainChainID+version (RPC-probed)"`

---

## Task 2: Refactor `P2PTradeService` to resolve per entity-chain

**Files:** `internal/services/p2p_trade_service.go` (+ its test + the handler test).

- [ ] **Step 1 — change the struct + constructor.** Replace the fields:
```go
	escrow  P2PEscrowAdapter
	...
	chainID int64
	eip712Version string
```
with:
```go
	escrows        *P2PEscrowRegistry
	defaultChainID int64 // for ops with no entity chain yet (CreateAd, pool ops); Sonic
```
New constructor:
```go
func NewP2PTradeService(db *gorm.DB, escrows *P2PEscrowRegistry, rates RateProvider, bus EventPublisher, defaultChainID int64) *P2PTradeService {
	return &P2PTradeService{
		db: db, escrows: escrows, rates: rates, bus: bus,
		clock: SystemClock, defaultChainID: defaultChainID,
	}
}
```
Add the private resolver:
```go
// resolve returns the escrow adapter + EIP-712 domain (chainId, version) for a
// chain. Fail-loud — callers MUST propagate the error, never fall back.
func (s *P2PTradeService) resolve(chainID int64) (P2PEscrowAdapter, int64, string, error) {
	return s.escrows.Resolve(chainID)
}
```

- [ ] **Step 2 — convert every escrow/chain/version use (COMPILE-DRIVEN).** After Step 1 the package won't compile — `s.escrow`, `s.chainID`, `s.eip712Version` no longer exist. Fix EVERY error using this rule:
  - **Trade/ad-scoped methods** (they load a `models.P2PTrade` or `models.P2PAd`): resolve from the **entity's** chain at the top of the escrow section:
    ```go
    escrow, domainChainID, version, err := s.resolve(trade.ChainID) // or ad.ChainID
    if err != nil { return /*zero values*/, err }
    ```
    then replace `s.escrow.X(...)` → `escrow.X(...)`. The error-log site at ~line 617 that printed `s.chainID`/`s.escrow.ContractAddress()` uses `domainChainID`/`escrow.ContractAddress()`.
  - **`CreateAd`**: set `ChainID: s.defaultChainID` (was `s.chainID`) and resolve via `s.resolve(s.defaultChainID)` for the voucher domain.
  - **Pool/seller ops with no entity chain** (`PrepareApprove`, `GetAllowance`, `PrepareDeposit`, `PrepareWithdraw`, `GetPoolBalance`, `ConfirmDeposit`, `ConfirmWithdraw`): resolve `s.resolve(s.defaultChainID)`.
  - **`buildVoucherTypedData`**: change its signature to accept the resolved domain instead of reading struct fields:
    ```go
    func (s *P2PTradeService) buildVoucherTypedData(v AdVoucher, contractAddress string, domainChainID int64, version string) VoucherTypedData {
        ... Domain: VoucherTypedDomain{ Name: "TSAConnectP2PEscrow", Version: version, ChainID: domainChainID, VerifyingContract: contractAddress }, ...
    }
    ```
    Update its callers (SignAd / voucher-hash paths) to resolve first (by the ad's ChainID) and pass `escrow.ContractAddress(), domainChainID, version`.
  - The reconciler entrypoint `ReconcileTrade` also uses `s.escrow` — resolve by `trade.ChainID` there too (this is the §8.5 fail-loud requirement; Plan 4 adds the dedicated test, but the conversion happens here for compile-correctness).

- [ ] **Step 3 — update the service tests.** In `p2p_trade_service_test.go`, add a helper near the top:
```go
func newTestEscrowRegistry(esc P2PEscrowAdapter, chainID int64, version string) *P2PEscrowRegistry {
	return &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		chainID: {service: esc, eip712Version: version, domainChainID: chainID},
	}}
}
```
Change the two `NewP2PTradeService(db, esc, &mockRates{...}, bus, 146, "1")` calls (lines ~418, ~515) to:
```go
	svc := NewP2PTradeService(db, newTestEscrowRegistry(esc, 146, "1"), &mockRates{...}, bus, 146)
```
(Any test trade/ad rows must have `ChainID: 146` so `resolve` finds the entry — most fixtures already set 146 or inherit the ad default; add `ChainID: 146` where a row is built without it and a resolve now fails.)

- [ ] **Step 4 — update the handler test.** In `internal/handlers/p2p_handler_test.go` line ~341, change `services.NewP2PTradeService(db, esc, &mockRates{}, bus, 146, "1")` to `services.NewP2PTradeService(db, services.NewTestEscrowRegistry(esc, 146, "1"), &mockRates{}, bus, 146)`.
  - Because the handler test is `package handlers` (external), it can't see the unexported helper. EITHER export a tiny test helper from services (`func NewTestEscrowRegistry(...)` in a `_test.go` is not visible cross-package) — so instead add an **exported** constructor usable in tests: `func NewP2PEscrowRegistryFromMap(m map[int64]P2PEscrowAdapter, version string) *P2PEscrowRegistry` in `p2p_escrow_registry.go` (production code, small, also handy for ops), and use it from both tests. Implement it to build `chainEscrow` entries with `domainChainID = chainID`. Update Task 3's service test to use it too for consistency.

- [ ] **Step 5 — run + verify.** `go test ./internal/services/ -count=1` PASS; `go test ./internal/handlers/ -run P2P -count=1` (P2P handler tests pass; the ~31 unrelated pre-existing handler failures stay unchanged). `go build ./... && go vet ./internal/services/ ./internal/handlers/ ./cmd/server/` clean.

- [ ] **Step 6 — commit.** `git commit -am "refactor(p2p): trade service resolves escrow+domain per entity-chain (Option B)"`

---

## Task 3: Wire the registry into the trade service at boot

**Files:** `cmd/server/main.go` (~lines 80-184).

- [ ] **Step 1 — implement.** Replace the `p2pTradeSvc = services.NewP2PTradeService(config.DB, p2pEscrowService, p2pService, eventBus, domainChainID, cfg.P2PEscrowEIP712Version)` call with:
```go
		p2pTradeSvc = services.NewP2PTradeService(config.DB, p2pEscrowRegistry, p2pService, eventBus, cfg.SonicChainID)
```
Delete the now-dead inline eth_chainId probe block (the `probeCtx … actualChainID … domainChainID := cfg.SonicChainID …` lines) — the registry probes per chain now. The single `p2pEscrowService` var is now used only by this call; remove it (and its `NewP2PEscrowService` line) **unless** another service still references it — grep first; if unused, delete to avoid a dead variable. Keep the `if cfg.P2PEscrowAddress != "" && p2pSonicClient != nil` gate (it still correctly gates whether P2P trade routes come up).

- [ ] **Step 2 — verify.** `go build ./...` clean; `go vet ./cmd/server/` clean.

- [ ] **Step 3 — full sweep.** `go test ./... -count=1` — services/config/jobs/blockchain/models green; handler P2P tests green; only the known ~31 unrelated handler failures remain.

- [ ] **Step 4 — commit.** `git commit -am "feat(p2p): wire per-chain escrow registry into the trade service at boot"`

---

## Self-review (completed)
- **Spec coverage:** implements §4.7 + §5.2.4 (Option B) — per-entity-chain resolution, interface-typed registry (mockable), RPC-probed domain chainId, admin path routes by trade chain, fail-loud on unconfigured chain. The dedicated reconciler/watchdog fail-loud **tests** + decimals are Plan 4; CreateAd/ListAds `chainId` param is Plan 5 (CreateAd defaults to Sonic here).
- **Compile-safety:** removing `s.escrow`/`s.chainID`/`s.eip712Version` turns every un-converted site into a build error — there is no default left to silently fall back to (the core safety property of Option B).
- **Testability preserved:** the registry stores `P2PEscrowAdapter` (interface), and `NewP2PEscrowRegistryFromMap` lets both the in-package and external (`handlers`) tests inject `mockEscrow`.
- **Type consistency:** `Resolve(chainID) (P2PEscrowAdapter, int64, string, error)` and `resolve(...)` used identically; `defaultChainID` (Sonic) only for entity-less ops; `buildVoucherTypedData(v, contractAddress, domainChainID, version)` signature consistent across its callers.
- **No-placeholder note:** the ~20 escrow call-site conversions are specified as a compile-driven rule with the exact resolve-snippet rather than 20 pre-written hunks — appropriate because they are mechanical, identical transforms the compiler enumerates; the reviewers verify each landed.
