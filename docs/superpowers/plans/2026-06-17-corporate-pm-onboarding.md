# Corporate PM Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let corporates register as Payment Merchants — apply via `MerchantRequest`, admin approves, the backend provisions a Nomba sub-account + virtual account under TSA's parent, and the corporate's ads automatically become PM ads with balance-gated sell liquidity.

**Architecture:** Extend the existing PM engine (buy/webhook/release/caps reused unchanged) with: per-PM sub-account payout routing, an atomic per-PM NGN balance ledger gating sells (mirrors the crypto pool's atomic reserve), a resume-able Nomba provisioning state machine, and ad-tagging. All under TSA's single Nomba parent (one client, one webhook secret; PM keyed by `walletId`).

**Tech Stack:** Go 1.24, Gin, GORM/PostgreSQL (numeric columns for money), go-ethereum, the existing `NombaClient` (OAuth), `PMAutomationService`, `P2PTradeService`.

**Spec:** `docs/superpowers/specs/2026-06-17-corporate-pm-onboarding-design.md` (read it; this plan implements its §4–§9 + §13 phasing).

## Global Constraints

- **Money columns** are `numeric` in Postgres, carried as decimal **strings** in Go (never float) — match `p2p_trades.fiat_amount` (`numeric(38,2)`).
- **NGN amounts** are 2-dp; reuse `ngnToKobo`/`ngnEqual` style integer-kobo comparison (`internal/services/pm_automation_service.go`) — never float equality.
- **Atomic reservation pattern**: all balance compare-and-decrement must use a single conditional `UPDATE ... WHERE (available - reserved) >= :amt` and reject on `RowsAffected == 0`, inside the trade's DB transaction — mirror `p2p_trade_service.go:1402-1415` (the crypto pool).
- **Dormant-by-default**: missing Nomba/PM config must not hard-fail boot (CLAUDE.md rule).
- **Tests**: SQLite in-memory raw-DDL (see `internal/services/pm_service_test.go`); when adding columns, patch the raw test DDLs.
- **Gated**: the Nomba sub-account/VA/balance/transfer endpoint shapes are built against documented shapes + fakes; mark with `// TODO(nomba-facts)`; live after sandbox verification.
- **Custody:** ledger balances are TSA-owned partitions; the PM has a contractual claim (comment money-handling code accordingly).
- Pre-existing `internal/handlers` test failures (~31, DDL gaps in unrelated tests) are baseline — don't add new ones.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/models/payment_merchant.go` (modify) | New columns: `NombaSubAccountID`, settlement bank fields, `AvailableNGN`/`ReservedNGN`, `ProvisioningStatus`. |
| `internal/models/merchant_request.go` (modify) | Application fields: settlement bank, maker wallet, requested caps. |
| `internal/config/database.go` (modify) | AutoMigrate new columns + any check constraint. |
| `internal/services/pm_ngn_ledger.go` (new) | Atomic ledger ops: `ReserveNGN`/`ConsumeNGN`/`ReleaseNGN`/`CreditNGN`/`WithdrawNGN`. |
| `internal/services/nomba_client.go` (modify) | Sub-account API: `CreateSubAccount`, `CreateVirtualAccountForSubAccount`, `SetSubAccountSettlement`, `FetchSubAccountBalance`, `TransferFromSubAccount`. |
| `internal/services/pm_onboarding_service.go` (new) | Resume-able provision+activate state machine. |
| `internal/handlers/merchant_request_handler.go` (modify) | Approval → PM provisioning branch for `business_type='p2p_merchant'`. |
| `internal/services/pm_automation_service.go` (modify) | Credit NGN on inbound; consume/release on payout; route payout to sub-account; provisioning hook. |
| `internal/services/nomba_gateway.go` (modify) | `PayoutRequest.SubAccountID`; gateway uses transfer-from-sub-account. |
| `internal/services/p2p_trade_service.go` (modify) | Sell-open atomic NGN reserve; ad-tagging + maker-wallet validation in `CreateAd`/`createBuyAd`; release-on-cancel. |
| `internal/jobs/pm_ngn_reconciler.go` (new) | Periodic ledger-vs-Nomba drift check. |
| `internal/handlers/pm_handler.go` (modify) | PM withdrawal endpoint + (minimal) status/balance view. |
| `cmd/server/main.go` (modify) | Register the reconciler; wire onboarding service. |

---

## Task 1: Data model + atomic NGN ledger

**Files:**
- Modify: `internal/models/payment_merchant.go`, `internal/models/merchant_request.go`, `internal/config/database.go`
- Create: `internal/services/pm_ngn_ledger.go`, `internal/services/pm_ngn_ledger_test.go`

**Interfaces:**
- Produces: `(*PMService).ReserveNGN(ctx, pmID uuid.UUID, amountNGN string) (ok bool, err error)`, `.ConsumeNGN(ctx, pmID, amountNGN)`, `.ReleaseNGN(ctx, pmID, amountNGN)`, `.CreditNGN(ctx, pmID, amountNGN)`, `.WithdrawNGN(ctx, pmID, amountNGN) (ok bool, err error)`. All amounts are 2-dp decimal strings. `ok=false` on a reserve/withdraw that would exceed available.
- Produces model fields: `PaymentMerchant.{NombaSubAccountID, SettlementBankCode, SettlementAccountNumber, SettlementAccountName, AvailableNGN, ReservedNGN, ProvisioningStatus string}`; `MerchantRequest.{SettlementBankCode, SettlementAccountNumber, MakerWalletAddress string, RequestedPerTradeCapUSD, RequestedDailyCapUSD float64}`.

- [ ] **Step 1: Add model fields.** In `payment_merchant.go` add to `PaymentMerchant`: `NombaSubAccountID string \`gorm:"size:64;index"\``, `SettlementBankCode string \`gorm:"size:16"\``, `SettlementAccountNumber string \`gorm:"size:32"\``, `SettlementAccountName string \`gorm:"size:128"\``, `AvailableNGN string \`gorm:"type:numeric(38,2);not null;default:0"\``, `ReservedNGN string \`gorm:"type:numeric(38,2);not null;default:0"\``, `ProvisioningStatus string \`gorm:"size:16;not null;default:'pending'"\``. Add consts `PMProvisioningPending="pending"`, `PMProvisioningProvisioned="provisioned"`, `PMProvisioningActive="active"`, `PMProvisioningFailed="failed"`. In `merchant_request.go` add the application fields above (nullable/zero-default).

- [ ] **Step 2: Migrate.** In `database.go` `AutoMigrate` — the structs above auto-add columns. Add a check constraint (idempotent, like the existing ones): `ALTER TABLE payment_merchants ... CHECK (CAST(available_ngn AS NUMERIC) >= 0 AND CAST(reserved_ngn AS NUMERIC) >= 0 AND CAST(reserved_ngn AS NUMERIC) <= CAST(available_ngn AS NUMERIC))`. (DROP IF EXISTS first.)

- [ ] **Step 3: Write failing ledger test** (`pm_ngn_ledger_test.go`). SQLite raw DDL for `payment_merchants` (copy from `pm_service_test.go`, add the new columns). Tests:
```go
func TestReserveNGN_AtomicGate(t *testing.T) {
    db := newSQLiteForPM(t); svc := NewPMService(db)
    pm := &models.PaymentMerchant{UserID: uuid.New(), Status: "active", AvailableNGN: "1000.00", ReservedNGN: "0.00", InboundAccountNumber:"1"}
    db.Create(pm)
    ok, err := svc.ReserveNGN(context.Background(), pm.ID, "600.00")
    if err != nil || !ok { t.Fatalf("first reserve should succeed: ok=%v err=%v", ok, err) }
    ok2, _ := svc.ReserveNGN(context.Background(), pm.ID, "600.00")
    if ok2 { t.Fatal("second reserve exceeding available must fail") } // 600+600 > 1000
    var got models.PaymentMerchant; db.First(&got, "id = ?", pm.ID)
    if got.ReservedNGN != "600.00" && got.ReservedNGN != "600" { t.Fatalf("reserved=%s", got.ReservedNGN) }
}
func TestConsumeReleaseCreditNGN(t *testing.T) { /* credit→reserve→consume drops both; reserve→release drops reserved only */ }
func TestWithdrawNGN_BlockedBelowReserved(t *testing.T) { /* available 1000 reserved 800 → withdraw 300 fails, 200 ok */ }
```

- [ ] **Step 4: Run — FAIL.** `go test ./internal/services -run 'ReserveNGN|ConsumeReleaseCreditNGN|WithdrawNGN' -v` → undefined.

- [ ] **Step 5: Implement `pm_ngn_ledger.go`.** Each op is a single conditional UPDATE using SQL numeric casts (SQLite + Postgres both accept `CAST(x AS NUMERIC)`), mirroring the pool pattern:
```go
func (s *PMService) ReserveNGN(ctx context.Context, pmID uuid.UUID, amt string) (bool, error) {
    res := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
        Where("id = ? AND (CAST(available_ngn AS NUMERIC) - CAST(reserved_ngn AS NUMERIC)) >= CAST(? AS NUMERIC)", pmID, amt).
        Update("reserved_ngn", gorm.Expr("CAST(reserved_ngn AS NUMERIC) + CAST(? AS NUMERIC)", amt))
    if res.Error != nil { return false, res.Error }
    return res.RowsAffected == 1, nil
}
// ConsumeNGN: available -= amt AND reserved -= amt (guard reserved >= amt).
// ReleaseNGN: reserved -= amt (guard reserved >= amt).
// CreditNGN:  available += amt (unconditional).
// WithdrawNGN: available -= amt WHERE (available - reserved) >= amt → bool.
```
Validate `amt` parses as a non-negative 2-dp decimal first (reuse the `ngnToKobo` validation idea); reject otherwise.

- [ ] **Step 6: Run — PASS.** Then `go test ./internal/services -count=1`.

- [ ] **Step 7: Patch raw test DDLs** anywhere `payment_merchants` is created in tests (grep `CREATE TABLE payment_merchants`) to add the new columns; re-run `go test ./internal/services ./internal/handlers -count=1` — no new failures.

- [ ] **Step 8: Commit.** `git add internal/models/payment_merchant.go internal/models/merchant_request.go internal/config/database.go internal/services/pm_ngn_ledger.go internal/services/pm_ngn_ledger_test.go && git commit -m "feat(pm): NGN ledger columns + atomic reserve/consume/release/withdraw"`

---

## Task 2: Nomba sub-account client methods (GATED shapes)

**Files:** Modify `internal/services/nomba_client.go`; Test `internal/services/nomba_subaccount_test.go`.

**Interfaces:**
- Produces on `*NombaClient`: `CreateSubAccount(ctx, name string) (subAccountID string, err error)`, `CreateVirtualAccountForSubAccount(ctx, subAccountID, accountName string) (vaNumber, walletID string, err error)`, `SetSubAccountSettlement(ctx, subAccountID, bankCode, accountNumber string) error`, `FetchSubAccountBalance(ctx, subAccountID string) (ngn string, err error)`, `TransferFromSubAccount(ctx, subAccountID string, req NombaTransferRequest) (NombaTransferResult, error)`.

- [ ] **Step 1: Failing httptest.** Stand up `httptest` mocking `/v1/auth/token/issue` + the sub-account endpoints with the documented shapes (see the Nomba API reference; the paths in the `nomba-facts` memory). Assert each method parses the documented response (sub-account id, VA number + walletId, balance, transfer id). Pattern after the existing `nomba_gateway_http_test.go`.

- [ ] **Step 2: Run — FAIL.**

- [ ] **Step 3: Implement** each method via the existing `authedRequest(ctx, method, path, body)` (it adds bearer + parent `accountId`). For sub-account targeting, follow the Nomba doc (header or path param — `// TODO(nomba-facts)` on the exact mechanism). `TransferFromSubAccount` mirrors `Transfer` but targets the sub-account. Non-2xx / `status:false` → wrapped error; empty critical ids → error.

- [ ] **Step 4: Run — PASS;** `go build ./... && go test ./internal/services -run 'SubAccount|Transfer' -count=1`.

- [ ] **Step 5: Commit.** `feat(pm): Nomba sub-account provisioning + balance + transfer client methods`

---

## Task 3: PM onboarding service (resume-able provisioning)

**Files:** Create `internal/services/pm_onboarding_service.go` + test; Modify `internal/handlers/merchant_request_handler.go`.

**Interfaces:**
- Consumes: Task 1 model fields; Task 2 client methods.
- Produces: `PMOnboardingService.ProvisionAndActivate(ctx, req *models.MerchantRequest) (*models.PaymentMerchant, error)` — idempotent/resume-able; anchors on `NombaSubAccountID`.

- [ ] **Step 1: Failing test.** Fake Nomba (the Task 2 methods behind an interface so the test can inject failures). Cases:
  - happy: approve → row `active`, `nomba_sub_account_id`/VA/walletId/settlement set.
  - resume: sub-account created but VA-create fails on attempt 1 → row stays `pending` with sub-account id stored → attempt 2 does NOT re-create the sub-account, completes VA → `active`.
  - activation gates on all four steps.

- [ ] **Step 2: Run — FAIL.**

- [ ] **Step 3: Implement** the state machine per spec §5: upsert `payment_merchants` (`pending`, keyed on `user_id`) → if `NombaSubAccountID==""` create sub-account + store → if VA fields empty create VA + store → set settlement → flip `provisioning_status='provisioned'`, `status='active'`, set caps + maker wallet + settlement bank from the request. Each Nomba id persisted immediately after the call returns. Define a small `nombaProvisioner` interface (the Task 2 methods) for testability.

- [ ] **Step 4: Wire approval.** In `merchant_request_handler.go` `ApproveMerchantRequest`, when `req.BusinessType == "p2p_merchant"`, after the existing role upgrade call `pmOnboarding.ProvisionAndActivate(ctx, req)`; on error return a clear 5xx and leave the request approvable for retry (don't mark fully done). Thread the onboarding service into the handler (constructor param) + `cmd/server/main.go` construction.

- [ ] **Step 5: Run — PASS;** `go build ./...`, `go test ./internal/services -run 'Onboard|Provision' -count=1`.

- [ ] **Step 6: Commit.** `feat(pm): corporate onboarding service (resume-able Nomba provisioning) + approval wiring`

---

## Task 4: Per-PM NGN ledger lifecycle + reconciler

**Files:** Modify `internal/services/pm_automation_service.go`, `internal/services/p2p_trade_service.go`; Create `internal/jobs/pm_ngn_reconciler.go` + test; Modify `cmd/server/main.go`.

**Interfaces:**
- Consumes: Task 1 ledger ops; Task 2 `FetchSubAccountBalance`.

- [ ] **Step 1: Failing tests.** (a) `HandleInbound` on a settled credit calls `CreditNGN(pm, amount)` (assert `available_ngn` grew) — even when no trade matches (top-up). (b) `HandlePayoutStatus` success → `ConsumeNGN`; failed/cancel → `ReleaseNGN`. (c) reconciler: seed a PM whose ledger `available_ngn` differs from the fake Nomba balance → reconciler logs/flags drift (assert via a counter/return).

- [ ] **Step 2: Run — FAIL.**

- [ ] **Step 3: Implement.**
  - In `HandleInbound`, after resolving the PM by `walletId` and before/independent of matching, `s.pm.CreditNGN(ctx, pm.ID, p.AmountNGN)` (every settled inbound credits the ledger). Keep it idempotent against webhook dedup (the ledger credit must run exactly once per `requestId` — guard via the existing WebhookEvent dedup which already prevents re-dispatch).
  - In `HandlePayoutStatus`: success path → `ConsumeNGN(pm, fiat)` then `AutoRelease`; failed path → `ReleaseNGN(pm, fiat)` (+ existing alert/manual). Resolve `pm` + `fiat` from the trade.
  - The sell-open reservation lives in Task... actually do it here too: see Task 4 covers lifecycle, but the open-time reserve is in `openTradeWithDeposit` (Task 6 touches that file for ad-tagging) — to avoid two tasks editing the same function, **put the sell-open reserve here** in `openTradeWithDeposit`: when `ad.PMID != nil`, inside the existing capacity transaction call `ReserveNGN(pm, fiatPayout)`; `ok==false` → roll back + `ErrInsufficientPMLiquidity`. Release on cancel: in the cancel/timeout path (`ConfirmCancel` / the watchdog cancel), when the trade is a PM sell, `ReleaseNGN`.
- [ ] **Step 4: Reconciler job** (`pm_ngn_reconciler.go`) implementing the `jobs.Worker` interface (`Name`/`Interval`/`Run`, ~5m) — for each active PM, `FetchSubAccountBalance` vs `available_ngn`; log + metric on drift beyond a tolerance. Register in `main.go` when PM active (next to the recovery job).

- [ ] **Step 5: Run — PASS;** `go test ./internal/services ./internal/jobs -count=1`.

- [ ] **Step 6: Commit.** `feat(pm): NGN ledger lifecycle (credit/reserve/consume/release) + drift reconciler`

---

## Task 5: Per-PM payout routing (sub-account)

**Files:** Modify `internal/services/nomba_gateway.go`, `internal/services/nomba_gateway_http.go`, `internal/services/pm_automation_service.go`.

**Interfaces:**
- Consumes: Task 2 `TransferFromSubAccount`.
- Produces: `PayoutRequest.SubAccountID string`.

- [ ] **Step 1: Failing test.** Extend the sell-flow test: assert `InitiateSellPayout` resolves the trade's PM `nomba_sub_account_id` and the gateway debits THAT sub-account (the fake records the sub-account id), not the parent.
- [ ] **Step 2: Run — FAIL.**
- [ ] **Step 3: Implement.** Add `SubAccountID` to `PayoutRequest`. `NombaHTTPGateway.InitiatePayout` → if `SubAccountID != ""` call `client.TransferFromSubAccount(SubAccountID, ...)` else `client.Transfer(...)` (back-compat). `InitiateSellPayout` loads the trade→ad→PM and sets `req.SubAccountID = pm.NombaSubAccountID` (+ `SenderName` = pm.Name).
- [ ] **Step 4: Run — PASS;** `go test ./internal/services -count=1`.
- [ ] **Step 5: Commit.** `feat(pm): route sell payouts through the PM's Nomba sub-account`

---

## Task 6: Ad-tagging + maker-wallet validation

**Files:** Modify `internal/services/p2p_trade_service.go` (`CreateAd` ~498, `createBuyAd` ~678); Test `internal/services/pm_ad_tagging_test.go`.

**Interfaces:** Consumes: Task 1 (PM lookup by user_id + maker wallet field).

- [ ] **Step 1: Failing tests.** (a) active-PM user creates a sell-ad with sellerAddress == PM maker wallet → `ad.PMID` set. (b) sellerAddress != maker wallet → rejected (`ErrInvalidParams`). (c) non-PM user → `ad.PMID` nil. (d) buy-ad by PM with recipient == maker wallet → tagged.
- [ ] **Step 2: Run — FAIL.**
- [ ] **Step 3: Implement.** Early in `CreateAd`/`createBuyAd`, after resolving the seller/merchant: `pm, err := s.pmLookupByUser(ctx, sellerID)` (active only). If found: require the ad's seller/recipient address `== pm.OperatorWalletAddress` (case-insensitive) else `ErrInvalidParams`; set the new ad's `PMID = &pm.ID`. (Inject a small `pmLookup func(ctx, userID)(*PaymentMerchant,error)` into `P2PTradeService`, satisfied by `PMService.GetByUserID` — add that method.) Non-PM users: unchanged.
- [ ] **Step 4: Run — PASS;** `go test ./internal/services -count=1`.
- [ ] **Step 5: Commit.** `feat(pm): auto-tag active-PM users' ads as PM ads (+ maker-wallet validation)`

---

## Task 7: PM withdrawal endpoint

**Files:** Modify `internal/handlers/pm_handler.go`, `internal/routes/routes.go`; Test `pm_handler_test.go`.

**Interfaces:** Consumes: Task 1 `WithdrawNGN`; Task 2 `TransferFromSubAccount`.

- [ ] **Step 1: Failing handler test.** `POST /api/admin/pm/:id/withdraw {amount}` → atomic `WithdrawNGN`; success → Nomba transfer to settlement bank; below-exposure → 409. Role-gated (`RequireP2PAdmin`).
- [ ] **Step 2: Run — FAIL.**
- [ ] **Step 3: Implement.** Handler: parse pm id + amount; `WithdrawNGN(pm, amt)` → `ok==false` → 409 "exceeds available"; else `TransferFromSubAccount(pm.NombaSubAccountID, settlement bank, amt)`; on Nomba failure, compensate (`CreditNGN` back) + 5xx. Register the route in the `/admin/pm` group.
- [ ] **Step 4: Run — PASS;** `go build ./... && go test ./internal/handlers -run 'Withdraw|PM' -count=1`.
- [ ] **Step 5: Commit.** `feat(pm): admin PM withdrawal endpoint (atomic, settlement-bank payout)`

---

## Task 8: App surface (admin approval + PM status)

**Files:** `tsa-app` — `services/p2p.ts` (api), an admin PM-applications screen + a PM status/balance view, `app/admin/_layout.tsx`.

- [ ] **Step 1:** Add `pmAdminApi.listApplications/approve` + `pmStatus` calls to `services/p2p.ts` (the approve endpoint is the existing MerchantRequest approve; the status view reads the PM row incl. `availableNgn`/`reservedNgn`).
- [ ] **Step 2:** Admin screen: list `p2p_merchant` MerchantRequests → approve (triggers provisioning) → show result. Mirror `app/admin/p2p-disputes.tsx` (role gate, list, action, toast).
- [ ] **Step 3:** Minimal PM status view (status, VA number, available/reserved NGN, caps). Register both in `app/admin/_layout.tsx`.
- [ ] **Step 4:** `npm test` + `npx tsc --noEmit` clean on touched files.
- [ ] **Step 5: Commit.** `feat(pm app): corporate PM approval + status screens`

---

## Self-Review

**Spec coverage:** §4 model → T1; §5 provisioning → T3; §6 buy-credit → T4; §7 sell gate + payout routing → T4 (reserve/consume/release) + T5 (routing); §8 ad-tagging → T6; §9 withdrawal → T7; reconciler (§3) → T4; onboarding app → T8. Covered.

**Placeholder scan:** Nomba shapes are explicitly `// TODO(nomba-facts)` gated (a real constraint, not a placeholder); all other steps have concrete code/signatures or cite an exact existing pattern (`p2p_trade_service.go:1402-1415`).

**Type consistency:** ledger ops (`ReserveNGN`/`ConsumeNGN`/`ReleaseNGN`/`CreditNGN`/`WithdrawNGN`) named identically in T1 (produce) and T4/T7 (consume); `PayoutRequest.SubAccountID` defined T5, used T5; `NombaSubAccountID`/`OperatorWalletAddress`(maker) defined T1, used T3/T5/T6/T7; `GetByUserID` added T6.

**Cross-task file contention note:** `openTradeWithDeposit` is edited by T4 (NGN reserve) only; `CreateAd`/`createBuyAd` by T6 only — disjoint functions in the same file, sequence T4 before T6 to avoid rebase churn.
