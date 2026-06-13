# P2P Multi-Chain — Plan 2: Per-Chain P2PEscrowService Registry + Boot Verification

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Turn the single Sonic-bound `P2PEscrowService` into a per-chain registry (`P2PEscrowRegistry`) resolvable by chainID with a **fail-loud** miss, and verify every configured P2P chain's escrow at boot (eth_chainId + bytecode), per chain.

**Architecture:** Add a constructor that takes an explicit escrow address (so one service can be built per chain), wrap a `map[int64]*P2PEscrowService` in a `P2PEscrowRegistry` built from `cfg.P2PChains` (Plan 1) via `BlockchainService.ClientForChainID`, and replace the Sonic-only boot bytecode check with a per-chain verification loop. The existing single `p2pEscrowService` stays wired into the trade service for now — Plan 3 migrates the trade service to the registry.

**Tech Stack:** Go 1.24, `internal/services/p2p_escrow_service.go` (+ new `p2p_escrow_registry.go`), `cmd/server/main.go`, in-package `package services` tests.

**Depends on:** Plan 1 (`cfg.P2PChains`, `P2PChainConfig`).

---

## File structure

- **Modify:** `internal/services/p2p_escrow_service.go` — add `NewP2PEscrowServiceForChain`; make `NewP2PEscrowService` delegate.
- **Create:** `internal/services/p2p_escrow_registry.go` — `P2PEscrowRegistry` + `For` + `NewP2PEscrowRegistry`.
- **Create:** `internal/services/p2p_escrow_registry_test.go` — `For` hit + fail-loud miss.
- **Modify:** `cmd/server/main.go` — build the registry + per-chain boot verification (replaces the Sonic-only bytecode block at ~91-101).

---

## Task 1: Per-chain escrow-service constructor

**Files:** Modify `internal/services/p2p_escrow_service.go` (the `NewP2PEscrowService` func at ~line 230).

- [ ] **Step 1 — failing test.** Create `internal/services/p2p_escrow_registry_test.go` (start it here; Task 2 adds more):

```go
package services

import "testing"

func TestNewP2PEscrowServiceForChain_UsesExplicitAddress(t *testing.T) {
	s := NewP2PEscrowServiceForChain(nil, nil, "0xBSCescrow")
	if s.ContractAddress() != "0xBSCescrow" {
		t.Errorf("ContractAddress() = %q, want 0xBSCescrow", s.ContractAddress())
	}
}
```

- [ ] **Step 2 — run, expect fail.** `go test ./internal/services/ -run TestNewP2PEscrowServiceForChain -v` → FAIL (undefined: `NewP2PEscrowServiceForChain`).

- [ ] **Step 3 — implement.** In `p2p_escrow_service.go`, replace the existing `NewP2PEscrowService` with:

```go
// NewP2PEscrowServiceForChain builds an escrow adapter bound to an explicit
// escrow contract address (one per chain). `cfg` may be nil in tests that only
// exercise address-dependent paths.
func NewP2PEscrowServiceForChain(client *blockchain.EVMClient, cfg *config.Config, escrowAddress string) *P2PEscrowService {
	return &P2PEscrowService{
		client:          client,
		contractAddress: escrowAddress,
		cfg:             cfg,
	}
}

// NewP2PEscrowService is the legacy single-chain constructor — binds the Sonic
// escrow from cfg.P2PEscrowAddress. Retained for callers not yet migrated to
// the per-chain registry (see Plan 2/3).
func NewP2PEscrowService(client *blockchain.EVMClient, cfg *config.Config) *P2PEscrowService {
	addr := ""
	if cfg != nil {
		addr = cfg.P2PEscrowAddress
	}
	return NewP2PEscrowServiceForChain(client, cfg, addr)
}
```

- [ ] **Step 4 — run, expect pass.** `go test ./internal/services/ -run TestNewP2PEscrowServiceForChain -v` → PASS. Also `go build ./...` clean.

- [ ] **Step 5 — commit.** `git commit -am "refactor(p2p): per-chain escrow constructor NewP2PEscrowServiceForChain"`

---

## Task 2: `P2PEscrowRegistry` (fail-loud resolution)

**Files:** Create `internal/services/p2p_escrow_registry.go`; extend `internal/services/p2p_escrow_registry_test.go`.

- [ ] **Step 1 — failing tests.** Append to `p2p_escrow_registry_test.go`:

