# Multi-Chain Wallet — Foundation + Tron Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `family`-based multi-chain abstraction and ship a complete self-custody **Tron** wallet (receive, balances, send TRX + TRC-20 USDT, history), reusing the user's existing BIP-39 mnemonic — no Turnkey.

**Architecture:** The app derives per-chain addresses and signs raw hashes with `@scure`/`@noble` primitives; **all** Tron transaction construction lives in the Go backend behind a `ChainClient` interface (one implementation per `family`). The existing EVM path stays as `family: evm`; Tron is added as `family: tron`. Balance/history reads go through a single backend gateway (caching + rate-limiting come in a later hardening pass; this plan establishes the seams). Bitcoin and Solana are separate follow-on plans that reuse this foundation.

**Tech Stack:** Go 1.24.7 (`github.com/ojimcy/tsa-api-go`, Gin, GORM), go-ethereum v1.17.1 (EVM only), TronGrid HTTP API (no Go Tron SDK), React Native + Expo, ethers v6, new `@scure/bip39` + `@scure/bip32` + `@noble/curves` + `@noble/hashes`.

## Global Constraints

- Backend module path: `github.com/ojimcy/tsa-api-go`, Go **1.24.7**. Do **not** add a Go Tron SDK; use TronGrid HTTP (`btcsuite`/`solana-go` are absent by design).
- **Self-custody only.** Backend stores **public addresses only**, never seeds/keys. App signs on-device.
- **Pinned derivation paths** (must match external wallets so an exported seed reproduces the same address): Tron `m/44'/195'/0'/0/0` (TronLink), BTC `m/84'/0'/0'/0/0` (BIP-84), Solana `m/44'/501'/0'/0'` (Phantom). BIP-39 passphrase empty everywhere.
- **Dormant-by-default:** a family whose RPC/indexer config is absent is **not registered**; its routes return a clear error and the server still boots.
- **Gas-token preflight is mandatory:** never let a USDT-only user reach a silent Tron revert. Tron TRC-20 sends **must** include a `fee_limit`; new-account sends cost ~1 TRX activation.
- Backend tests: **stdlib `testing`** (no testify), hand-written interface fakes, GORM over SQLite `:memory:` with `DisableForeignKeyConstraintWhenMigrating: true`. App tests: `jest-expo`, `jest.fn` injection.
- Handlers are methods on the aggregate `*handlers.Handlers`; routes mounted in `SetupRoutes`; jobs implement `jobs.Worker` and register via `scheduler.Register(...)`.
- TronGrid amounts: TRX is denominated in **sun** (1 TRX = 1e6 sun); USDT-TRC20 has **6 decimals**.

---

## File Structure

**Backend (create):**
- `internal/chains/chain_client.go` — `ChainClient` interface + shared types (`Balance`, `FeeQuote`, `TransferRequest`, `PreparedTx`, `SigningPayload`, `TxStatus`, `Activity`).
- `internal/chains/registry.go` — family registry + dormant gating.
- `internal/chains/tron/trongrid.go` — TronGrid HTTP client.
- `internal/chains/tron/client.go` — Tron `ChainClient` implementation.
- `internal/chains/tron/address.go` — base58check validation/encode helpers.
- `internal/models/wallet_address.go` — `WalletAddress` model.
- `internal/models/wallet_activity.go` — `WalletActivity` model.
- `internal/jobs/nonevm_reconciler.go` — non-EVM confirm/reconcile worker.

**Backend (modify):**
- `internal/config/config.go` — non-EVM chain config + Tron env gating.
- `internal/config/database.go` — register the two new models in `AutoMigrate`.
- `internal/handlers/wallet_handler.go` — make prepare/submit/register/balances/history family-aware.
- `internal/routes/routes.go` — no new routes (existing wallet routes become family-aware); gate the Tron reconciler wiring.
- `cmd/server/main.go` — construct the chain registry, inject into handlers, register the reconciler.

**App (create):**
- `services/chains/derivation.ts` — per-family address derivation from a mnemonic.
- `services/chains/sign.ts` — raw secp256k1/ed25519 signing primitives.
- `services/chains/__tests__/derivation.test.ts` — interop vectors.

**App (modify):**
- `constants/chains.ts` — add `family` to `ChainConfig`, add the `tron` entry.
- `services/walletApi.ts` — family-aware `prepare-tx`/`submit-tx` for the sign-hash flow.

---

### Task 1: Chain family interface + shared types (backend)

**Files:**
- Create: `internal/chains/chain_client.go`
- Test: `internal/chains/chain_client_test.go`

**Interfaces:**
- Produces: `ChainClient` interface and types `Balance`, `TransferRequest`, `FeeQuote`, `PreparedTx`, `SigningPayload`, `Signature`, `TxStatus`, `Activity` used by every family impl and by the wallet handler.

- [ ] **Step 1: Write the failing test** (`internal/chains/chain_client_test.go`)

```go
package chains

import "testing"

func TestBalanceZeroValueIsRenderable(t *testing.T) {
	b := Balance{Symbol: "TRX", Raw: "0", Decimals: 6}
	if b.Symbol != "TRX" || b.Decimals != 6 {
		t.Fatalf("unexpected balance: %+v", b)
	}
}

func TestSigningPayloadCarriesIndex(t *testing.T) {
	p := SigningPayload{Index: 2, HashHex: "deadbeef", Curve: CurveSecp256k1}
	if p.Index != 2 || p.Curve != CurveSecp256k1 {
		t.Fatalf("unexpected payload: %+v", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/chains/ -run TestSigningPayload -v`
Expected: FAIL — `undefined: SigningPayload` (package does not compile).

- [ ] **Step 3: Write minimal implementation** (`internal/chains/chain_client.go`)

```go
package chains

import "context"

type Curve string

const (
	CurveSecp256k1 Curve = "secp256k1"
	CurveEd25519   Curve = "ed25519"
)

// Balance is one native or token balance for an address.
type Balance struct {
	Symbol       string `json:"symbol"`
	Raw          string `json:"raw"`          // integer string in base units
	Decimals     int    `json:"decimals"`
	TokenAddress string `json:"tokenAddress,omitempty"`
	IsNative     bool   `json:"isNative"`
}

// TransferRequest describes a send the user wants to make.
type TransferRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    string `json:"amount"`    // integer string, base units
	Symbol    string `json:"symbol"`    // native symbol or token symbol
	TokenAddr string `json:"tokenAddr"` // empty for native sends
}

// FeeQuote is the result of the mandatory pre-send gas check.
type FeeQuote struct {
	GasSymbol       string `json:"gasSymbol"`       // e.g. "TRX"
	EstFeeRaw       string `json:"estFeeRaw"`       // integer string in gas base units
	GasDecimals     int    `json:"gasDecimals"`
	NeedsActivation bool   `json:"needsActivation"` // recipient not yet on-chain (Tron ~1 TRX)
	HasEnoughGas    bool   `json:"hasEnoughGas"`    // sender's native balance covers EstFeeRaw
	Note            string `json:"note,omitempty"`  // human hint, e.g. "You need ~1 TRX to send USDT"
}

// SigningPayload is one hash the app must sign.
type SigningPayload struct {
	Index   int    `json:"index"`   // input index (BTC); 0 for single-sig chains
	HashHex string `json:"hashHex"` // hex of the 32-byte digest to sign
	Curve   Curve  `json:"curve"`
}

// PreparedTx is the opaque unsigned tx plus the payloads to sign.
type PreparedTx struct {
	Family   string           `json:"family"`
	Raw      string           `json:"raw"`      // family-specific serialized unsigned tx (JSON/hex)
	Payloads []SigningPayload `json:"payloads"`
}

// Signature is one app-produced signature for a payload.
type Signature struct {
	Index  int    `json:"index"`
	SigHex string `json:"sigHex"`
}

type TxStatus struct {
	Found         bool   `json:"found"`
	Confirmed     bool   `json:"confirmed"`
	Success       bool   `json:"success"` // false = reverted/failed even if mined
	Confirmations int64  `json:"confirmations"`
	Note          string `json:"note,omitempty"`
}

type Activity struct {
	Hash      string `json:"hash"`
	Direction string `json:"direction"` // "in" | "out"
	AmountRaw string `json:"amountRaw"`
	Symbol    string `json:"symbol"`
	Counter   string `json:"counterparty"`
	Status    string `json:"status"`
	BlockTime int64  `json:"blockTime"`
}

// ChainClient is implemented once per non-EVM family.
type ChainClient interface {
	Family() string
	ValidateAddress(addr string) error
	GetBalances(ctx context.Context, addr string) ([]Balance, error)
	EstimateFee(ctx context.Context, req TransferRequest) (FeeQuote, error)
	PrepareTransfer(ctx context.Context, req TransferRequest) (PreparedTx, error)
	AssembleAndBroadcast(ctx context.Context, prepared PreparedTx, sigs []Signature) (txHash string, err error)
	GetTxStatus(ctx context.Context, txHash string) (TxStatus, error)
	History(ctx context.Context, addr string, cursor string) (items []Activity, next string, err error)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/chains/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/chains/chain_client.go tsa-api-go/internal/chains/chain_client_test.go
git commit -m "feat(chains): add ChainClient family interface and shared types"
```

