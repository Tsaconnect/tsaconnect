# PM Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the off-chain automation layer that bridges Nomba fiat events ↔ the existing P2P escrow's two manual touchpoints (`markPaid`, `release`), so PM (Payment Merchant) trades settle automatically.

**Architecture:** PM trades are ordinary P2P trades (ad marked with `pm_id`). A webhook receiver ingests Nomba events, a `ReleaseSigner` seam signs `adminRelease` with the one platform admin EOA (key behind a swappable signer; AWS KMS in prod, local-key in dev, fake in tests), and a PM automation service orchestrates: buy = inbound-match → system `markPaid` → `adminRelease`; sell = JIT deposit → Nomba payout → `adminRelease` on payout success, cancel/refund on failure. Everything is built against interfaces with fakes so it is fully testable without live Nomba or KMS. The live Nomba HTTP adapter and AWS KMS signer are the final two tasks, gated on facts the user supplies.

**Tech Stack:** Go 1.24, Gin, GORM/PostgreSQL, go-ethereum (signing/broadcast), existing P2P escrow + trade services.

**Spec:** `docs/superpowers/specs/2026-06-15-pm-payment-merchant-design.md`

**Design decisions locked before this plan:**
- Release authority = **one platform admin EOA** registered on-chain via `addAdmin`; key held behind the `ReleaseSigner` seam (AWS KMS default). Per-PM "maker" wallets only fund the pool — no release authority. (Spec §2, §3.)
- Auto-release **only on a `settled` inbound** / `success` payout — never `pending`. (Spec §9.)
- Buy match = `MatchesPayerName(sender, kyc)` AND `amount == fiatAmount` AND exactly one open PM buy trade for that user; anything else → **manual-release queue**, never auto. (`pm_match.go` already built.)
- No contract change: `adminRelease(tradeId)` + `addAdmin`/`removeAdmin` + pause already deployed on V2; `PrepareAdminRelease` already in `p2p_escrow_service.go`.

**Already built (PM foundation, merged to main):** `models.PaymentMerchant`, `p2p_ads.pm_id`, `p2p_trades` payout columns, `MatchesPayerName` (`pm_match.go`), `NombaGateway` interface + DTOs (`nomba_gateway.go`), `AdFilter.PM` marketplace filter.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/services/release_signer.go` (new) | `ReleaseSigner` interface + `LocalKeyReleaseSigner` (dev) + `fakeReleaseSigner` (test). Signs+broadcasts the unsigned `adminRelease` tx. |
| `internal/services/p2p_trade_service.go` (modify) | Extract `confirmReleaseCore`; add `SystemMarkPaid` + `AutoRelease` (system-driven, no per-user identity gate). |
| `internal/services/pm_service.go` (new) | `PaymentMerchant` lookups (by inbound account, by id), TSA seed from config, cap checks. |
| `internal/services/nomba_webhook.go` (new) | `WebhookVerifier` seam (HMAC-SHA256 default) + payload parsers (`ParseInbound`/`ParsePayout`) + `WebhookEvent` ledger writer with dedup. |
| `internal/services/pm_automation_service.go` (new) | The bridge: `HandleInbound` (buy) + `HandlePayoutStatus` (sell) + `InitiateSellPayout` + manual-queue helpers. |
| `internal/handlers/nomba_webhook_handler.go` (new) | `POST /api/webhooks/nomba` → verify → ledger → dispatch. |
| `internal/handlers/pm_handler.go` (new) | Manual-release queue list + release/decline + sell-trade payout trigger (admin/PM-scoped). |
| `internal/routes/routes.go` (modify) | Register webhook + PM routes. |
| `internal/config/config.go` (modify) | PM/Nomba/admin-EOA env: `PM_ADMIN_EOA_ADDRESS`, `PM_RELEASE_SIGNER` (`local`/`kms`), `PM_RELEASE_PRIVATE_KEY` (dev), `NOMBA_WEBHOOK_SECRET`, TSA PM seed fields. |
| `cmd/server/main.go` (modify) | Construct signer + PM service + automation service; wire webhook dispatch. |
| `internal/services/nomba_gateway_http.go` (new, **GATED**) | Live Nomba HTTP adapter — payout + per-PM client. Built when Nomba API facts arrive. |
| `internal/services/release_signer_kms.go` (new, **GATED**) | AWS KMS `ReleaseSigner` impl. Built when KMS facts (ARN/region) arrive. |

Test files mirror each new `.go` with `_test.go`. All tests use the existing SQLite-raw-DDL pattern (see `p2p_trade_service_test.go`) — **when adding columns, also patch the raw test DDLs** (known regression class from PM foundation).

---

## Task 1: ReleaseSigner seam

**Files:**
- Create: `internal/services/release_signer.go`
- Test: `internal/services/release_signer_test.go`

The seam that turns an unsigned `adminRelease` tx (JSON `blockchain.UnsignedTx` from `PrepareAdminRelease`) into a broadcast tx hash, signing with the platform admin key. Default prod impl is AWS KMS (Task 10); `LocalKeyReleaseSigner` serves dev; `fakeReleaseSigner` drives tests.

- [ ] **Step 1: Write the failing test**

```go
package services

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// captures the raw signed tx the signer broadcasts.
type captureBroadcaster struct {
	chainID  *big.Int
	lastSent []byte
	hash     string
}

