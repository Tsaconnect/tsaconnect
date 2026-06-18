# smoke-pm-e2e.js — PM Lifecycle E2E Smoke Test

## Overview

End-to-end driver for the Payment Merchant (PM) lifecycle of TSA Connect.
It exercises every major path in the PM automation stack:

* Onboarding (merchant-request → admin approval → Nomba VA provisioning)
* **Buy flow** (PM sells crypto → counterparty pays fiat → inbound webhook → auto-release)
* **Sell flow** (PM buys crypto → counterparty deposits → admin kicks payout → payout webhook → release)
* Withdraw (admin sweeps PM's available NGN to settlement bank)
* Verify (final summary table of statuses, balances, on-chain pool)
* Cleanup (psql deletes all rows created by the run, by tracked IDs)

State is persisted to `.pm-e2e-state.json` so each phase can run independently.
The file is removed by the `cleanup` phase.

---

## Prerequisites

| Requirement | Where |
|---|---|
| Node.js ≥ 18 | `node --version` |
| `ethers` in `smart-contracts/node_modules` | `cd smart-contracts && npm install` |
| `tsa-api-go/.env` containing `DATABASE_URL`, `JWT_SECRET`, `NOMBA_WEBHOOK_SECRET` | copy from `.env.example` |
| `smart-contracts/.env` containing `DEPLOYER_PRIVATE_KEY`, `SONIC_TESTNET_RPC` | copy from `.env.example` |
| TSA API running and reachable at `API_BASE` (default `https://tsa.mcgpchain.com/api`) | |
| Deployer wallet (`0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9`) funded on Sonic testnet (S for gas + some USDT to seed pool) | |
| `psql` installed and in `$PATH` | `which psql` |

---

## How to run

Run from the `smart-contracts/` directory:

```bash
cd /root/dev/tsa-dev/smart-contracts

# Full run (all phases in order):
node scripts/smoke-pm-e2e.js all

# Or phase-by-phase:
node scripts/smoke-pm-e2e.js seed
node scripts/smoke-pm-e2e.js onboard
node scripts/smoke-pm-e2e.js buy
node scripts/smoke-pm-e2e.js sell
node scripts/smoke-pm-e2e.js withdraw
node scripts/smoke-pm-e2e.js verify
node scripts/smoke-pm-e2e.js cleanup

# Override API base (e.g. local dev):
API_BASE=http://localhost:5000/api node scripts/smoke-pm-e2e.js all

# Verbose errors:
DEBUG=1 node scripts/smoke-pm-e2e.js buy
```

Phases are idempotent: re-running `seed` does an ON CONFLICT upsert;
re-running `onboard` skips an already-submitted request and retries approval.

---

## Phases

### 1. `seed`
Inserts 3 fixed test users by known UUIDs (idempotent `ON CONFLICT DO UPDATE`):

| UUID suffix | Email | Role |
|---|---|---|
| `…000000000001` | `e2e-pm@example.test` | `user` (PM applicant) |
| `…000000000002` | `e2e-counter@example.test` | `user` (trading counterparty) |
| `…000000000003` | `e2e-admin@example.test` | `super_admin` |

Forges HS256 JWTs for all three and saves them (not printed) to the state file.

**Output:** `✓ upserted user … (×3)`, `✓ JWTs forged`

---

### 2. `onboard`
* PM applicant posts `POST /api/merchant-requests` with `businessType=p2p_merchant`.
  Settlement bank = Nomba sandbox test values (see **Nomba sandbox note** below).
  MakerWalletAddress = deployer address (so the deployer can fund the pool).
* Admin posts `POST /api/admin/merchant-requests/:id/approve`.
  This triggers `PMOnboardingService.ProvisionAndActivate` → Nomba VA creation.
* Polls `GET /api/admin/pm` until the PM row for `pm-<PM_USER_ID>` has `status=active`.

Saves to state: `pmId`, `pmAccountRef` (= `pm-<PM_USER_ID>`), `pmInboundAccount`.

**Output:** PM id, accountRef, inboundAccountNumber, status.

**Dependency:** `seed`

---

### 3. `buy` — counterparty buys crypto from PM
PM is the *seller*; counterparty pays fiat via Nomba.

1. PM creates a payment method.
2. Deployer wallet approves USDT → deposits 2 USDT into the on-chain pool (`POST /p2p/deposits/approve` → broadcast → `POST /p2p/deposits` → broadcast → `POST /p2p/deposits/confirm`).
3. PM creates a sell-ad (`POST /p2p/ads`), signs the EIP-712 voucher (`POST /p2p/ads/:id/sign`).
4. A fresh ephemeral counterparty wallet (funded from deployer) opens a trade (`POST /p2p/ads/:id/trades` → broadcast `openTrade` on-chain → `POST /p2p/trades/:id/open/confirm`).
5. Self-POST a signed `payment_success` Nomba webhook to `POST /api/webhooks/nomba`.
   The webhook carries `accountRef=pmAccountRef` (VA-model PM key) and `senderName="E2E Counterparty"` (must match `users.name` in the counterparty row — the automation's conservative name-match).
6. Polls the trade until `status=released`.
7. Asserts PM `available_ngn` increased.

Saves: `buyAdId`, `buyTradeId`, deposit + open tx hashes.

**Dependency:** `onboard`

---

### 4. `sell` — counterparty sells crypto to PM
PM is the *buyer*; PM automation pays fiat to the counterparty's bank.

1. PM creates a buy-ad (`POST /p2p/ads`).
2. Fresh ephemeral counterparty wallet is funded (gas + 2 USDT minted from deployer).
3. Counterparty approves USDT → opens sell trade with JIT deposit (`openTradeWithDeposit`):
   `POST /p2p/ads/:id/trades` with `tokenAmount`, `payoutAccountNumber`, `payoutBank`, `payoutAccountName`.
4. Admin kicks sell payout: `POST /api/admin/pm/trades/:id/payout`.
   If Nomba is absent/sandbox (expected on testnet), the script **patches `nomba_payout_id` directly via psql** and continues.
5. Self-POST a signed `payout_success` webhook carrying `merchantTxRef=tradeId` (the deterministic match key used by `HandlePayoutStatus`).
6. Polls the trade until `status=released`.
7. Asserts PM `reserved_ngn` was consumed.

Saves: `sellAdId`, `sellTradeId`, `sellPayoutId`.

**Dependency:** `onboard`

---

### 5. `withdraw`
Reads the PM's current `availableNgn` and `reservedNgn`, computes free balance
(`available − reserved`), and calls `POST /api/admin/pm/:id/withdraw` with
`{ amount: "<free>" }`.

Asserts `available_ngn` decreased after the call.

If free balance is 0 or Nomba is not wired, the gateway call is skipped with a warning.

---

### 6. `verify`
Prints a final summary table (non-secret data only):
* PM row: status, provisioningStatus, inboundAccountNumber, accountRef, availableNgn, reservedNgn.
* buy trade status (expect `released`).
* sell trade status (expect `released`).
* On-chain `P2PEscrow.balanceOf(deployer, USDT)` (via direct RPC read — no broadcast).

---

### 7. `cleanup`
Deletes **only** the rows this run created, in FK-safe order:

| Table | Rows deleted |
|---|---|
| `webhook_events` | by tracked event IDs (`provider='nomba'`) |
| `p2p_messages` | by tracked trade IDs |
| `p2p_trades` | by tracked trade IDs |
| `p2p_ads` | by tracked ad IDs |
| `p2p_payment_methods` | by tracked method ID |
| `payment_merchants` | by `user_id=PM_USER_ID` |
| `merchant_requests` | by tracked request ID |
| `users` | all 3 fixed UUIDs |

Also removes `.pm-e2e-state.json`.

---

## Security constraints

* `JWT_SECRET`, `NOMBA_WEBHOOK_SECRET`, `DATABASE_URL`, `DEPLOYER_PRIVATE_KEY`
  are read from `.env` files but **never printed**.
* `psql` receives the DB password via `PGPASSWORD` environment variable only —
  never in the command string or its arguments.
* The script **does not push any transaction to mainnet** — it uses Sonic testnet
  RPC (`SONIC_TESTNET_RPC`) and only broadcasts on testnet.

---

## Nomba sandbox notes

The `onboard` phase submits settlement bank details:

```
settlementBankCode:      '999992'   // Nomba sandbox bank code
settlementAccountNumber: '9900000000' // Nomba sandbox test account
settlementAccountName:   'E2E PM Corp'
```

**The controller MUST replace these with valid Nomba sandbox test bank values**
before running `onboard` in a real Nomba-connected sandbox environment.
These placeholder values may cause the VA provisioning call to fail if Nomba
validates them at creation time (the `onboard` phase handles the 503 gracefully
and allows re-runs).

Similarly, the `buy` and `sell` phase inbound/payout webhooks use placeholder
`walletId: ''` — in a Nomba-configured environment the TSA parent walletId
should be filled in for payout webhooks if `merchantTxRef`-based matching is not
available (it is available here — the script sets `merchantTxRef = tradeId`).

---

## Contract uncertainties (verify at runtime)

1. **`accountRef` field location in inbound webhook** —
   `nomba_webhook.go` (lines 241-244) contains a `TODO(nomba-facts)`:
   the exact JSON path for the accountRef in a real Nomba `payment_success`
   payload is unconfirmed.  The code tries `data.transaction.accountRef` first,
   then falls back to `data.transaction.aliasAccountNumber`.  If a live Nomba
   inbound webhook puts the accountRef elsewhere (e.g., `data.merchant.accountRef`),
   the `buy` phase auto-release will route to the manual queue (no match) instead
   of releasing.  **Verify against a real sandbox webhook before declaring buy-flow
   end-to-end green.**

2. **Nomba VA provisioning** — `onboard` calls `POST /v1/accounts/virtual` via
   `NombaClient.CreateVirtualAccount`.  If Nomba credentials are absent
   (`NOMBA_CLIENT_ID`, `NOMBA_CLIENT_SECRET` etc. unset in `tsa-api-go/.env`),
   the API returns 503 "PM provisioning unavailable".  The `onboard` phase
   catches this and continues so that phases that don't need a real VA
   (`seed`, `cleanup`) can still run.

3. **Sell payout gateway** — `sell` phase step 4 calls `POST /admin/pm/trades/:id/payout`
   which internally calls `NombaClient.Transfer`.  If Nomba is not configured,
   the API returns 500.  The script **falls back to a direct psql patch** of
   `nomba_payout_id` and continues to the payout webhook.  The webhook will still
   exercise the full `HandlePayoutStatus` → `AutoRelease` path as long as the
   patched `nomba_payout_id` matches the `merchantTxRef` in the webhook payload
   (which it does — the script sets `merchantTxRef = sellTrade.id` and patches the
   column to `sandbox-payout-<timestamp>`; the `HandlePayoutStatus` lookup prefers
   `merchantTxRef` over `nomba_payout_id`).

4. **`escrow.balanceOf` ABI** — the `verify` phase reads the on-chain pool balance
   using the ABI fragment `balanceOf(address seller, address token) view returns (uint256)`.
   Verify this matches the deployed `P2PEscrowV2` interface; if the function
   signature differs (e.g., different argument order or name) the call will revert.

5. **Counter user name must match exactly** — the `buy` phase inbound webhook uses
   `senderName: "E2E Counterparty"`.  `matchBuyInbound` calls `MatchesPayerName`
   which likely does a fuzzy or case-insensitive substring match.  Confirm the
   exact matching logic in `pm_automation_service.go::MatchesPayerName` (not
   shown here) to ensure the name in the `seed` phase (`name: 'E2E Counterparty'`)
   will match the webhook `senderName`.

6. **PM handler is `nil`-gated** — `GET /admin/pm` and `POST /admin/pm/:id/withdraw`
   are only registered when `pmh != nil` (see `routes.go` line 198).  If the API
   boots without Nomba credentials, `pmh` may be nil and these routes return 404.
   The `onboard` phase poll and `withdraw` phase will fail in that case.  This is
   expected — the PM automation stack is dormant-by-default.