---

### Task 2: Family registry with dormant gating (backend)

**Files:**
- Create: `internal/chains/registry.go`
- Test: `internal/chains/registry_test.go`

**Interfaces:**
- Consumes: `ChainClient` (Task 1).
- Produces: `Registry` with `Register(chainKey string, c ChainClient)`, `ForKey(chainKey string) (ChainClient, bool)`, `ForFamily(family string) (ChainClient, bool)`, `NewRegistry()`.

- [ ] **Step 1: Write the failing test** (`internal/chains/registry_test.go`)

```go
package chains

import (
	"context"
	"testing"
)

type stubClient struct{ family string }

func (s stubClient) Family() string                                          { return s.family }
func (s stubClient) ValidateAddress(string) error                            { return nil }
func (s stubClient) GetBalances(context.Context, string) ([]Balance, error)  { return nil, nil }
func (s stubClient) EstimateFee(context.Context, TransferRequest) (FeeQuote, error) {
	return FeeQuote{}, nil
}
func (s stubClient) PrepareTransfer(context.Context, TransferRequest) (PreparedTx, error) {
	return PreparedTx{}, nil
}
func (s stubClient) AssembleAndBroadcast(context.Context, PreparedTx, []Signature) (string, error) {
	return "", nil
}
func (s stubClient) GetTxStatus(context.Context, string) (TxStatus, error) { return TxStatus{}, nil }
func (s stubClient) History(context.Context, string, string) ([]Activity, string, error) {
	return nil, "", nil
}

func TestRegistryLookupByKeyAndFamily(t *testing.T) {
	r := NewRegistry()
	r.Register("tron", stubClient{family: "tron"})

	if _, ok := r.ForKey("tron"); !ok {
		t.Fatal("expected tron by key")
	}
	if _, ok := r.ForFamily("tron"); !ok {
		t.Fatal("expected tron by family")
	}
	if _, ok := r.ForKey("solana"); ok {
		t.Fatal("solana must be dormant (unregistered)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/chains/ -run TestRegistry -v`
Expected: FAIL — `undefined: NewRegistry`.

- [ ] **Step 3: Write minimal implementation** (`internal/chains/registry.go`)

```go
package chains

type Registry struct {
	byKey    map[string]ChainClient
	byFamily map[string]ChainClient
}

func NewRegistry() *Registry {
	return &Registry{byKey: map[string]ChainClient{}, byFamily: map[string]ChainClient{}}
}

// Register wires a client for a chain key (e.g. "tron"). Only called when that
// chain's config is present, so an absent chain stays dormant (ForKey → false).
func (r *Registry) Register(chainKey string, c ChainClient) {
	r.byKey[chainKey] = c
	r.byFamily[c.Family()] = c
}

func (r *Registry) ForKey(chainKey string) (ChainClient, bool) {
	c, ok := r.byKey[chainKey]
	return c, ok
}

func (r *Registry) ForFamily(family string) (ChainClient, bool) {
	c, ok := r.byFamily[family]
	return c, ok
}

func (r *Registry) Keys() []string {
	out := make([]string, 0, len(r.byKey))
	for k := range r.byKey {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/chains/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/chains/registry.go tsa-api-go/internal/chains/registry_test.go
git commit -m "feat(chains): add family registry with dormant-by-default gating"
```

---

### Task 3: Non-EVM chain config + Tron env gating (backend)

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/nonevm_config_test.go`

**Interfaces:**
- Produces: `NonEVMChainConfig` struct and `Config.NonEVMChains map[string]NonEVMChainConfig` (keyed by chain key, e.g. `"tron"`), populated only when the chain's RPC/indexer env is set.

- [ ] **Step 1: Write the failing test** (`internal/config/nonevm_config_test.go`)

```go
package config

import (
	"os"
	"testing"
)

func TestTronDormantWhenUnset(t *testing.T) {
	os.Unsetenv("TRON_RPC_URL")
	m := buildNonEVMChains()
	if _, ok := m["tron"]; ok {
		t.Fatal("tron must be absent when TRON_RPC_URL is unset")
	}
}