func (c *captureBroadcaster) ChainID() *big.Int { return c.chainID }
func (c *captureBroadcaster) SendRawTransaction(signed []byte) (string, error) {
	c.lastSent = signed
	return c.hash, nil
}

func TestLocalKeyReleaseSigner_SignsAndBroadcasts(t *testing.T) {
	key, _ := crypto.GenerateKey()
	wantAddr := crypto.PubkeyToAddress(key.PublicKey)
	bc := &captureBroadcaster{chainID: big.NewInt(146), hash: "0xabc"}
	signer := NewLocalKeyReleaseSigner(key, bc)

	if signer.Address() != wantAddr.Hex() {
		t.Fatalf("Address() = %s, want %s", signer.Address(), wantAddr.Hex())
	}

	unsigned, _ := json.Marshal(blockchain.UnsignedTx{
		Nonce: 7, GasPrice: "1000000000", GasLimit: 90000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0",
		Data: "abcdef", ChainID: "146",
	})
	hash, err := signer.SignAndBroadcast(context.Background(), unsigned)
	if err != nil {
		t.Fatalf("SignAndBroadcast: %v", err)
	}
	if hash != "0xabc" {
		t.Fatalf("hash = %s, want 0xabc", hash)
	}
	// Decode what was broadcast and recover the sender — must be the admin EOA.
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(bc.lastSent, tx); err != nil {
		t.Fatalf("decode broadcast tx: %v", err)
	}
	from, err := types.Sender(types.NewEIP155Signer(big.NewInt(146)), tx)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if from != wantAddr {
		t.Fatalf("recovered sender %s, want admin %s", from.Hex(), wantAddr.Hex())
	}
	if tx.Nonce() != 7 || tx.To().Hex() != common.HexToAddress("0x000000000000000000000000000000000000dEaD").Hex() {
		t.Fatalf("tx fields mismatch: nonce=%d to=%s", tx.Nonce(), tx.To())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/services -run TestLocalKeyReleaseSigner -v`
Expected: FAIL — `NewLocalKeyReleaseSigner`/`ReleaseSigner` undefined.

- [ ] **Step 3: Write the implementation**

```go
package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// ReleaseSigner signs and broadcasts the unsigned adminRelease tx produced by
// P2PEscrowService.PrepareAdminRelease with the ONE platform admin EOA key. The
// key never lives in the DB. Prod = AWS KMS (release_signer_kms.go); dev =
// LocalKeyReleaseSigner; tests = fakeReleaseSigner. See spec §3 (ReleaseSigner seam).
type ReleaseSigner interface {
	// Address is the admin EOA address — the `from` passed to PrepareAdminRelease.
	Address() string
	// SignAndBroadcast signs the JSON-encoded blockchain.UnsignedTx and broadcasts
	// it, returning the tx hash.
	SignAndBroadcast(ctx context.Context, unsignedTx []byte) (txHash string, err error)
}

// txBroadcaster is the slice of *blockchain.EVMClient the signer needs.
type txBroadcaster interface {
	SendRawTransaction(signedTx []byte) (string, error)
	ChainID() *big.Int
}

// LocalKeyReleaseSigner holds the admin key in process. Dev/staging only —
// prod uses KMS. Swappable behind ReleaseSigner so call sites never change.
type LocalKeyReleaseSigner struct {
	key *ecdsa.PrivateKey
	bc  txBroadcaster
}

func NewLocalKeyReleaseSigner(key *ecdsa.PrivateKey, bc txBroadcaster) *LocalKeyReleaseSigner {
	return &LocalKeyReleaseSigner{key: key, bc: bc}
}

func (s *LocalKeyReleaseSigner) Address() string {
	return crypto.PubkeyToAddress(s.key.PublicKey).Hex()
}

func (s *LocalKeyReleaseSigner) SignAndBroadcast(ctx context.Context, unsignedTx []byte) (string, error) {
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsignedTx, &u); err != nil {
		return "", fmt.Errorf("decode unsigned tx: %w", err)
	}
	gasPrice, ok := new(big.Int).SetString(u.GasPrice, 10)
	if !ok {
		return "", fmt.Errorf("bad gasPrice %q", u.GasPrice)
	}
	value, ok := new(big.Int).SetString(u.Value, 10)
	if !ok {
		value = big.NewInt(0)
	}
	chainID, ok := new(big.Int).SetString(u.ChainID, 10)
	if !ok {
		chainID = s.bc.ChainID()
	}
	data, err := hexutil.Decode("0x" + u.Data)
	if err != nil {
		return "", fmt.Errorf("decode call data: %w", err)
	}
	to := common.HexToAddress(u.To)
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    u.Nonce,
		GasPrice: gasPrice,
		Gas:      u.GasLimit,
		To:       &to,
		Value:    value,
		Data:     data,
	})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(chainID), s.key)
	if err != nil {
		return "", fmt.Errorf("sign tx: %w", err)
	}
	raw, err := rlp.EncodeToBytes(signed)
	if err != nil {
		return "", fmt.Errorf("rlp encode: %w", err)
	}
	return s.bc.SendRawTransaction(raw)
}
```

- [ ] **Step 4: Add the test fake (same file, after the impl)**

```go
// fakeReleaseSigner records calls for tests and returns a canned hash.
type fakeReleaseSigner struct {
	addr   string
	hash   string
	calls  [][]byte
	err    error
}