```go
func TestP2PEscrowRegistry_For_HitAndFailLoudMiss(t *testing.T) {
	reg := &P2PEscrowRegistry{byChain: map[int64]*P2PEscrowService{
		146: NewP2PEscrowServiceForChain(nil, nil, "0xSonic"),
	}}

	got, err := reg.For(146)
	if err != nil {
		t.Fatalf("For(146) error = %v, want nil", err)
	}
	if got.ContractAddress() != "0xSonic" {
		t.Errorf("For(146) escrow = %q, want 0xSonic", got.ContractAddress())
	}

	// Fail-loud: an unconfigured chain must ERROR, never default to another chain.
	if _, err := reg.For(56); err == nil {
		t.Errorf("For(56) on unconfigured chain returned nil error — must fail loud")
	}
}
```

- [ ] **Step 2 — run, expect fail.** `go test ./internal/services/ -run TestP2PEscrowRegistry -v` → FAIL (undefined: `P2PEscrowRegistry`).

- [ ] **Step 3 — implement.** Create `internal/services/p2p_escrow_registry.go`:

```go
package services

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// P2PEscrowRegistry holds one escrow adapter per P2P-enabled chain, keyed by
// chainID. Resolution is fail-loud: an unconfigured chain returns an error and
// NEVER falls back to another chain — a BSC trade read against the Sonic escrow
// would orphan funds (see spec §8.5).
type P2PEscrowRegistry struct {
	byChain map[int64]*P2PEscrowService
}

// For returns the escrow adapter for chainID, or an error if that chain is not
// P2P-enabled. Callers MUST treat the error as "P2P unavailable on this chain",
// not as a reason to use a default chain.
func (r *P2PEscrowRegistry) For(chainID int64) (*P2PEscrowService, error) {
	if r == nil || r.byChain == nil {
		return nil, fmt.Errorf("p2p escrow registry not initialized")
	}
	s, ok := r.byChain[chainID]
	if !ok {
		return nil, fmt.Errorf("p2p: no escrow configured for chain %d", chainID)
	}
	return s, nil
}

// ChainIDs returns the configured P2P chain IDs (for diagnostics/iteration).
func (r *P2PEscrowRegistry) ChainIDs() []int64 {
	out := make([]int64, 0, len(r.byChain))
	for id := range r.byChain {
		out = append(out, id)
	}
	return out
}

// clientResolver is the BlockchainService surface the registry needs (kept
// narrow so tests can fake it).
type clientResolver interface {
	ClientForChainID(chainID int64) (*EVMClientHandle, string)
}

// NewP2PEscrowRegistry builds the per-chain escrow registry from cfg.P2PChains,
// resolving each chain's client via bs.ClientForChainID. It also runs a boot
// verification per chain (eth_chainId probe + escrow bytecode check) and logs
// loudly on any mismatch so an operator can fix env before the first trade. A
// chain whose client can't be resolved is skipped with a warning (For() will
// then fail loud for it).
func NewP2PEscrowRegistry(bs *BlockchainService, cfg *config.Config) *P2PEscrowRegistry {
	reg := &P2PEscrowRegistry{byChain: map[int64]*P2PEscrowService{}}
	if cfg == nil {
		return reg
	}
	for chainID, cc := range cfg.P2PChains {
		client, _ := bs.ClientForChainID(chainID)
		if client == nil {
			log.Printf("[config] WARNING: P2P chain %d (%s) has no RPC client — skipping (P2P unavailable on this chain)", chainID, cc.ChainKey)
			continue
		}
		reg.byChain[chainID] = NewP2PEscrowServiceForChain(client, cfg, cc.EscrowAddress)
		verifyP2PChainAtBoot(client, chainID, cc)
	}
	log.Printf("[config] P2P escrow registry: %d chain(s) enabled: %v", len(reg.byChain), reg.ChainIDs())
	return reg
}

// verifyP2PChainAtBoot warns (does not abort) when the RPC's eth_chainId
// disagrees with the configured chainID, or when the escrow address has no
// bytecode — either makes signatures/txs fail later with opaque errors.
func verifyP2PChainAtBoot(client *blockchain.EVMClient, chainID int64, cc config.P2PChainConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if rpcID, err := client.Client.ChainID(ctx); err != nil {
		log.Printf("[config] WARNING: eth_chainId probe failed for P2P chain %d (%s): %v", chainID, cc.ChainKey, err)
	} else if rpcID.Int64() != chainID {
		log.Printf("[config] WARNING: P2P chain %s configured chainID=%d but RPC reports %d — voucher signatures will be rejected until this matches", cc.ChainKey, chainID, rpcID.Int64())
	}
	if cc.EscrowAddress == "" {
		return
	}
	if code, err := client.GetCode(cc.EscrowAddress); err != nil {
		log.Printf("[config] WARNING: could not check bytecode at %s escrow %s: %v", cc.ChainKey, cc.EscrowAddress, err)
	} else if len(code) == 0 {
		log.Printf("[config] WARNING: %s escrow %s has NO bytecode on chain %d — P2P will fail with recovery mismatches", cc.ChainKey, cc.EscrowAddress, chainID)
	} else {
		log.Printf("[config] P2P escrow verified: %s %s on chain %d (%d bytes)", cc.ChainKey, cc.EscrowAddress, chainID, len(code))
	}
}
```