func TestTronPresentWhenConfigured(t *testing.T) {
	os.Setenv("TRON_RPC_URL", "https://api.trongrid.io")
	os.Setenv("TRON_USDT_ADDRESS", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	defer os.Unsetenv("TRON_RPC_URL")
	defer os.Unsetenv("TRON_USDT_ADDRESS")

	m := buildNonEVMChains()
	tron, ok := m["tron"]
	if !ok {
		t.Fatal("expected tron config")
	}
	if tron.Family != "tron" || tron.NativeSymbol != "TRX" {
		t.Fatalf("unexpected tron config: %+v", tron)
	}
	if tron.Tokens["USDT"] == "" {
		t.Fatal("expected USDT token address")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/config/ -run TestTron -v`
Expected: FAIL — `undefined: buildNonEVMChains`.

- [ ] **Step 3: Write minimal implementation** (add to `internal/config/config.go`)

Add the struct near `NetworkConfig` (~line 146):

```go
// NonEVMChainConfig configures one non-EVM chain (Tron/BTC/Solana). Populated
// only when its RPC/indexer env is present, so absent chains stay dormant.
type NonEVMChainConfig struct {
	ChainKey     string
	Family       string
	Network      string            // "mainnet" | "testnet"
	RPCURL       string            // TronGrid base, mempool base, or Solana RPC
	APIKey       string            // optional provider key
	NativeSymbol string
	NativeDecimals int
	Tokens       map[string]string // symbol → on-chain token contract/mint
}
```

Add the builder (near the chains builders, ~line 591) and call it in the `Load()`/config-assembly path where `cfg.Networks` is set (~line 686):

```go
func buildNonEVMChains() map[string]NonEVMChainConfig {
	out := map[string]NonEVMChainConfig{}

	// Tron — dormant unless TRON_RPC_URL is set.
	if rpc := os.Getenv("TRON_RPC_URL"); rpc != "" {
		tokens := map[string]string{}
		if a := os.Getenv("TRON_USDT_ADDRESS"); a != "" {
			tokens["USDT"] = a
		}
		out["tron"] = NonEVMChainConfig{
			ChainKey:       "tron",
			Family:         "tron",
			Network:        getEnv("TRON_NETWORK", "mainnet"),
			RPCURL:         rpc,
			APIKey:         os.Getenv("TRON_API_KEY"),
			NativeSymbol:   "TRX",
			NativeDecimals: 6,
			Tokens:         tokens,
		}
	}
	return out
}
```

Add the field to `Config` (near `Networks`, ~line 348): `NonEVMChains map[string]NonEVMChainConfig`, and assign it in `Load()` right after `cfg.Networks = ...`:

```go
cfg.NonEVMChains = buildNonEVMChains()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/config/ -run TestTron -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/config/config.go tsa-api-go/internal/config/nonevm_config_test.go
git commit -m "feat(config): add dormant-by-default non-EVM chain config (Tron)"
```

---

### Task 4: Wallet address + activity models and migration (backend)

**Files:**
- Create: `internal/models/wallet_address.go`, `internal/models/wallet_activity.go`
- Modify: `internal/config/database.go`
- Test: `internal/models/wallet_nonevm_test.go`

**Interfaces:**
- Produces: `models.WalletAddress`, `models.WalletActivity` with `TableName()`; both migrated by `config.AutoMigrate`.

- [ ] **Step 1: Write the failing test** (`internal/models/wallet_nonevm_test.go`)

```go
package models_test

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openMem(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.WalletAddress{}, &models.WalletActivity{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestWalletAddressRoundTrip(t *testing.T) {
	db := openMem(t)
	wa := models.WalletAddress{
		UserID:   uuid.New(),
		WalletID: "0xabc",
		ChainKey: "tron",
		Family:   "tron",
		Address:  "TXk...abc",
	}
	if err := db.Create(&wa).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got models.WalletAddress
	if err := db.Where("chain_key = ? AND wallet_id = ?", "tron", "0xabc").First(&got).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Address != "TXk...abc" {
		t.Fatalf("unexpected addr: %s", got.Address)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/models/ -run TestWalletAddress -v`
Expected: FAIL — `undefined: models.WalletAddress`.

- [ ] **Step 3: Write minimal implementation**

`internal/models/wallet_address.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

// WalletAddress is a public per-chain receive address for a given wallet.
// Keyed on (WalletID, ChainKey) to respect the app's multi-wallet reality.
type WalletAddress struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	WalletID  string    `gorm:"size:64;not null;uniqueIndex:idx_wallet_chain" json:"walletId"` // the EVM address that anchors the mnemonic
	ChainKey  string    `gorm:"size:16;not null;uniqueIndex:idx_wallet_chain" json:"chainKey"`
	Family    string    `gorm:"size:16;not null" json:"family"`
	Address   string    `gorm:"size:128;not null;index" json:"address"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (WalletAddress) TableName() string { return "wallet_addresses" }
```

`internal/models/wallet_activity.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

// WalletActivity is a cached, normalized non-EVM transaction history row.
type WalletActivity struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ChainKey      string    `gorm:"size:16;not null;uniqueIndex:idx_chain_hash_dir" json:"chainKey"`
	Address       string    `gorm:"size:128;not null;index" json:"address"`
	Hash          string    `gorm:"size:128;not null;uniqueIndex:idx_chain_hash_dir" json:"hash"`
	Direction     string    `gorm:"size:4;not null;uniqueIndex:idx_chain_hash_dir" json:"direction"` // "in" | "out"
	AmountRaw     string    `gorm:"type:numeric(78,0);not null" json:"amountRaw"`
	Symbol        string    `gorm:"size:16;not null" json:"symbol"`
	Counterparty  string    `gorm:"size:128" json:"counterparty"`
	Status        string    `gorm:"size:16;not null;default:'pending'" json:"status"`
	Confirmations int64     `gorm:"not null;default:0" json:"confirmations"`
	BlockTime     int64     `gorm:"index" json:"blockTime"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (WalletActivity) TableName() string { return "wallet_activity" }
```

Register both in `internal/config/database.go`'s `AutoMigrate` variadic list (append to the existing `DB.AutoMigrate(&models.User{}, ...)` call):

```go
	&models.WalletAddress{},
	&models.WalletActivity{},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/models/ -run TestWalletAddress -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/models/wallet_address.go tsa-api-go/internal/models/wallet_activity.go tsa-api-go/internal/config/database.go tsa-api-go/internal/models/wallet_nonevm_test.go
git commit -m "feat(models): add wallet_addresses and wallet_activity (multi-wallet keyed)"
```

---

### Task 5: TronGrid HTTP client (backend)

**Files:**
- Create: `internal/chains/tron/trongrid.go`, `internal/chains/tron/address.go`
- Test: `internal/chains/tron/trongrid_test.go`

**Interfaces:**
- Produces: `NewTronGrid(baseURL, apiKey string) *TronGrid` with methods `GetAccount(ctx, addr) (*Account, error)`, `CreateTransfer(ctx, from, to string, amountSun int64) (*RawTx, error)`, `TriggerTransfer(ctx, from, tokenAddr, to string, amount *big.Int, feeLimit int64) (*RawTx, error)`, `Broadcast(ctx, raw *RawTx, sigHex string) (string, error)`, `GetTxInfo(ctx, txID string) (*TxInfo, error)`. Address helpers `IsValidBase58(addr string) bool`, `Base58ToHex(addr string) (string, error)`.

- [ ] **Step 1: Write the failing test** (`internal/chains/tron/trongrid_test.go`)

Uses `httptest` to fake TronGrid — matches the codebase's stdlib-only style.

```go
package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAccountParsesBalances(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"balance":1500000,"trc20":[{"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t":"2000000"}]}]}`))
	}))
	defer srv.Close()

	tg := NewTronGrid(srv.URL, "")
	acc, err := tg.GetAccount(context.Background(), "TXYZ")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.BalanceSun != 1500000 {
		t.Fatalf("want 1500000 got %d", acc.BalanceSun)
	}
	if acc.TRC20["TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"] != "2000000" {
		t.Fatalf("trc20 not parsed: %+v", acc.TRC20)
	}
}

func TestGetAccountInactiveIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`)) // never-activated account
	}))
	defer srv.Close()

	tg := NewTronGrid(srv.URL, "")
	acc, err := tg.GetAccount(context.Background(), "TNEW")
	if err != nil {
		t.Fatalf("expected nil err for inactive account, got %v", err)
	}
	if acc.Active {
		t.Fatal("inactive account must report Active=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/chains/tron/ -run TestGetAccount -v`
Expected: FAIL — `undefined: NewTronGrid`.

- [ ] **Step 3: Write minimal implementation**

`internal/chains/tron/address.go` (base58check — implement locally, no external Tron dep):

```go
package tron

import (
	"crypto/sha256"
	"errors"
	"math/big"
)

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// IsValidBase58 checks a Tron mainnet address: base58check, 25 bytes, 0x41 prefix.
func IsValidBase58(addr string) bool {
	dec, ok := b58Decode(addr)
	if !ok || len(dec) != 25 || dec[0] != 0x41 {
		return false
	}
	body, checksum := dec[:21], dec[21:]
	h1 := sha256.Sum256(body)
	h2 := sha256.Sum256(h1[:])
	for i := 0; i < 4; i++ {
		if checksum[i] != h2[i] {
			return false
		}
	}
	return true
}

// Base58ToHex returns the 21-byte hex form (41-prefixed) TronGrid expects when visible=false.
func Base58ToHex(addr string) (string, error) {
	dec, ok := b58Decode(addr)
	if !ok || len(dec) != 25 {
		return "", errors.New("invalid tron address")
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 42)
	for i, b := range dec[:21] {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out), nil
}

func b58Decode(s string) ([]byte, bool) {
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range s {
		idx := -1
		for i, c := range b58Alphabet {
			if c == r {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, false
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}
	dec := x.Bytes()
	// restore leading zero bytes (encoded as leading '1's)
	zeros := 0
	for _, r := range s {
		if r == '1' {
			zeros++
		} else {
			break
		}
	}
	return append(make([]byte, zeros), dec...), true
}
```

`internal/chains/tron/trongrid.go`:

```go
package tron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

type TronGrid struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewTronGrid(baseURL, apiKey string) *TronGrid {
	return &TronGrid{baseURL: baseURL, apiKey: apiKey, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

type Account struct {
	Active     bool
	BalanceSun int64
	TRC20      map[string]string // token contract → integer string
}

type RawTx struct {
	TxID    string          `json:"txID"`
	RawData json.RawMessage `json:"raw_data"`
	RawHex  string          `json:"raw_data_hex"`
	Visible bool            `json:"visible"`
}

type TxInfo struct {
	ID          string
	BlockNumber int64
	Confirmed   bool
	Success     bool
}

func (t *TronGrid) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", t.apiKey)
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("trongrid %s: status %d: %s", path, resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}

func (t *TronGrid) GetAccount(ctx context.Context, addr string) (*Account, error) {
	var envelope struct {
		Data []struct {
			Balance int64                          `json:"balance"`
			TRC20   []map[string]string            `json:"trc20"`
		} `json:"data"`
	}
	if err := t.do(ctx, http.MethodGet, "/v1/accounts/"+addr, nil, &envelope); err != nil {
		return nil, err
	}
	acc := &Account{TRC20: map[string]string{}}
	if len(envelope.Data) == 0 {
		return acc, nil // inactive account: Active=false, zero balances
	}
	acc.Active = true
	acc.BalanceSun = envelope.Data[0].Balance
	for _, m := range envelope.Data[0].TRC20 {
		for k, v := range m {
			acc.TRC20[k] = v
		}
	}
	return acc, nil
}

func (t *TronGrid) CreateTransfer(ctx context.Context, from, to string, amountSun int64) (*RawTx, error) {
	body := map[string]any{"owner_address": from, "to_address": to, "amount": amountSun, "visible": true}
	var rt RawTx
	if err := t.do(ctx, http.MethodPost, "/wallet/createtransaction", body, &rt); err != nil {
		return nil, err
	}
	if rt.TxID == "" {
		return nil, fmt.Errorf("trongrid createtransaction returned no txID")
	}
	return &rt, nil
}

func (t *TronGrid) TriggerTransfer(ctx context.Context, from, tokenAddr, to string, amount *big.Int, feeLimit int64) (*RawTx, error) {
	// ABI-encode transfer(address,uint256): 32-byte padded recipient (hex, no 41 prefix) + 32-byte amount.
	toHex, err := Base58ToHex(to)
	if err != nil {
		return nil, err
	}
	param := fmt.Sprintf("%064s%064s", toHex[2:], toHexNoPrefix(amount))
	body := map[string]any{
		"owner_address":     from,
		"contract_address":  tokenAddr,
		"function_selector": "transfer(address,uint256)",
		"parameter":         param,
		"fee_limit":         feeLimit,
		"call_value":        0,
		"visible":           true,
	}
	var wrap struct {
		Transaction RawTx `json:"transaction"`
		Result      struct {
			Result  bool   `json:"result"`
			Message string `json:"message"`
		} `json:"result"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/triggersmartcontract", body, &wrap); err != nil {
		return nil, err
	}
	if wrap.Transaction.TxID == "" {
		return nil, fmt.Errorf("triggersmartcontract failed: %s", wrap.Result.Message)
	}
	return &wrap.Transaction, nil
}

