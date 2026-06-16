# PM (Payment Merchant) — Go-Live Checklist

**Status:** All PM code is on `main` and **dormant by default** — nothing settles until the items below are configured. This checklist covers what's needed to flip PM live, in order. Nothing here is a code bug; it's configuration, an on-chain setup, one signer decision, and a validation step.

Design: `docs/superpowers/specs/2026-06-15-pm-payment-merchant-design.md`. Nomba facts: see the project memory / the parsers in `tsa-api-go/internal/services/nomba_webhook.go`.

---

## 1. Release signer (decide before real funds) — the one real decision

`adminRelease` is an on-chain tx that must be signed by the **platform admin EOA**. The `ReleaseSigner` seam (`tsa-api-go/internal/services/release_signer.go`) decides where that key lives. Selected via `PM_RELEASE_SIGNER` (`local` default; `kms` reserved for the gated impl).

**Blast radius is small** (informs how much hardening you need): the admin EOA can ONLY `adminRelease`/`adminResolve` — it cannot withdraw the pool, change fees, pause, or add admins. `adminRelease` settles a trade to *its own legitimate counterparty*, not to an attacker. The owner can `removeAdmin` instantly to revoke a compromised key, and `pause`.

| Phase | Signer | Conditions |
|---|---|---|
| Dev / sandbox | `LocalKeyReleaseSigner` (built) | `PM_RELEASE_PRIVATE_KEY` set; no extra hardening needed |
| Limited / early launch | Local key — **defensible interim** | ALL of: server env access locked down (no shared shell, no key in logs/crash dumps); low `PerTradeCapUSD` + `DailyCapUSD`; an incident runbook ready to `removeAdmin` + rotate; key is a dedicated EOA used for nothing else |
| Real volume | **Managed signer** (build the gated impl) | AWS KMS *or* GCP KMS *or* HashiCorp Vault *or* MPC (e.g. Fireblocks) — driven by TSA's hosting/custody vendor. Build against the existing `ReleaseSigner` seam: **zero call-site changes**, just a new impl + `PM_RELEASE_SIGNER=<impl>` |

- [ ] **Decide the prod signer vendor** (drives which impl to write — it does NOT have to be AWS KMS).
- [ ] If launching on the local-key interim: confirm the three conditions above are in place.
- [ ] Build the managed-signer impl (the gated "Task 10") when the vendor is chosen — needs the key handle (e.g. KMS key ARN + region) and IAM/access. Acceptance test: the signer's recovered address == `PM_ADMIN_EOA_ADDRESS` (main.go already warns on mismatch at boot).

## 2. On-chain prerequisites

- [ ] Register the platform admin EOA on the escrow via `addAdmin(adminEOA)` (owner-only). Without this, every `adminRelease` reverts → trades stall (auto-recovered by the PM recovery job retries, but never settle).
- [ ] Fund the PM's escrow pool from its **maker wallet** (the PM's own wallet; deposits only — no release authority).
- [ ] Confirm the deployed escrow is the V2/V3 with `adminRelease`/`addAdmin`/`removeAdmin`/`pause` (it is — no contract change needed).

## 3. Nomba configuration + sandbox validation

- [ ] Set Nomba creds: `NOMBA_BASE_URL` (sandbox first), `NOMBA_CLIENT_ID`, `NOMBA_CLIENT_SECRET`, `NOMBA_ACCOUNT_ID` (parent business UUID), `NOMBA_WEBHOOK_SECRET`, `NOMBA_SENDER_NAME`. The live transfer gateway only enables when client id + secret + account id are all set.
- [ ] Point the Nomba dashboard webhook at `POST /api/webhooks/nomba` and copy its signature key into `NOMBA_WEBHOOK_SECRET`.
- [ ] **Validate against a real sandbox payload** (the one thing the docs couldn't fully confirm):
  - [ ] A real inbound credit fires `event_type == "payment_success"` and the colon-field HMAC-SHA256+base64 signature (header `nomba-signature`, timestamp `nomba-timestamp`) verifies. If the real `event_type` value or signing-field set differs, adjust `NombaWebhookVerifier` / `ParseNombaInbound`.
  - [ ] A real payout webhook is `payout_success`/`payout_failed` and carries `data.transaction.merchantTxRef` (= our trade ID) + `requestId`.
  - [ ] A `POST /v2/transfers/bank` returns `data.id` + `status` (`SUCCESS`/`PENDING_BILLING`).

## 4. PM seed + caps

- [ ] Seed the TSA PM: `PM_TSA_ENABLED` + `PM_TSA_USER_ID`, `PM_TSA_NAME`, `PM_TSA_INBOUND_ACCOUNT`, `PM_TSA_INBOUND_BANK`, `PM_TSA_MAKER_WALLET`, **`PM_TSA_NOMBA_WALLET_ID`** (the inbound `data.merchant.walletId` used to match credits), `PM_TSA_PER_TRADE_CAP_USD`, `PM_TSA_DAILY_CAP_USD`.
- [ ] Set `PM_ADMIN_EOA_ADDRESS` (must match the signer's address).
- [ ] Set caps deliberately — both per-trade and daily are enforced (daily added in hardening).

## 5. Operational readiness

- [ ] **Monitor the manual-release queue** (`/admin/pm/manual-queue` + the app screen). Auto-settle only fires on an exact name+amount match of a settled credit; everything else (mismatch, ambiguous, over-cap) waits here for an operator. The PM recovery job (2-min tick) re-drives paid-orphans + re-dispatches failed webhooks, but the queue is the human net.
- [ ] Incident runbook: how to `pause` the escrow and `removeAdmin` the signer key on compromise.
- [ ] Confirm operators have the P2P-admin role (the queue endpoints are `RequireP2PAdmin`-gated).

## Reference — already handled (no action)

- Dormant-by-default boot (no creds → routes 503, server boots).
- Daily + per-trade cap enforcement (fail-safe; cap skipped on paid-orphan re-drive = already-admitted).
- Webhook dedup on `requestId`; benign events (`payment_failed`/reversal) recorded `processed`, not `failed`.
- Money invariant fail-closed on every release path; release authority is the single admin EOA; conservative single-hit matching.
- Recovery job for paid-orphans + failed/stale webhooks.