> **Implementer note:** the `clientResolver`/`EVMClientHandle` interface stub above is illustrative — DO NOT add it if it doesn't match the real types. `bs.ClientForChainID` returns `(*blockchain.EVMClient, string)`. Use `*blockchain.EVMClient` directly (import `github.com/ojimcy/tsa-api-go/internal/blockchain`) and delete the `clientResolver` interface. Keep `verifyP2PChainAtBoot` signature using `*blockchain.EVMClient`.

- [ ] **Step 4 — run, expect pass.** `go test ./internal/services/ -run 'TestP2PEscrowRegistry|TestNewP2PEscrowServiceForChain' -v` → PASS. `go build ./...` clean. `go vet ./internal/services/` clean.

- [ ] **Step 5 — commit.** `git commit -am "feat(p2p): per-chain P2PEscrowRegistry with fail-loud resolution + boot verify"`

---

## Task 3: Wire the registry into boot + per-chain verification

**Files:** Modify `cmd/server/main.go` (~lines 80-101).

- [ ] **Step 1 — implement.** After the existing `p2pEscrowService := services.NewP2PEscrowService(p2pSonicClient, cfg)` line, add:

```go
	// Per-chain escrow registry (Sonic + any enabled BSC). Boot verification
	// (eth_chainId + bytecode per chain) runs inside the constructor. The single
	// p2pEscrowService above stays wired to the trade service until Plan 3
	// migrates it to this registry.
	p2pEscrowRegistry := services.NewP2PEscrowRegistry(blockchainService, cfg)
	_ = p2pEscrowRegistry // consumed by the trade service in Plan 3
```

Then **delete** the now-redundant Sonic-only bytecode block (the `if cfg.P2PEscrowAddress != "" && p2pSonicClient != nil { code, codeErr := ... }` block at ~lines 91-101) — the registry's `verifyP2PChainAtBoot` now covers Sonic (and every chain) with the same checks plus the eth_chainId probe.

- [ ] **Step 2 — verify build + boot smoke.** `go build ./...` clean. Optionally `go vet ./cmd/server/`.

- [ ] **Step 3 — full backend test sweep** (no regressions): `go test ./... -count=1` → all packages pass (config + services + handlers + jobs + blockchain).

- [ ] **Step 4 — commit.** `git commit -am "feat(p2p): build per-chain escrow registry at boot; drop Sonic-only bytecode check"`

---

## Self-review (completed)

- **Spec coverage:** implements §5.2.3 (per-chain `P2PEscrowService` registry via `ClientForChainID`) and the registry half of §5.2.5 (per-chain boot probe + bytecode check). The trade-service/handler multiplex (§5.2.4) and reconciler fail-loud (§5.2.6) are Plans 3–4; this plan deliberately leaves the single `p2pEscrowService`→trade-service wiring intact (registry built but consumed next plan).
- **Placeholder scan:** none; the `clientResolver` interface in the code block is explicitly flagged as illustrative with a concrete instruction to use `*blockchain.EVMClient` directly.
- **Fail-loud invariant:** `For()` returns an error for an unconfigured chain and never defaults — tested directly (spec §8.5).
- **Type consistency:** `For(chainID int64) (*P2PEscrowService, error)`, `byChain map[int64]*P2PEscrowService`, `NewP2PEscrowServiceForChain(client, cfg, escrowAddress)` used identically across registry, tests, and main.go wiring.