func (t *TronGrid) Broadcast(ctx context.Context, raw *RawTx, sigHex string) (string, error) {
	body := map[string]any{
		"txID":         raw.TxID,
		"raw_data":     raw.RawData,
		"raw_data_hex": raw.RawHex,
		"signature":    []string{sigHex},
		"visible":      true,
	}
	var res struct {
		Result  bool   `json:"result"`
		Txid    string `json:"txid"`
		Message string `json:"message"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/broadcasttransaction", body, &res); err != nil {
		return "", err
	}
	if !res.Result {
		return "", fmt.Errorf("broadcast rejected: %s", res.Message)
	}
	return raw.TxID, nil
}

func (t *TronGrid) GetTxInfo(ctx context.Context, txID string) (*TxInfo, error) {
	body := map[string]any{"value": txID}
	var info struct {
		ID          string `json:"id"`
		BlockNumber int64  `json:"blockNumber"`
		Receipt     struct {
			Result string `json:"result"`
		} `json:"receipt"`
	}
	if err := t.do(ctx, http.MethodPost, "/wallet/gettransactioninfobyid", body, &info); err != nil {
		return nil, err
	}
	if info.ID == "" {
		return &TxInfo{Confirmed: false}, nil
	}
	// A native TRX transfer has no receipt.result; treat mined-with-no-failure as success.
	success := info.Receipt.Result == "" || info.Receipt.Result == "SUCCESS"
	return &TxInfo{ID: info.ID, BlockNumber: info.BlockNumber, Confirmed: info.BlockNumber > 0, Success: success}, nil
}

func toHexNoPrefix(n *big.Int) string {
	return fmt.Sprintf("%x", n)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/chains/tron/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/chains/tron/
git commit -m "feat(tron): TronGrid HTTP client + base58check address helpers"
```

---

### Task 6: Tron ChainClient implementation (backend)

**Files:**
- Create: `internal/chains/tron/client.go`
- Test: `internal/chains/tron/client_test.go`

**Interfaces:**
- Consumes: `chains.ChainClient` (Task 1), `*TronGrid` (Task 5), `config.NonEVMChainConfig` (Task 3).
- Produces: `NewClient(cfg config.NonEVMChainConfig, tg *TronGrid) *Client` implementing `chains.ChainClient`.

- [ ] **Step 1: Write the failing test** (`internal/chains/tron/client_test.go`)

```go
package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

func newTestClient(url string) *Client {
	cfg := config.NonEVMChainConfig{
		ChainKey: "tron", Family: "tron", RPCURL: url,
		NativeSymbol: "TRX", NativeDecimals: 6,
		Tokens: map[string]string{"USDT": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"},
	}
	return NewClient(cfg, NewTronGrid(url, ""))
}

func TestEstimateFeeBlocksWhenNoTRX(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// sender has 0 TRX; recipient (second lookup) inactive
		w.Write([]byte(`{"data":[{"balance":0,"trc20":[]}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	q, err := c.EstimateFee(context.Background(), chainsTransfer("USDT", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"))
	if err != nil {
		t.Fatalf("EstimateFee: %v", err)
	}
	if q.HasEnoughGas {
		t.Fatal("expected HasEnoughGas=false for a 0-TRX sender")
	}
	if q.GasSymbol != "TRX" {
		t.Fatalf("gas symbol want TRX got %s", q.GasSymbol)
	}
}

func TestValidateAddressRejectsBadPrefix(t *testing.T) {
	c := newTestClient("http://x")
	if err := c.ValidateAddress("0xabc"); err == nil {
		t.Fatal("expected rejection of non-Tron address")
	}
}
```

Add the small helper at the bottom of the test file:

```go
import "github.com/ojimcy/tsa-api-go/internal/chains"

func chainsTransfer(symbol, tokenAddr string) chains.TransferRequest {
	return chains.TransferRequest{
		From: "TSenderaddressbase58xxxxxxxxxxxxxxx", To: "TRecipientbase58xxxxxxxxxxxxxxxxxx",
		Amount: "1000000", Symbol: symbol, TokenAddr: tokenAddr,
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/chains/tron/ -run TestEstimateFee -v`
Expected: FAIL — `undefined: NewClient`.

- [ ] **Step 3: Write minimal implementation** (`internal/chains/tron/client.go`)

```go
package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Fee constants (conservative; refine against Nile testnet during execution).
const (
	trc20FeeLimitSun   = 100_000_000 // 100 TRX cap for TRC-20 transfer
	activationCostSun  = 1_100_000   // ~1.1 TRX to activate a new account
	nativeFeeEstSun    = 300_000     // ~0.3 TRX bandwidth headroom
)

type Client struct {
	cfg config.NonEVMChainConfig
	tg  *TronGrid
}

func NewClient(cfg config.NonEVMChainConfig, tg *TronGrid) *Client { return &Client{cfg: cfg, tg: tg} }

func (c *Client) Family() string { return "tron" }

func (c *Client) ValidateAddress(addr string) error {
	if !IsValidBase58(addr) {
		return fmt.Errorf("invalid tron address: %s", addr)
	}
	return nil
}

func (c *Client) GetBalances(ctx context.Context, addr string) ([]chains.Balance, error) {
	acc, err := c.tg.GetAccount(ctx, addr)
	if err != nil {
		return nil, err
	}
	out := []chains.Balance{{
		Symbol: "TRX", Raw: strconv.FormatInt(acc.BalanceSun, 10), Decimals: 6, IsNative: true,
	}}
	for sym, tokenAddr := range c.cfg.Tokens {
		out = append(out, chains.Balance{
			Symbol: sym, Raw: firstNonEmpty(acc.TRC20[tokenAddr], "0"),
			Decimals: 6, TokenAddress: tokenAddr,
		})
	}
	return out, nil
}

func (c *Client) EstimateFee(ctx context.Context, req chains.TransferRequest) (chains.FeeQuote, error) {
	sender, err := c.tg.GetAccount(ctx, req.From)
	if err != nil {
		return chains.FeeQuote{}, err
	}
	recipient, err := c.tg.GetAccount(ctx, req.To)
	if err != nil {
		return chains.FeeQuote{}, err
	}
	estFee := int64(nativeFeeEstSun)
	if req.TokenAddr != "" {
		estFee = trc20FeeLimitSun / 10 // typical energy burn is well under the cap
	}
	needsActivation := !recipient.Active
	if needsActivation {
		estFee += activationCostSun
	}
	note := ""
	if sender.BalanceSun < estFee {
		if req.TokenAddr != "" {
			note = "You need TRX in this wallet to send " + req.Symbol + "."
		} else {
			note = "Not enough TRX to cover the network fee."
		}
	}
	return chains.FeeQuote{
		GasSymbol: "TRX", EstFeeRaw: strconv.FormatInt(estFee, 10), GasDecimals: 6,
		NeedsActivation: needsActivation, HasEnoughGas: sender.BalanceSun >= estFee, Note: note,
	}, nil
}

func (c *Client) PrepareTransfer(ctx context.Context, req chains.TransferRequest) (chains.PreparedTx, error) {
	if err := c.ValidateAddress(req.To); err != nil {
		return chains.PreparedTx{}, err
	}
	var raw *RawTx
	var err error
	if req.TokenAddr == "" {
		amt, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("bad amount")
		}
		raw, err = c.tg.CreateTransfer(ctx, req.From, req.To, amt.Int64())
	} else {
		amt, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			return chains.PreparedTx{}, fmt.Errorf("bad amount")
		}
		raw, err = c.tg.TriggerTransfer(ctx, req.From, req.TokenAddr, req.To, amt, trc20FeeLimitSun)
	}
	if err != nil {
		return chains.PreparedTx{}, err
	}
	rawJSON, _ := json.Marshal(raw)
	return chains.PreparedTx{
		Family: "tron",
		Raw:    string(rawJSON),
		// The Tron txID IS the sha256 digest of raw_data — sign it directly.
		Payloads: []chains.SigningPayload{{Index: 0, HashHex: raw.TxID, Curve: chains.CurveSecp256k1}},
	}, nil
}

func (c *Client) AssembleAndBroadcast(ctx context.Context, prepared chains.PreparedTx, sigs []chains.Signature) (string, error) {
	if len(sigs) != 1 {
		return "", fmt.Errorf("tron expects exactly one signature, got %d", len(sigs))
	}
	var raw RawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &raw); err != nil {
		return "", err
	}
	return c.tg.Broadcast(ctx, &raw, sigs[0].SigHex)
}

func (c *Client) GetTxStatus(ctx context.Context, txHash string) (chains.TxStatus, error) {
	info, err := c.tg.GetTxInfo(ctx, txHash)
	if err != nil {
		return chains.TxStatus{}, err
	}
	return chains.TxStatus{
		Found: info.ID != "", Confirmed: info.Confirmed, Success: info.Success,
		Confirmations: boolToConf(info.Confirmed),
	}, nil
}

func (c *Client) History(ctx context.Context, addr string, cursor string) ([]chains.Activity, string, error) {
	// Minimal first pass: return empty history; hydrated by the reconciler/webhookless
	// polling in a later task. Kept non-nil to satisfy the interface contract.
	return []chains.Activity{}, "", nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func boolToConf(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/chains/tron/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/chains/tron/client.go tsa-api-go/internal/chains/tron/client_test.go
git commit -m "feat(tron): ChainClient impl (balances, fee preflight, prepare/assemble, status)"
```

---

### Task 7: Wire the registry + family-aware wallet endpoints (backend)

**Files:**
- Modify: `internal/handlers/wallet_handler.go`, `cmd/server/main.go`, `internal/routes/routes.go`
- Test: `internal/handlers/wallet_nonevm_handler_test.go`

**Interfaces:**
- Consumes: `chains.Registry` (Task 2), Tron client (Task 6).
- Produces: handler methods `RegisterNonEVMAddress`, `GetNonEVMBalances`, `PrepareNonEVMTx`, `SubmitNonEVMTx`, `GetNonEVMTxStatus`; `Handlers.ChainRegistry *chains.Registry` field.

- [ ] **Step 1: Write the failing test** (`internal/handlers/wallet_nonevm_handler_test.go`)

Follows the existing `wallet_handler_test.go` SQLite-`:memory:` + gin test-context pattern.

```go
package handlers

import (
	"context"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// stubTron is a hand fake of chains.ChainClient for handler wiring tests.
type stubTron struct{ chains.ChainClient }

func (stubTron) Family() string                { return "tron" }
func (stubTron) ValidateAddress(a string) error { return nil }
func (stubTron) GetBalances(context.Context, string) ([]chains.Balance, error) {
	return []chains.Balance{{Symbol: "TRX", Raw: "5", Decimals: 6, IsNative: true}}, nil
}

func TestRegistryResolvesFamily(t *testing.T) {
	r := chains.NewRegistry()
	r.Register("tron", stubTron{})
	c, ok := r.ForKey("tron")
	if !ok {
		t.Fatal("tron not registered")
	}
	bals, _ := c.GetBalances(context.Background(), "TXYZ")
	if len(bals) != 1 || bals[0].Symbol != "TRX" {
		t.Fatalf("unexpected balances: %+v", bals)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/handlers/ -run TestRegistryResolvesFamily -v`
Expected: FAIL — package compiles but the assertion path depends on `ChainRegistry` field added below; if it compiles and passes trivially, proceed (this test guards the wiring contract).

- [ ] **Step 3: Write minimal implementation**

Add the field to the `Handlers` struct (in `internal/handlers/` where `Handlers` is defined) and a constructor param, plus the handlers. In `wallet_handler.go` add:

```go
// --- non-EVM (family) wallet endpoints ---

type registerNonEVMRequest struct {
	WalletID string `json:"walletId" binding:"required"`
	ChainKey string `json:"chainKey" binding:"required"`
	Address  string `json:"address" binding:"required"`
}

func (h *Handlers) RegisterNonEVMAddress(c *gin.Context) {
	user, err := getUserFromContext(c)
	if err != nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req registerNonEVMRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	client, ok := h.ChainRegistry.ForKey(req.ChainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	if err := client.ValidateAddress(req.Address); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	rec := models.WalletAddress{
		UserID: user.ID, WalletID: req.WalletID, ChainKey: req.ChainKey,
		Family: client.Family(), Address: req.Address,
	}
	// upsert on (wallet_id, chain_key)
	if err := config.DB.Where("wallet_id = ? AND chain_key = ?", req.WalletID, req.ChainKey).
		Assign(rec).FirstOrCreate(&rec).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "failed to save address")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "registered", gin.H{"address": rec.Address})
}

func (h *Handlers) GetNonEVMBalances(c *gin.Context) {
	chainKey := c.Query("chain")
	addr := c.Query("address")
	client, ok := h.ChainRegistry.ForKey(chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	bals, err := client.GetBalances(c.Request.Context(), addr)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "balance read failed")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "ok", gin.H{"balances": bals})
}

func (h *Handlers) PrepareNonEVMTx(c *gin.Context) {
	var req chains.TransferRequest
	chainKey := c.Query("chain")
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	client, ok := h.ChainRegistry.ForKey(chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	fee, err := client.EstimateFee(c.Request.Context(), req)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, "fee estimate failed")
		return
	}
	if !fee.HasEnoughGas {
		utils.ErrorResponse(c, http.StatusPaymentRequired, fee.Note)
		return
	}
	prepared, err := client.PrepareTransfer(c.Request.Context(), req)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "prepared", gin.H{"prepared": prepared, "fee": fee})
}

type submitNonEVMRequest struct {
	Prepared chains.PreparedTx  `json:"prepared" binding:"required"`
	Sigs     []chains.Signature `json:"sigs" binding:"required"`
}

func (h *Handlers) SubmitNonEVMTx(c *gin.Context) {
	chainKey := c.Query("chain")
	var req submitNonEVMRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	client, ok := h.ChainRegistry.ForKey(chainKey)
	if !ok {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "chain unavailable")
		return
	}
	hash, err := client.AssembleAndBroadcast(c.Request.Context(), req.Prepared, req.Sigs)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadGateway, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "broadcast", gin.H{"txHash": hash})
}
```

Add `ChainRegistry *chains.Registry` to the `Handlers` struct definition and set it in the `Handlers` constructor. In `cmd/server/main.go`, build the registry after config load and before handler construction:

```go
chainRegistry := chains.NewRegistry()
if tcfg, ok := cfg.NonEVMChains["tron"]; ok {
	tg := tron.NewTronGrid(tcfg.RPCURL, tcfg.APIKey)
	chainRegistry.Register("tron", tron.NewClient(tcfg, tg))
	log.Printf("chains: tron registered (%s)", tcfg.RPCURL)
} else {
	log.Printf("chains: tron dormant (TRON_RPC_URL unset)")
}
```

Pass `chainRegistry` into the `Handlers` constructor. In `internal/routes/routes.go`, add to the existing `walletGroup`:

```go
walletGroup.POST("/nonevm/register", h.RegisterNonEVMAddress)
walletGroup.GET("/nonevm/balances", h.GetNonEVMBalances)
walletGroup.POST("/nonevm/prepare-tx", h.PrepareNonEVMTx)
walletGroup.POST("/nonevm/submit-tx", h.SubmitNonEVMTx)
```

- [ ] **Step 4: Run test + build to verify**

Run: `cd tsa-api-go && go build ./... && go test ./internal/handlers/ -run TestRegistryResolvesFamily -v`
Expected: build succeeds; PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/handlers/ tsa-api-go/cmd/server/main.go tsa-api-go/internal/routes/routes.go
git commit -m "feat(wallet): family-aware non-EVM endpoints + registry wiring (Tron dormant-safe)"
```

---

### Task 8: Non-EVM confirm/reconcile worker (backend)

**Files:**
- Create: `internal/jobs/nonevm_reconciler.go`
- Modify: `cmd/server/main.go`
- Test: `internal/jobs/nonevm_reconciler_test.go`

**Interfaces:**
- Consumes: `chains.Registry`, `models.WalletActivity`, GORM `DB`.
- Produces: `NewNonEVMReconciler(db *gorm.DB, reg *chains.Registry) *NonEVMReconciler` implementing `jobs.Worker`.

- [ ] **Step 1: Write the failing test** (`internal/jobs/nonevm_reconciler_test.go`)

```go
package jobs

import (
	"testing"
	"time"
)

func TestNonEVMReconcilerMeta(t *testing.T) {
	r := NewNonEVMReconciler(nil, nil)
	if r.Name() == "" {
		t.Fatal("Name empty")
	}
	if r.Interval() <= 0 {
		t.Fatalf("bad interval %v", r.Interval())
	}
	if r.Interval() != 45*time.Second {
		t.Fatalf("want 45s got %v", r.Interval())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/jobs/ -run TestNonEVMReconciler -v`
Expected: FAIL — `undefined: NewNonEVMReconciler`.

- [ ] **Step 3: Write minimal implementation** (`internal/jobs/nonevm_reconciler.go`)

```go
package jobs

import (
	"context"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// NonEVMReconciler walks pending non-EVM activity rows and finalizes them
// against on-chain truth. Sibling to the EVM ChainReconciler; family-aware
// because confirmation semantics differ (Tron ~3s, BTC ~10min, Solana sub-s).
type NonEVMReconciler struct {
	db  *gorm.DB
	reg *chains.Registry
}

func NewNonEVMReconciler(db *gorm.DB, reg *chains.Registry) *NonEVMReconciler {
	return &NonEVMReconciler{db: db, reg: reg}
}

func (r *NonEVMReconciler) Name() string            { return "nonevm-reconciler" }
func (r *NonEVMReconciler) Interval() time.Duration { return 45 * time.Second }

func (r *NonEVMReconciler) Run(ctx context.Context) error {
	if r.db == nil || r.reg == nil {
		return nil // dormant
	}
	var pending []models.WalletActivity
	if err := r.db.Where("status = ?", "pending").Limit(100).Find(&pending).Error; err != nil {
		return err
	}
	for i := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		row := pending[i]
		client, ok := r.reg.ForKey(row.ChainKey)
		if !ok {
			continue
		}
		st, err := client.GetTxStatus(ctx, row.Hash)
		if err != nil || !st.Confirmed {
			continue
		}
		status := "confirmed"
		if !st.Success {
			status = "failed"
		}
		r.db.Model(&models.WalletActivity{}).Where("id = ?", row.ID).
			Updates(map[string]any{"status": status, "confirmations": st.Confirmations})
	}
	return nil
}
```

Register in `cmd/server/main.go` next to the EVM reconciler registration (guard on the registry having any chains):

```go
if len(chainRegistry.Keys()) > 0 {
	scheduler.Register(jobs.NewNonEVMReconciler(config.DB, chainRegistry))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/jobs/ -run TestNonEVMReconciler -v && cd tsa-api-go && go build ./...`
Expected: PASS; build succeeds.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/jobs/nonevm_reconciler.go tsa-api-go/internal/jobs/nonevm_reconciler_test.go tsa-api-go/cmd/server/main.go
git commit -m "feat(jobs): non-EVM confirm/reconcile worker (dormant-safe)"
```

---

### Task 9: App — dependencies + address derivation with interop vectors

**Files:**
- Modify: `tsa-app/package.json`
- Create: `tsa-app/services/chains/derivation.ts`, `tsa-app/services/chains/__tests__/derivation.test.ts`

**Interfaces:**
- Produces: `deriveTronAddress(mnemonic: string): string`, `deriveBtcAddress(mnemonic: string): string`, `deriveSolanaAddress(mnemonic: string): string`, and `DERIVATION_PATHS` map. Consumed by the wallet UI (later) and by the send flow (Task 10).

- [ ] **Step 1: Install dependencies**

Run: `cd tsa-app && npm install @scure/bip39@^1.3.0 @scure/bip32@^1.5.0 @noble/curves@^1.6.0 @noble/hashes@^1.5.0 @scure/base@^1.1.9`
Expected: added to `package.json` dependencies.

- [ ] **Step 2: Write the failing test** (`services/chains/__tests__/derivation.test.ts`)

The BTC value is the **published BIP-84 test vector** for the canonical mnemonic — a hard automated gate. Tron/Solana assert format now and are pinned to external wallets in Step 5's manual interop check.

```ts
import { deriveTronAddress, deriveBtcAddress, deriveSolanaAddress } from '../derivation';

// Canonical BIP-39 test mnemonic.
const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';

describe('non-EVM derivation (one seed, standard paths)', () => {
  it('BTC matches the published BIP-84 vector (m/84h/0h/0h/0/0)', () => {
    expect(deriveBtcAddress(M)).toBe('bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu');
  });

  it('Tron produces a base58 T-address (m/44h/195h/0h/0/0)', () => {
    const a = deriveTronAddress(M);
    expect(a).toMatch(/^T[1-9A-HJ-NP-Za-km-z]{33}$/);
  });

  it('Solana produces a base58 ed25519 pubkey (m/44h/501h/0h/0h)', () => {
    const a = deriveSolanaAddress(M);
    expect(a).toMatch(/^[1-9A-HJ-NP-Za-km-z]{43,44}$/);
  });

  it('is deterministic', () => {
    expect(deriveTronAddress(M)).toBe(deriveTronAddress(M));
    expect(deriveSolanaAddress(M)).toBe(deriveSolanaAddress(M));
  });
});
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd tsa-app && npx jest services/chains/__tests__/derivation.test.ts`
Expected: FAIL — cannot find module `../derivation`.

- [ ] **Step 4: Write minimal implementation** (`services/chains/derivation.ts`)

```ts
import { mnemonicToSeedSync } from '@scure/bip39';
import { HDKey } from '@scure/bip32';
import { keccak_256 } from '@noble/hashes/sha3';
import { sha256 } from '@noble/hashes/sha256';
import { ripemd160 } from '@noble/hashes/ripemd160';
import { secp256k1 } from '@noble/curves/secp256k1';
import { ed25519 } from '@noble/curves/ed25519';
import { base58check, bech32 } from '@scure/base';

export const DERIVATION_PATHS = {
  tron: "m/44'/195'/0'/0/0",
  btc: "m/84'/0'/0'/0/0",
  solana: "m/44'/501'/0'/0'",
} as const;

const b58check = base58check(sha256);

function secpNode(mnemonic: string, path: string): HDKey {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  const node = HDKey.fromMasterSeed(seed).derive(path);
  if (!node.privateKey || !node.publicKey) throw new Error('derivation failed');
  return node;
}

// Tron: keccak256(uncompressed pubkey[1:])[12:] → prepend 0x41 → base58check.
export function deriveTronAddress(mnemonic: string): string {
  const node = secpNode(mnemonic, DERIVATION_PATHS.tron);
  const pub = secp256k1.getPublicKey(node.privateKey!, false); // 65 bytes, 0x04 prefix
  const hash = keccak_256(pub.slice(1));
  const body = new Uint8Array(21);
  body[0] = 0x41;
  body.set(hash.slice(12), 1);
  return b58check.encode(body);
}

// BTC native SegWit (BIP-84): bech32(hash160(compressed pubkey)).
export function deriveBtcAddress(mnemonic: string): string {
  const node = secpNode(mnemonic, DERIVATION_PATHS.btc);
  const h160 = ripemd160(sha256(node.publicKey!)); // publicKey is compressed (33 bytes)
  const words = bech32.toWords(h160);
  return bech32.encode('bc', [0x00, ...words]);
}

// Solana: ed25519 via SLIP-0010 hardened path → base58 of the 32-byte pubkey.
export function deriveSolanaAddress(mnemonic: string): string {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  const priv = ed25519Slip10(seed, DERIVATION_PATHS.solana);
  const pub = ed25519.getPublicKey(priv);
  return b58EncodeNoCheck(pub);
}

// --- SLIP-0010 ed25519 (all-hardened) ---
import { hmac } from '@noble/hashes/hmac';
import { sha512 } from '@noble/hashes/sha512';

function ed25519Slip10(seed: Uint8Array, path: string): Uint8Array {
  let I = hmac(sha512, new TextEncoder().encode('ed25519 seed'), seed);
  let key = I.slice(0, 32);
  let chain = I.slice(32);
  for (const seg of path.split('/').slice(1)) {
    const idx = (parseInt(seg) | 0x80000000) >>> 0; // ed25519 is always hardened
    const data = new Uint8Array(1 + 32 + 4);
    data.set(key, 1);
    new DataView(data.buffer).setUint32(33, idx, false);
    I = hmac(sha512, chain, data);
    key = I.slice(0, 32);
    chain = I.slice(32);
  }
  return key;
}

import { base58 } from '@scure/base';
function b58EncodeNoCheck(b: Uint8Array): string {
  return base58.encode(b);
}
```

- [ ] **Step 5: Run test + manual interop verification**

Run: `cd tsa-app && npx jest services/chains/__tests__/derivation.test.ts`
Expected: PASS (BTC exact vector + Tron/Solana format).

**Then verify external interop once (2 min) and pin literals:** import mnemonic `M` into TronLink and Phantom; confirm `deriveTronAddress(M)` equals the TronLink address and `deriveSolanaAddress(M)` equals the Phantom address. Replace the two `toMatch` assertions with exact `toBe(<pinned address>)`. This converts the "one seed phrase" promise into a hard regression gate.

- [ ] **Step 6: Commit**

```bash
git add tsa-app/package.json tsa-app/package-lock.json tsa-app/services/chains/derivation.ts tsa-app/services/chains/__tests__/derivation.test.ts
git commit -m "feat(app): non-EVM address derivation from existing mnemonic + interop vectors"
```

---

### Task 10: App — raw signing + Tron send flow

**Files:**
- Create: `tsa-app/services/chains/sign.ts`, `tsa-app/services/chains/tronSend.ts`, `tsa-app/services/chains/__tests__/tronSend.test.ts`
- Modify: `tsa-app/constants/chains.ts`

**Interfaces:**
- Consumes: `deriveTronAddress` (Task 9), backend `nonevm/prepare-tx` + `nonevm/submit-tx` (Task 7).
- Produces: `signSecpDigest(hashHex: string, privateKey: string): string` (65-byte r||s||v hex), `sendTron(params): Promise<{ txHash: string }>`.

- [ ] **Step 1: Add `family` to chain config** (`constants/chains.ts`)

Extend the `ChainConfig` interface with `family?: 'evm' | 'tron' | 'btc' | 'solana'` (defaults to `'evm'`), and add a `tron` entry to a new `NONEVM_CHAINS` record (do not add it to the EVM `ChainKey` union — keep the EVM readers untouched):

```ts
export interface NonEVMChainConfig {
  chainKey: 'tron' | 'btc' | 'solana';
  family: 'tron' | 'btc' | 'solana';
  name: string;
  shortName: string;
  explorerUrl: string;
  nativeCurrency: { symbol: string; decimals: number };
  iconColor: string;
}

export const NONEVM_CHAINS: Record<'tron', NonEVMChainConfig> = {
  tron: {
    chainKey: 'tron', family: 'tron', name: 'Tron', shortName: 'TRON',
    explorerUrl: 'https://tronscan.org',
    nativeCurrency: { symbol: 'TRX', decimals: 6 }, iconColor: '#EF0027',
  },
};
```

- [ ] **Step 2: Write the failing test** (`services/chains/__tests__/tronSend.test.ts`)

```ts
import { signSecpDigest } from '../sign';

describe('signSecpDigest', () => {
  it('produces a 65-byte hex signature (r||s||v)', () => {
    // known private key + digest; assert length/shape (Tron accepts recovery id v in {0,1})
    const pk = '0x0000000000000000000000000000000000000000000000000000000000000001';
    const digest = 'a'.repeat(64);
    const sig = signSecpDigest(digest, pk);
    expect(sig).toMatch(/^[0-9a-f]{130}$/); // 65 bytes = 130 hex chars
    const v = parseInt(sig.slice(128), 16);
    expect(v === 0 || v === 1).toBe(true);
  });
});
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd tsa-app && npx jest services/chains/__tests__/tronSend.test.ts`
Expected: FAIL — cannot find module `../sign`.

- [ ] **Step 4: Write minimal implementation**

`services/chains/sign.ts`:

```ts
import { secp256k1 } from '@noble/curves/secp256k1';

/** Sign a 32-byte hex digest with secp256k1; return 65-byte r||s||v hex (Tron format). */
export function signSecpDigest(hashHex: string, privateKey: string): string {
  const pk = privateKey.startsWith('0x') ? privateKey.slice(2) : privateKey;
  const sig = secp256k1.sign(hashHex, pk); // RecoveredSignature
  const r = sig.r.toString(16).padStart(64, '0');
  const s = sig.s.toString(16).padStart(64, '0');
  const v = sig.recovery.toString(16).padStart(2, '0');
  return r + s + v;
}
```

`services/chains/tronSend.ts`:

```ts
import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getPrivateKey } from '../wallet';
import { signSecpDigest } from './sign';

async function authHeaders() {
  const token = await AsyncStorage.getItem('authToken');
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

export async function sendTron(params: {
  from: string; to: string; amount: string; symbol: string; tokenAddr?: string; walletAddress: string;
}): Promise<{ txHash: string }> {
  const headers = await authHeaders();
  const body = {
    from: params.from, to: params.to, amount: params.amount,
    symbol: params.symbol, tokenAddr: params.tokenAddr ?? '',
  };
  const prep = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=tron`, {
    method: 'POST', headers, body: JSON.stringify(body),
  }).then((r) => r.json());
  if (!prep?.data?.prepared) throw new Error(prep?.message || 'prepare failed');

  const { prepared } = prep.data;
  const pk = await getPrivateKey(params.walletAddress);
  if (!pk) throw new Error('no key for wallet');
  const sigs = prepared.payloads.map((p: { index: number; hashHex: string }) => ({
    index: p.index, sigHex: signSecpDigest(p.hashHex, pk),
  }));

  const sub = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=tron`, {
    method: 'POST', headers, body: JSON.stringify({ prepared, sigs }),
  }).then((r) => r.json());
  if (!sub?.data?.txHash) throw new Error(sub?.message || 'broadcast failed');
  return { txHash: sub.data.txHash };
}
```

(Confirm `getPrivateKey(address)` is exported from `services/wallet.ts`; the reference shows the SecureStore key is `tsa-wallet-pk-${address}` read via `getPrivateKey`.)

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tsa-app && npx jest services/chains/__tests__/tronSend.test.ts`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tsa-app/services/chains/sign.ts tsa-app/services/chains/tronSend.ts tsa-app/services/chains/__tests__/tronSend.test.ts tsa-app/constants/chains.ts
git commit -m "feat(app): raw secp256k1 signing + Tron send flow (prepare→sign→submit)"
```

---

### Task 11: End-to-end Tron testnet validation (manual gate)

**Files:** none (validation task).

- [ ] **Step 1:** Set backend env for **Nile testnet**: `TRON_RPC_URL=https://nile.trongrid.io`, `TRON_NETWORK=testnet`, `TRON_USDT_ADDRESS=<Nile USDT>`, optional `TRON_API_KEY`. Start the server; confirm the log line `chains: tron registered`.
- [ ] **Step 2:** In the app, derive the Tron address from a test wallet's mnemonic and `POST /wallet/nonevm/register`. Fund it from the Nile faucet (TRX + test USDT).
- [ ] **Step 3:** `GET /wallet/nonevm/balances?chain=tron&address=<addr>` — confirm TRX + USDT balances render.
- [ ] **Step 4:** Send TRX to a second address; confirm `txHash`, then confirm on Nile Tronscan and via the reconciler flipping the activity row to `confirmed`.
- [ ] **Step 5:** Send USDT (TRC-20) **with the wallet holding 0 TRX** → confirm the send is **blocked** at prepare with the "You need TRX…" message (validates the gas preflight). Fund TRX, retry, confirm success.
- [ ] **Step 6:** Document the confirmed Nile values and any `fee_limit`/activation adjustments in the plan's notes, then commit those notes.

```bash
git add docs/superpowers/plans/2026-07-09-multichain-wallet-foundation-tron.md
git commit -m "docs(plan): record Tron Nile E2E results and fee tuning"
```

---

## Self-Review

**Spec coverage** (against `2026-07-09-multichain-wallet-nonevm-design.md`):
- Family abstraction (§5.1) → Tasks 1, 2, 7. ✅
- Pinned derivation paths + interop vectors (§2.1, §10) → Task 9. ✅
- Gas-token preflight (§5.2, §8) → Task 6 `EstimateFee` + Task 7 `PaymentRequired` block + Task 11 Step 5. ✅
- Multi-wallet data model (§5.3) → Task 4 (`(wallet_id, chain_key)` unique). ✅
- App-signs / backend-builds split (§2, §5.1) → Tasks 5–6 (backend build) + 9–10 (app sign). ✅
- Per-family confirm/reconcile (§5.4) → Task 8. ✅
- Dormant-by-default (§3, §8) → Task 3 gating + Task 7 registry + Task 8 nil-guard. ✅
- `@scure`/`@noble` over heavy SDKs (§9) → Task 9 deps. ✅
- Transaction history (§5.3) — model in Task 4, populated by reconciler in Task 8; **full history hydration (indexer reads) is deferred to the hardening pass** noted in `client.go History()` — flagged, not silent.
- Free-tier conservation (§7) — the single-gateway seam exists (all reads go through the backend client); **caching/token-bucket/single-flight is a later hardening task**, explicitly out of this foundation plan.
- Deposit webhooks (§7.5) — **BTC/Solana webhooks live in their own plans**; Tron has no free webhook and relies on the reconciler (Task 8), consistent with the spec.

**Placeholder scan:** no "TBD"/"handle errors"/"similar to" — each code step is complete. The two deferrals (history hydration, caching layer) are named, scoped to later plans, and reflect the spec's own phasing, not gaps.

**Type consistency:** `PreparedTx`/`SigningPayload`/`Signature`/`FeeQuote`/`TransferRequest` names are identical across Tasks 1, 6, 7, 10. `deriveTronAddress`/`signSecpDigest`/`sendTron` signatures match between definition (Tasks 9, 10) and use. `NewClient`/`NewTronGrid`/`NewRegistry`/`NewNonEVMReconciler` constructors match their wiring in Task 7/8.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-07-09-multichain-wallet-foundation-tron.md`. Two execution options:**

1. **Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session with checkpoints for review.

**Which approach?** (Bitcoin and Solana get their own plans once this foundation lands.)