func (f *fakeReleaseSigner) Address() string { return f.addr }
func (f *fakeReleaseSigner) SignAndBroadcast(_ context.Context, tx []byte) (string, error) {
	f.calls = append(f.calls, tx)
	if f.err != nil {
		return "", f.err
	}
	return f.hash, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/services -run TestLocalKeyReleaseSigner -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/services/release_signer.go internal/services/release_signer_test.go
git commit -m "feat(pm): ReleaseSigner seam + local-key impl"
```

---

## Task 2: System-driven trade transitions (SystemMarkPaid + AutoRelease)

**Files:**
- Modify: `internal/services/p2p_trade_service.go` (extract `confirmReleaseCore` from `ConfirmRelease:1636`; add `SystemMarkPaid`, `AutoRelease`)
- Test: `internal/services/pm_auto_release_test.go` (new)

The seller-facing `MarkPaid`/`PrepareRelease`/`ConfirmRelease` are gated on `buyerID`/`sellerID`. PM automation is system-driven, so add system variants. `AutoRelease` reuses the exact release-confirm body (money-invariant + `accrueP2PReleaseFee` + ad-capacity drain + emits) by extracting it into an unexported `confirmReleaseCore`.

- [ ] **Step 1: Refactor — extract `confirmReleaseCore`**

In `ConfirmRelease` (line 1636), keep the identity check + status guard + `resolve` + `VerifyTradeReleased`, then delegate the post-verify body (lines 1666–1735) to a new method. Add:

```go
// confirmReleaseCore performs the on-chain-verified settlement: money-invariant
// check, DB transition paid|releasing → released, ad-capacity drain, fee accrual,
// emits. auditActor is the audit's actor pointer — &sellerID for the manual path,
// nil for the system/automation path (audit takes *uuid.UUID). ev is the verified
// on-chain release event. NOTE: the user-facing emits inside the body go to the
// real trade parties (trade.SellerID / trade.BuyerID), NOT auditActor — emit
// takes a value uuid.UUID, so do not pass nil there.
func (s *P2PTradeService) confirmReleaseCore(ctx context.Context, trade *models.P2PTrade, txHash string, auditActor *uuid.UUID, ev *P2PTradeReleasedEvent) (*models.P2PTrade, error) {
	// ... lines 1666–1735 verbatim, with:
	//   - s.audit(..., &sellerID, ...) → s.audit(..., auditActor, ...)
	//   - s.emit(..., sellerID, ...)   → s.emit(..., trade.SellerID, ...)
	//   - s.emit(..., trade.BuyerID, ...) unchanged
}
```

`ConfirmRelease` becomes: identity/status guard → `resolve` → `VerifyTradeReleased` → `return s.confirmReleaseCore(ctx, &trade, txHash, &sellerID, ev)`. (`VerifyTradeReleased` returns `*P2PTradeReleasedEvent` — `internal/services/p2p_escrow_service.go:709`.)

- [ ] **Step 2: Write the failing test**

```go
func TestAutoRelease_SystemPath(t *testing.T) {
	svc, db := newTradeServiceForTest(t) // existing test harness in p2p_trade_service_test.go
	// seed an ad + a paid PM trade with OpenTxHash set (helper from existing tests)
	trade := seedPaidTrade(t, db /* ...pm ad... */)

	signer := &fakeReleaseSigner{addr: "0xAdm1n0000000000000000000000000000000000", hash: "0xrelease"}
	// fake escrow VerifyTradeReleased to return a matching ev (existing fakeEscrow harness)

	out, err := svc.AutoRelease(context.Background(), trade.ID, signer)
	if err != nil {
		t.Fatalf("AutoRelease: %v", err)
	}
	if out.Status != models.P2PTradeStatusReleased {
		t.Fatalf("status = %s, want released", out.Status)
	}
	if len(signer.calls) != 1 {
		t.Fatalf("signer called %d times, want 1", len(signer.calls))
	}
	if out.ReleaseTxHash != "0xrelease" {
		t.Fatalf("release tx = %s", out.ReleaseTxHash)
	}
}

func TestSystemMarkPaid_FlipsPendingToPaid(t *testing.T) {
	svc, db := newTradeServiceForTest(t)
	trade := seedOpenedTrade(t, db) // pending_payment, OpenTxHash set
	out, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1")
	if err != nil {
		t.Fatalf("SystemMarkPaid: %v", err)
	}
	if out.Status != models.P2PTradeStatusPaid {
		t.Fatalf("status = %s, want paid", out.Status)
	}
	if out.ProofImageURL != "nomba:evt_1" {
		t.Fatalf("proof marker = %s", out.ProofImageURL)
	}
}
```

(Use the existing test scaffolding names from `p2p_trade_service_test.go` — confirm helper names before writing; adapt if they differ.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/services -run 'TestAutoRelease_SystemPath|TestSystemMarkPaid' -v`
Expected: FAIL — `AutoRelease`/`SystemMarkPaid` undefined.

- [ ] **Step 4: Implement `SystemMarkPaid`**

```go
// SystemMarkPaid flips a PM trade pending_payment → paid on a verified inbound
// fiat credit (no proof image, no buyer identity gate — the Nomba match IS the
// proof). ref is a provenance marker, e.g. "nomba:<eventID>". Idempotent: a
// trade already past pending_payment returns ErrInvalidTransition for the caller
// to treat as already-handled.
func (s *P2PTradeService) SystemMarkPaid(ctx context.Context, tradeID uuid.UUID, ref string) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	if strings.TrimSpace(trade.OpenTxHash) == "" {
		return nil, fmt.Errorf("%w: trade not yet opened on-chain", ErrInvalidTransition)
	}
	now := s.clock.Now()
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status = ?", trade.ID, models.P2PTradeStatusPendingPayment).
		Updates(map[string]any{
			"status": models.P2PTradeStatusPaid, "marked_paid_at": now,
			"proof_image_url": ref, "updated_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrInvalidTransition
	}
	trade.Status = models.P2PTradeStatusPaid
	trade.MarkedPaidAt = &now
	trade.ProofImageURL = ref
	s.audit(ctx, models.P2PAuditTradeMarkedPaid, nil, &trade.AdID, &trade.ID, map[string]any{"system": ref})
	s.systemMsg(ctx, trade.ID, "Payment received and verified automatically. Releasing crypto.")
	return &trade, nil
}
```

- [ ] **Step 5: Implement `AutoRelease`**

```go
// AutoRelease settles a PM trade via the platform admin EOA: flip → releasing
// (system-driven), build the unsigned adminRelease tx, sign+broadcast through
// the ReleaseSigner, verify the on-chain event, and confirm. Reuses
// confirmReleaseCore for the money-invariant + fee accrual + emits. Accepts
// pending_payment|paid|releasing (mirrors PrepareRelease's relaxed gate); a
// concurrent dispute that wins the optimistic lock yields ErrInvalidTransition.
func (s *P2PTradeService) AutoRelease(ctx context.Context, tradeID uuid.UUID, signer ReleaseSigner) (*models.P2PTrade, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	if trade.Status == models.P2PTradeStatusReleased {
		return &trade, nil // idempotent
	}
	if trade.Status != models.P2PTradeStatusPendingPayment &&
		trade.Status != models.P2PTradeStatusPaid &&
		trade.Status != models.P2PTradeStatusReleasing {
		return nil, fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, trade.Status)
	}
	escrow, _, _, err := s.resolve(trade.ChainID)
	if err != nil {
		return nil, err
	}
	// Flip → releasing first (optimistic lock vs dispute), like PrepareRelease.
	if trade.Status != models.P2PTradeStatusReleasing {
		res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ? AND status IN ?", trade.ID,
				[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid}).
			Updates(map[string]any{"status": models.P2PTradeStatusReleasing, "updated_at": s.clock.Now()})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, ErrInvalidTransition
		}
	}
	unsignedTx, err := escrow.PrepareAdminRelease(HashID(trade.ID), signer.Address())
	if err != nil {
		return nil, fmt.Errorf("prepare admin release: %w", err)
	}
	txHash, err := signer.SignAndBroadcast(ctx, unsignedTx)
	if err != nil {
		return nil, fmt.Errorf("sign+broadcast admin release: %w", err)
	}
	ev, err := escrow.VerifyTradeReleased(txHash, HashID(trade.ID))
	if err != nil {
		return nil, fmt.Errorf("verify on-chain release: %w", err)
	}
	// System/automation path: nil audit actor (audit takes *uuid.UUID). The
	// user-facing emits inside confirmReleaseCore go to trade.SellerID/BuyerID.
	return s.confirmReleaseCore(ctx, &trade, txHash, nil, ev)
}
```

(When wiring `confirmReleaseCore` emits, pass `actorID == uuid.Nil` through to `emit`/`audit` as a nil pointer where they take `*uuid.UUID`. Confirm the exact signatures and adjust.)

- [ ] **Step 6: Patch raw test DDLs if any new columns** — none added here; skip.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/services -run 'TestAutoRelease|TestSystemMarkPaid|TestConfirmRelease' -v`
Expected: PASS (including the pre-existing `ConfirmRelease` tests, proving the refactor is behavior-preserving).

- [ ] **Step 8: Commit**

```bash
git add internal/services/p2p_trade_service.go internal/services/pm_auto_release_test.go
git commit -m "feat(pm): system-driven SystemMarkPaid + AutoRelease (admin EOA)"
```

---

## Task 3: PaymentMerchant service

**Files:**
- Create: `internal/services/pm_service.go`
- Test: `internal/services/pm_service_test.go`

Lookups + cap checks + TSA seed. Keep it thin — no Nomba/release logic here.

- [ ] **Step 1: Failing test**

```go
func TestPMService_GetByInboundAccount(t *testing.T) {
	db := newSQLiteForPM(t) // raw DDL for payment_merchants
	svc := NewPMService(db)
	pm := &models.PaymentMerchant{UserID: uuid.New(), Name: "TSA", Status: models.PaymentMerchantStatusActive,
		InboundAccountNumber: "1234567890", InboundBank: "Nomba", PerTradeCapUSD: 1000, DailyCapUSD: 5000}
	if err := db.Create(pm).Error; err != nil { t.Fatal(err) }

	got, err := svc.GetByInboundAccount(context.Background(), "1234567890")
	if err != nil { t.Fatalf("GetByInboundAccount: %v", err) }
	if got.ID != pm.ID { t.Fatalf("wrong PM") }

	if _, err := svc.GetByInboundAccount(context.Background(), "0000000000"); !errors.Is(err, ErrPMNotFound) {
		t.Fatalf("want ErrPMNotFound, got %v", err)
	}
}

func TestPMService_WithinCaps(t *testing.T) {
	pm := &models.PaymentMerchant{PerTradeCapUSD: 100, DailyCapUSD: 0}
	if !WithinPerTradeCap(pm, 100) { t.Fatal("100 should be within per-trade cap 100") }
	if WithinPerTradeCap(pm, 100.01) { t.Fatal("100.01 should exceed per-trade cap 100") }
	pm0 := &models.PaymentMerchant{PerTradeCapUSD: 0}
	if !WithinPerTradeCap(pm0, 1e9) { t.Fatal("0 cap = unlimited") }
}
```

- [ ] **Step 2: Run — FAIL** (`go test ./internal/services -run TestPMService -v`).

- [ ] **Step 3: Implement**

```go
package services

import (
	"context"
	"errors"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var ErrPMNotFound = errors.New("payment merchant not found")

type PMService struct{ db *gorm.DB }

func NewPMService(db *gorm.DB) *PMService { return &PMService{db: db} }

func (s *PMService) GetByInboundAccount(ctx context.Context, acct string) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).First(&pm, "inbound_account_number = ?", acct).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	return &pm, err
}

