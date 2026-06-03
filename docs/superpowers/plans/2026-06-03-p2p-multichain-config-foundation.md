# P2P Multi-Chain — Plan 1: Per-Chain Config Foundation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the backend a per-chain P2P config registry (`Config.P2PChains` keyed by chainID) — the single source of truth every later per-chain refactor reads — with Sonic always present and BSC dormant-by-default until its escrow is deployed/configured.

**Architecture:** Generalize the existing `sonicPreset` bundle to a chain-agnostic `chainPreset`, add a parallel `bscPresets` map, and have `Load()` assemble a `map[int64]P2PChainConfig` from the active Sonic preset (always) + the active BSC preset (only when its escrow address is set). Preserve the legacy scalar `P2PEscrowAddress`/`P2PEscrowEIP712Version` (= the Sonic entry) so nothing downstream breaks while later plans migrate to the registry.

**Tech Stack:** Go 1.24, `internal/config/config.go`, std `testing` (white-box `package config` tests, `t.Setenv` + `Load()`).

---

## Plan set (this is Plan 1 of 7)

The multi-chain effort (spec: `docs/superpowers/specs/2026-06-03-p2p-multichain-bsc-design.md`) is split into sequenced, independently-testable plans:

1. **Per-chain config foundation** ← *this plan* (unblocked; no deploy/key-custody needed)
2. Per-chain `P2PEscrowService` registry + per-chain boot probe loop + bytecode check
3. Per-chain `P2PTradeService` + `P2PHandler` multiplex (incl. admin dispute) + per-chain voucher domain
4. Reconciler + timeout-watchdog fail-loud per-chain escrow resolution + per-`(chain,token)` decimals (backend)
5. `CreateAd`/`ListAds` `chainId` + asset-curation allow-list validation
6. Contracts: deploy `P2PEscrowV2` to BSC testnet (gated on the §10.6 key-custody decision)
7. App: asset-first marketplace, 10 broadcast sites, per-`(chain,token)` decimals, BNB gas guard, receive-chain confirm, pool per-chain breakdown

Each plan ends green and committed. Plans 2–5 read `P2PChains` from this one.

---

## File structure

- **Modify:** `internal/config/config.go`
  - Rename type `sonicPreset` → `chainPreset` (now reused by BSC).
  - Add `var bscPresets map[string]chainPreset`.
  - Add `type P2PChainConfig struct` + `Config.P2PChains map[int64]P2PChainConfig` field.
  - Extend `Load()` to populate `P2PChains`.
- **Create:** `internal/config/config_test.go` (first config test file; white-box).

No other files change in this plan. (`main.go` keeps using the legacy `cfg.P2PEscrowAddress` scalar; Plan 2 switches it to the registry.)

---

## Task 1: Generalize the preset type + add BSC presets + the per-chain config struct

**Files:**
- Modify: `internal/config/config.go:15` (type rename), `:36` (map type), and new declarations.

- [ ] **Step 1: Rename the preset type `sonicPreset` → `chainPreset`.**

In `internal/config/config.go`, change the struct declaration (line ~15) from:

```go
type sonicPreset struct {
```

to:

```go
// chainPreset bundles every value that has to flip together for one chain's
// P2P deployment (Sonic or BSC). Flipping piecemeal makes the EIP-712 voucher
// domain mismatch what the contract recovers, so every signature gets rejected.
type chainPreset struct {
```

And change the `sonicPresets` map declaration (line ~36) from:

```go
var sonicPresets = map[string]sonicPreset{
```

to:

```go
var sonicPresets = map[string]chainPreset{
```

(Do NOT rename the variable `sonicPresets` — only the type. The `testnetSonic := sonicPresets["testnet"]` reference at ~line 346 is unaffected.)

- [ ] **Step 2: Add the `bscPresets` map** directly after the `sonicPresets` map literal (after its closing `}` at ~line 59):

```go
// bscPresets mirrors sonicPresets for BSC. p2pEscrow is EMPTY until P2PEscrowV2
// is deployed to BSC (see Plan 6) — an empty escrow keeps BSC P2P dormant (Load
// omits it from P2PChains). mcgpAddress is intentionally empty: MCGP is Sonic-
// only, so the fee-discount path is disabled on BSC (P2PEscrowV2 charges the
// standard fee when mcgpToken is address(0)). Testnet token addresses are filled
// in at deploy time via P2P_ESCROW_ADDRESS_BSC + the *_TOKEN_ADDRESS overrides.
var bscPresets = map[string]chainPreset{
	"mainnet": {
		rpcURL:        "https://bsc-dataseed.binance.org",
		chainID:       56,
		usdcAddress:   "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d",
		usdtAddress:   "0x55d398326f99059fF775485246999027B3197955",
		mcgpAddress:   "",
		p2pEscrow:     "", // set after the audit-gated BSC mainnet deploy
		eip712Version: "2",
	},
	"testnet": {
		rpcURL:        "https://data-seed-prebsc-1-s1.binance.org:8545",
		chainID:       97,
		usdcAddress:   "", // set at BSC-testnet deploy (test token addresses)
		usdtAddress:   "",
		mcgpAddress:   "",
		p2pEscrow:     "", // set after the BSC testnet deploy
		eip712Version: "2",
	},
}

// P2PChainConfig is the per-chain P2P deployment the trade/escrow services
// resolve by chainID. ChainID + EscrowAddress + EIP712Version move in lockstep
// (a mismatch rejects every voucher). Assets is the curated allow-list of
// tradeable asset symbols on this chain — no asset is split across chains.
type P2PChainConfig struct {
	ChainID       int64
	ChainKey      string // blockchainService key: "sonic", "bsc"
	EscrowAddress string
	EIP712Version string
	Assets        []string // e.g. ["MCGP"] on Sonic, ["USDT","USDC"] on BSC
}
```