func (s *PMService) GetByID(ctx context.Context, id uuid.UUID) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).First(&pm, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	return &pm, err
}

// WithinPerTradeCap: 0 cap = unlimited; otherwise usd must be <= cap.
func WithinPerTradeCap(pm *models.PaymentMerchant, usd float64) bool {
	return pm.PerTradeCapUSD == 0 || usd <= pm.PerTradeCapUSD
}
```

Add a `SeedTSA(ctx, cfg)` that upserts one active PM row from config fields (idempotent on `inbound_account_number`). Daily-cap enforcement (running sum of today's PM trades) is a helper `WithinDailyCap(ctx, pm, usd)` querying released+open PM trades since midnight; if the schema lacks a clean USD column, sum `fiat_amount`/rate — keep it simple and documented; 0 = unlimited.

- [ ] **Step 4: Run — PASS.**

- [ ] **Step 5: Commit** `feat(pm): PaymentMerchant service (lookups, caps, TSA seed)`.

---

## Task 4: Nomba webhook verification + parsing + ledger

**Files:**
- Create: `internal/services/nomba_webhook.go`
- Test: `internal/services/nomba_webhook_test.go`

Signature seam + payload→DTO parsers + `WebhookEvent` writer with dedup. Default verifier is HMAC-SHA256 over the raw body (the common Nomba scheme; swappable when facts confirm).

- [ ] **Step 1: Failing test**

```go
func TestHMACVerifier(t *testing.T) {
	v := NewHMACWebhookVerifier("topsecret")
	body := []byte(`{"event":"payment_success"}`)
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	if !v.Verify(body, sig) { t.Fatal("valid sig rejected") }
	if v.Verify(body, "deadbeef") { t.Fatal("bad sig accepted") }
}