- [ ] **Step 3: Add the `P2PChains` field to the `Config` struct.** Right after the `P2PEscrowEIP712Version string` field (~line 107):

```go
	// P2PChains is the per-chain P2P deployment registry keyed by chainID,
	// assembled in Load(). Sonic is always present; BSC is present only when
	// its escrow address is configured (dormant-by-default). Later refactors
	// resolve escrow/domain/version by trade.ChainID from this map.
	P2PChains map[int64]P2PChainConfig
```

- [ ] **Step 4: Verify it compiles.**

Run: `go build ./internal/config/`
Expected: builds cleanly, no output. (No behavior added yet — `P2PChains` is nil until Task 2.)

- [ ] **Step 5: Commit.**

```bash
git add internal/config/config.go
git commit -m "refactor(config): generalize chainPreset + add bscPresets + P2PChainConfig"
```

---

## Task 2: Populate `P2PChains` in `Load()` (Sonic always; BSC dormant-by-default)

**Files:**
- Create: `internal/config/config_test.go`
- Modify: `internal/config/config.go` (inside `Load()`, after the `cfg.P2PEscrowEIP712Version = …` line at ~223)

- [ ] **Step 1: Write the failing tests.**

Create `internal/config/config_test.go`:

```go
package config

import (
	"reflect"
	"testing"
)

// Sonic is always in the registry; BSC is dormant when no escrow is configured.
func TestLoad_SonicAlways_BSCDormantByDefault(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "") // BSC dormant

	cfg := Load()

	sonic, ok := cfg.P2PChains[14601]
	if !ok {
		t.Fatalf("expected Sonic testnet (14601) in P2PChains, got %+v", cfg.P2PChains)
	}
	if sonic.ChainKey != "sonic" || sonic.EIP712Version != "2" {
		t.Errorf("sonic entry = %+v, want chainKey=sonic version=2", sonic)
	}
	if sonic.EscrowAddress == "" {
		t.Errorf("sonic escrow should be the testnet preset, got empty")
	}
	if !reflect.DeepEqual(sonic.Assets, []string{"MCGP"}) {
		t.Errorf("sonic assets = %v, want [MCGP]", sonic.Assets)
	}
	if _, bsc := cfg.P2PChains[97]; bsc {
		t.Errorf("BSC must be dormant when no escrow configured, but 97 present")
	}
}

// BSC joins the registry once its escrow address is configured.
func TestLoad_BSCEnabledWhenEscrowConfigured(t *testing.T) {
	t.Setenv("BSC_NETWORK", "testnet")
	t.Setenv("P2P_ESCROW_ADDRESS_BSC", "0x00000000000000000000000000000000000bsc01")

	cfg := Load()

	bsc, ok := cfg.P2PChains[97]
	if !ok {
		t.Fatalf("expected BSC testnet (97) in P2PChains, got %+v", cfg.P2PChains)
	}
	if bsc.ChainKey != "bsc" || bsc.EIP712Version != "2" {
		t.Errorf("bsc entry = %+v, want chainKey=bsc version=2", bsc)
	}
	if bsc.EscrowAddress != "0x00000000000000000000000000000000000bsc01" {
		t.Errorf("bsc escrow = %q, want the env override", bsc.EscrowAddress)
	}
	if !reflect.DeepEqual(bsc.Assets, []string{"USDT", "USDC"}) {
		t.Errorf("bsc assets = %v, want [USDT USDC]", bsc.Assets)
	}
}

// The legacy scalar fields still mirror the Sonic entry (back-compat for
// consumers not yet migrated to P2PChains).
func TestLoad_LegacyScalarsMirrorSonic(t *testing.T) {
	t.Setenv("SONIC_NETWORK", "mainnet")
	cfg := Load()
	sonic := cfg.P2PChains[146]
	if cfg.P2PEscrowAddress != sonic.EscrowAddress {
		t.Errorf("legacy P2PEscrowAddress %q != sonic entry %q", cfg.P2PEscrowAddress, sonic.EscrowAddress)
	}
	if cfg.P2PEscrowEIP712Version != sonic.EIP712Version {
		t.Errorf("legacy version %q != sonic entry %q", cfg.P2PEscrowEIP712Version, sonic.EIP712Version)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail.**

Run: `go test ./internal/config/ -run TestLoad -v`
Expected: FAIL — `cfg.P2PChains` is nil, so the map lookups fail (`expected Sonic … in P2PChains, got map[]`).

- [ ] **Step 3: Implement the `P2PChains` assembly in `Load()`.**

In `internal/config/config.go`, immediately after the line:

```go
	cfg.P2PEscrowEIP712Version = getEnv("P2P_ESCROW_EIP712_VERSION", preset.eip712Version)