func TestRecordWebhookEvent_Dedup(t *testing.T) {
	db := newSQLiteForWebhook(t)
	w := NewNombaWebhookStore(db)
	body := []byte(`{"id":"evt_1"}`)
	first, dup, err := w.Record(context.Background(), "evt_1", body, "sig")
	if err != nil || dup { t.Fatalf("first record should not be dup: dup=%v err=%v", dup, err) }
	_ = first
	_, dup2, err := w.Record(context.Background(), "evt_1", body, "sig")
	if err != nil { t.Fatal(err) }
	if !dup2 { t.Fatal("second record with same event id should be dup") }
}
```

- [ ] **Step 2: Run — FAIL.**

- [ ] **Step 3: Implement** `WebhookVerifier` interface + `HMACWebhookVerifier`, `NombaWebhookStore.Record(ctx, eventID, body, sig) (event *models.WebhookEvent, dup bool, err error)` (insert with `provider="nomba"`; rely on the unique `(provider,event_id)` index — on conflict return `dup=true`), and `ParseInbound(body) (*InboundPayment, error)` / `ParsePayout(body) (*PayoutStatus, error)` mapping Nomba's JSON to the DTOs in `nomba_gateway.go`. **Parser field mapping is GATED on Nomba facts** — implement against a documented assumed shape now, with a `// TODO(nomba-facts)` and a single place to adjust. Tests use the assumed shape.