```

insert:

```go
	// ── Per-chain P2P registry ────────────────────────────────────────────
	// Sonic is always present (its escrow is preset/overridable above). BSC is
	// added only when its escrow address is configured (dormant-by-default).
	cfg.P2PChains = map[int64]P2PChainConfig{
		cfg.SonicChainID: {
			ChainID:       cfg.SonicChainID,
			ChainKey:      "sonic",
			EscrowAddress: cfg.P2PEscrowAddress,
			EIP712Version: cfg.P2PEscrowEIP712Version,
			Assets:        []string{"MCGP"},
		},
	}

	bscNetwork := strings.ToLower(strings.TrimSpace(getEnv("BSC_NETWORK", "testnet")))
	bscPreset, okBSC := bscPresets[bscNetwork]
	if !okBSC {
		log.Printf("[config] BSC_NETWORK=%q is unknown; falling back to 'testnet'", bscNetwork)
		bscNetwork = "testnet"
		bscPreset = bscPresets["testnet"]
	}
	bscEscrow := getEnv("P2P_ESCROW_ADDRESS_BSC", bscPreset.p2pEscrow)
	if bscEscrow != "" {
		cfg.P2PChains[bscPreset.chainID] = P2PChainConfig{
			ChainID:       bscPreset.chainID,
			ChainKey:      "bsc",
			EscrowAddress: bscEscrow,
			EIP712Version: getEnv("P2P_ESCROW_EIP712_VERSION_BSC", bscPreset.eip712Version),
			Assets:        []string{"USDT", "USDC"},
		}
		log.Printf("[config] P2P chains enabled: sonic(%d) + bsc(%d, network=%s, escrow=%s)",
			cfg.SonicChainID, bscPreset.chainID, bscNetwork, bscEscrow)
	} else {
		log.Printf("[config] P2P chains enabled: sonic(%d) only — BSC dormant (no escrow for BSC_NETWORK=%s)",
			cfg.SonicChainID, bscNetwork)
	}
```

(`strings` and `log` are already imported in this file.)

- [ ] **Step 4: Run the tests to verify they pass.**

Run: `go test ./internal/config/ -run TestLoad -v`
Expected: PASS — all three tests green.

- [ ] **Step 5: Run the full backend build + vet to confirm nothing else broke.**

Run: `go build ./... && go vet ./internal/config/`
Expected: clean (no output).

- [ ] **Step 6: Commit.**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): assemble per-chain P2PChains registry (BSC dormant-by-default)"
```

---

## Task 3: Document the new env vars

**Files:**
- Modify: `tsa-api-go/CLAUDE.md` (env-vars section) — *if* it enumerates P2P env vars; otherwise skip. Also add a one-line note to the spec status.

- [ ] **Step 1: Add the BSC P2P env vars to the backend env documentation.**

If `tsa-api-go/CLAUDE.md` (or `.env.example`) lists `SONIC_NETWORK` / `P2P_ESCROW_ADDRESS`, add beside them:

```
- BSC_NETWORK                     # "mainnet" | "testnet" (default testnet) — selects the BSC P2P preset
- P2P_ESCROW_ADDRESS_BSC          # BSC P2PEscrowV2 proxy; UNSET = BSC P2P dormant
- P2P_ESCROW_EIP712_VERSION_BSC   # default "2"; matches the deployed BSC contract
```

- [ ] **Step 2: Commit.**

```bash
git add tsa-api-go/CLAUDE.md
git commit -m "docs: document BSC P2P env vars (BSC_NETWORK, P2P_ESCROW_ADDRESS_BSC)"
```

---

## Self-review (completed)

- **Spec coverage:** This plan implements spec §5.2.1 (`bscPresets`), §5.2.2 (per-chain config map), §4.6 (dormant-by-default), and the §4.5 asset allow-list as config data. §5.2.3–9 + §6 + §7 are explicitly deferred to Plans 2–7 (listed above).
- **Placeholder scan:** No TBD/TODO; every code step shows complete code; the empty preset escrow/token strings are intentional (dormant until deploy) and documented, not placeholders.
- **Type consistency:** `chainPreset` (type) vs `sonicPresets`/`bscPresets` (vars) kept distinct; `P2PChainConfig` fields (`ChainID`, `ChainKey`, `EscrowAddress`, `EIP712Version`, `Assets`) used identically in the struct, in `Load()`, and in the tests. `P2PChains` keyed by `int64` chainID everywhere.
- **Back-compat:** legacy `P2PEscrowAddress`/`P2PEscrowEIP712Version` scalars untouched and asserted to mirror the Sonic entry, so `main.go` and the single-chain services keep working until Plan 2 migrates them.