- [ ] **Step 4: Run — PASS. Step 5: Commit** `feat(pm): Nomba webhook verify + parse + dedup ledger`.

---

## Task 5: PM automation — buy flow

**Files:**
- Create: `internal/services/pm_automation_service.go`
- Test: `internal/services/pm_automation_buy_test.go`

`HandleInbound(ctx, InboundPayment)`: only act on `Status=="settled"`; resolve PM by `DestinationAccount`; find candidate `pending_payment` PM buy trades on that PM's ads with `fiat_amount == AmountNGN`; for each, load buyer KYC name and test `MatchesPayerName(p.SenderAccountName, kycName)`; **exactly one** match → `SystemMarkPaid(ref="nomba:"+EventID)` → `AutoRelease(signer)`; **zero or >1** → leave for manual queue (record nothing destructive; the trade stays `pending_payment`, surfaced by Task 7's query). Enforce caps + PM `IsActive()` before releasing. Idempotent per `EventID` (the webhook ledger dedup in Task 4 is the outer guard; also guard against re-release via `AutoRelease`'s released-idempotency).

- [ ] **Step 1: Failing test** — table of cases: exact single match → released + signer called once; name mismatch → trade stays pending, signer not called; two same-amount trades, one name-matches → released; two name+amount matches → ambiguous, none released; `Status="pending"` → no-op. Use `fakeReleaseSigner` + the SQLite trade harness.

- [ ] **Step 2–4: Implement → green.** Service struct holds `db`, `*PMService`, `*P2PTradeService`, `ReleaseSigner`, and a KYC-name lookup (function or small interface over `users`). Keep matching logic pure and unit-tested.

- [ ] **Step 5: Commit** `feat(pm): buy-flow automation (inbound match → markPaid → adminRelease)`.

---

## Task 6: PM automation — sell flow

**Files:**
- Modify: `internal/services/pm_automation_service.go` (+ `pm_automation_sell_test.go`)

`InitiateSellPayout(ctx, tradeID)`: precondition — sell trade with user's KYC-name-verified destination bank (`payout_account_*` set, deposit confirmed). Call `NombaGateway.InitiatePayout`; store `nomba_payout_id`. `HandlePayoutStatus(ctx, PayoutStatus)`: `success` → `AutoRelease(signer)` (token → PM); `failed` (terminal) → cancel via existing cancel path → deposited token returns to user + alert; `pending` → no-op. Idempotent per payout `EventID`.

- [ ] **Step 1: Failing test** — success → released + signer called; failed → trade cancelled, signer not called; pending → no-op; double success webhook → released once (idempotent). Use a `fakeNombaGateway` implementing `NombaGateway`.

- [ ] **Step 2–4: Implement → green. Step 5: Commit** `feat(pm): sell-flow automation (payout → adminRelease / cancel on fail)`.

---

## Task 7: Manual-release queue + PM/admin handlers

**Files:**
- Create: `internal/handlers/pm_handler.go`, `internal/handlers/pm_handler_test.go`
- Modify: `internal/routes/routes.go`

Endpoints (admin/PM-scoped, behind existing admin/merchant middleware):
- `GET /api/admin/pm/manual-queue` — `pending_payment` PM buy trades whose inbound is unresolved (no matched `nomba_inbound_event_id`), with the inbound candidates surfaced for the operator.
- `POST /api/admin/pm/trades/:id/release` — operator confirms a human-verified payment → `SystemMarkPaid` + `AutoRelease`.
- `POST /api/admin/pm/trades/:id/decline` — operator rejects → leave to timeout watchdog (no state change) or explicit cancel; document which.
- `POST /api/admin/pm/trades/:id/payout` — trigger `InitiateSellPayout` for a sell trade (manual kick if the auto path didn't fire).

- [ ] **Step 1: Failing handler test** (httptest + SQLite) for the queue list + release action. **Step 2–4: Implement → green.** **Step 5:** register routes. **Step 6: Commit** `feat(pm): manual-release queue + admin/PM release endpoints`.

---

## Task 8: Webhook route + wiring

**Files:**
- Create: `internal/handlers/nomba_webhook_handler.go`, `internal/handlers/nomba_webhook_handler_test.go`
- Modify: `internal/routes/routes.go`, `cmd/server/main.go`, `internal/config/config.go`

`POST /api/webhooks/nomba`: read raw body, `WebhookVerifier.Verify(body, header-sig)` (reject 401 on fail), `NombaWebhookStore.Record` (dup → 200 no-op), classify event (inbound vs payout) and dispatch to `HandleInbound`/`HandlePayoutStatus` (async/best-effort; the ledger + reconciler are the safety net). Config: `PM_ADMIN_EOA_ADDRESS`, `PM_RELEASE_SIGNER` (`local`|`kms`, default `local` in dev / required `kms` in prod), `PM_RELEASE_PRIVATE_KEY` (dev only — boot error if set in prod), `NOMBA_WEBHOOK_SECRET`, TSA seed fields. In `main.go`: build the signer (local from key in dev; KMS in prod — Task 10), construct `PMService` + `PMAutomationService`, seed TSA PM, register the webhook route. Keep dormant-by-default: if `NOMBA_WEBHOOK_SECRET`/signer unconfigured, the route returns a clear error and the server still boots (per CLAUDE.md dormant-integration rule).

- [ ] **Steps:** failing handler test (valid sig → 200 + dispatch; bad sig → 401; dup event → 200 no-op) → implement → wire `main.go` → `go build ./... && go test ./... -count=1` green → **Commit** `feat(pm): Nomba webhook route + automation wiring`.

---

## Task 9 (GATED — needs Nomba API facts): live NombaGateway HTTP adapter

**Files:** `internal/services/nomba_gateway_http.go` (+ test)

Implement `NombaGateway` against the live API: OAuth2 per-PM client (extend `nomba_client.go`'s `PerPMClient`), `InitiatePayout` (real endpoint), and finalize the Task 4 parsers to Nomba's true payload. **Do not build until the user provides:** inbound webhook payload fields (payer name/number, destination account, amount, status, event id), payout endpoint + status webhook shape, signature scheme + header name, sandbox creds. Until then the fakes + assumed-shape parsers stand in. Mark with `// TODO(nomba-facts)`.

- [ ] Build when facts arrive; replace assumed shapes; integration-test against sandbox; commit.

---

## Task 10 (GATED — needs KMS facts): AWS KMS ReleaseSigner

**Files:** `internal/services/release_signer_kms.go` (+ test)

Implement `ReleaseSigner` backed by AWS KMS (`asymmetric ECC_SECG_P256K1` sign; assemble the secp256k1 signature, recover `v`, build the signed tx, broadcast). **Do not build until the user provides:** KMS key ARN + region + IAM access (or chosen vendor if not AWS). `LocalKeyReleaseSigner` covers dev until then. Selected via `PM_RELEASE_SIGNER=kms`.

- [ ] Build when facts arrive; verify recovered address == `PM_ADMIN_EOA_ADDRESS`; commit.

---

## Final review

After Tasks 1–8: dispatch a final code-review over the whole branch diff (`code-review --effort high`), fix findings, then use `superpowers:finishing-a-development-branch`. Tasks 9–10 ship later as their own small PRs when the gating facts land.

## Self-review notes
- **Spec coverage:** buy (§5) = Tasks 4,5,8; sell (§6) = Tasks 4,6,8; release-auth (§2,§3) = Tasks 1,2,10; manual queue (§9) = Task 7; caps/pause (§9) = Tasks 3,5,6; webhook+dedup (§7) = Tasks 4,8; onboarding seed (§8) = Task 3. Covered.
- **Type consistency:** `ReleaseSigner` (Task 1) used identically in Tasks 2,5,6,8,10. `NombaGateway`/DTOs reused from `nomba_gateway.go`. `confirmReleaseCore` signature fixed once in Task 2 and reused by `ConfirmRelease` + `AutoRelease`.
- **Gating is explicit:** the only live-fact-dependent code (Nomba HTTP, KMS) is isolated in Tasks 9–10; everything else is testable now with fakes.
