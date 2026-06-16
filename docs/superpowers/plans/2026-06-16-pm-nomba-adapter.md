# PM Nomba Live Adapter (Plan 4 — completes Task 9)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** Replace the gated Nomba stubs with the real integration, against the documented Nomba API, so PM automation goes live the moment sandbox creds are configured.

**Branch:** `feat/pm-nomba` (off `feat/pm-automation` / PR #37).

**Source of truth:** Nomba API facts — see memory `reference_nomba-api-facts.md`. Key shapes:
- Auth (already built in `nomba_client.go`): `POST /v1/auth/token/issue`, header `accountId`, body `{grant_type, client_id, client_secret}`.
- Payout: `POST /v2/transfers/bank`, headers `Authorization: Bearer` + `accountId`, body `{amount, accountNumber, accountName, bankCode, merchantTxRef, senderName, narration}` → `data.{id, status ("SUCCESS"|"PENDING_BILLING")}`.
- Webhook sig: header `nomba-signature` = base64(HMAC-SHA256(secret, `{event_type}:{requestId}:{userId}:{walletId}:{transactionId}:{type}:{time}:{responseCode}:{timestamp}`)), timestamp from header `nomba-timestamp`. **NOT raw-body HMAC.**
- Inbound `payment_success`: `data.merchant.walletId` (PM key), `data.transaction.{transactionId,type,transactionAmount,time,responseCode}`, `data.customer.{senderName,accountNumber}`; event id = `requestId`; `payment_success` itself == settled.
- Payout `payout_success`/`payout_failed`: `data.transaction.{transactionId, merchantTxRef}`; match our trade by `merchantTxRef` (= trade ID); dedup on `requestId`.

**Two corrections to the already-built stub code (the point of this plan):** (1) the webhook signature is the colon-delimited-field scheme, not raw-body HMAC; (2) the inbound PM is keyed by `walletId`, not a destination account number.

---

## T9.1 — DTOs + real parsers
**Files:** `internal/services/nomba_gateway.go` (DTOs), `internal/services/nomba_webhook.go` (`ParseNombaInbound`/`ParseNombaPayout`), tests.
- [ ] `InboundPayment`: add `WalletID string` (PM key). `PayoutStatus`: add `MerchantTxRef string`. `PayoutRequest`: add `AccountName string` and `SenderName string`.
- [ ] Rewrite `ParseNombaInbound` for the real envelope: `event_type`/`requestId`/`data.merchant.walletId`/`data.transaction.{transactionAmount,type,...}`/`data.customer.senderName,accountNumber`. EventID=`requestId`; WalletID=`data.merchant.walletId`; SenderAccountName=`data.customer.senderName`; AmountNGN = `data.transaction.transactionAmount` formatted to a 2dp decimal string (it's a JSON number in naira); Status="settled" iff `event_type=="payment_success"` else "" (ignore non-success). Error on empty requestId/walletId.
- [ ] Rewrite `ParseNombaPayout`: EventID=`requestId`; PayoutID=`data.transaction.transactionId`; MerchantTxRef=`data.transaction.merchantTxRef`; Status: `payout_success`→"success", `payout_failed`→"failed", else "pending".
- [ ] Tests with realistic Nomba JSON for both. Remove the old `// TODO(nomba-facts)` assumed-shape comments. Commit: `feat(pm): real Nomba inbound/payout parsers`.

## T9.2 — Nomba webhook signature verifier
**Files:** `internal/services/nomba_webhook.go` (new `NombaWebhookVerifier`), `internal/handlers/nomba_webhook_handler.go` (verify flow), tests.
- [ ] Add `NombaWebhookVerifier{secret []byte}` + `NewNombaWebhookVerifier(secret)`. Method `Verify(body []byte, sigHeader, timestampHeader string) bool`: unmarshal the envelope to extract `event_type, requestId, data.merchant.{userId,walletId}, data.transaction.{transactionId,type,time,responseCode}`; build the colon-delimited string with `timestampHeader` as the trailing `{timestamp}`; HMAC-SHA256 with secret; base64-encode; constant-time compare (`hmac.Equal` on raw bytes after base64-decoding the header) against `sigHeader`. Reject empty sig/timestamp.
- [ ] Update `NombaWebhookHandler`: read headers `nomba-signature` + `nomba-timestamp`; call the Nomba verifier with `(body, sig, timestamp)`. (The handler already reads the raw body once and parses — keep single-read.) Keep the generic `HMACWebhookVerifier` in the file for any other provider but the handler now uses the Nomba verifier (injected). Update the handler's verifier field type accordingly (a small interface `bodyHeaderVerifier{ Verify(body []byte, sig, ts string) bool }`).
- [ ] Tests: a payload + a correctly-computed `nomba-signature` (compute in the test the same way) → accepted; tampered body/sig/timestamp → rejected; empty → rejected. Update the existing handler tests to pass the timestamp header + a valid Nomba signature. Commit: `feat(pm): Nomba webhook signature verifier (colon-field HMAC-SHA256+base64)`.

## T9.3 — PM-by-walletId + payout-by-merchantTxRef
**Files:** `internal/models/payment_merchant.go`, `internal/services/pm_service.go`, `internal/services/pm_automation_service.go`, `internal/config/database.go`, tests.
- [ ] Add `NombaWalletID string \`gorm:"size:64;index" json:"nombaWalletId"\`` to `PaymentMerchant` (the inbound `data.merchant.walletId` we match). AutoMigrate adds it; add a unique partial index in `database.go` (`uq_payment_merchants_nomba_wallet ... WHERE nomba_wallet_id <> ''`).
- [ ] `PMService.GetByWalletID(ctx, walletID) (*PaymentMerchant, error)` → `ErrPMNotFound` on miss. Update `SeedTSA` to accept + set `NombaWalletID`.
- [ ] `HandleInbound`: resolve PM by `p.WalletID` via `GetByWalletID` (replace `GetByInboundAccount(p.DestinationAccount)`).
- [ ] `HandlePayoutStatus`: if `p.MerchantTxRef` non-empty, parse it as a trade UUID and load the trade by ID (the deterministic match); else fall back to the existing `nomba_payout_id` lookup. Keep the empty-PayoutID guard.
- [ ] Update the existing buy/sell automation tests that seeded `DestinationAccount`/payout-id to use `WalletID`/`MerchantTxRef`. New tests: `GetByWalletID` hit/miss; `HandleInbound` keys by walletId; `HandlePayoutStatus` matches by merchantTxRef. Commit: `feat(pm): key inbound by Nomba walletId + match payout by merchantTxRef`.

## T9.4 — live NombaGateway HTTP adapter
**Files:** `internal/services/nomba_client.go` (add `Transfer`), `internal/services/nomba_gateway_http.go` (new), tests.
- [ ] Add `(*NombaClient).Transfer(ctx, body TransferRequest) (TransferResult, error)` calling `authedRequest(ctx, POST, "/v2/transfers/bank", json)`; map response `data.{id,status}`. `TransferRequest{Amount float64|string, AccountNumber, AccountName, BankCode, MerchantTxRef, SenderName, Narration}`.
- [ ] `NombaHTTPGateway{client *NombaClient}` implementing `NombaGateway.InitiatePayout(ctx, PayoutRequest)`: build the transfer body from `PayoutRequest` (AmountNGN→amount, AccountNumber, AccountName, BankCode, Reference→merchantTxRef, SenderName, a narration like "TSA P2P payout"); return `data.id` as the payout id. Treat `SUCCESS`/`PENDING_BILLING` as accepted (final status via webhook).
- [ ] Tests with `httptest` server returning the documented transfer response; assert the request body fields + that `data.id` is returned; a 4xx → error. Commit: `feat(pm): live Nomba transfer gateway (POST /v2/transfers/bank)`.

## T9.5 — config + wiring
**Files:** `internal/config/config.go`, `cmd/server/main.go`, tests/boot.
- [ ] Config: add `NombaSenderName` (`NOMBA_SENDER_NAME`, default e.g. "TSA Connect") and `PMTSANombaWalletID` (`PM_TSA_NOMBA_WALLET_ID`) for the seed. (Base URL/clientID/secret/accountID/webhook secret already exist.)
- [ ] main.go: when Nomba creds present (`NombaClientID != "" && NombaClientSecret != ""`), build `NewNombaClient(NombaBaseURL, NombaClientID, NombaClientSecret, NombaAccountID)` → `NombaHTTPGateway` and pass it to `PMAutomationService` instead of the disabled gateway. When `NombaWebhookSecret != ""`, build `NewNombaWebhookVerifier(NombaWebhookSecret)` and inject into the webhook handler (replacing the generic HMAC verifier). Seed TSA with `NombaWalletID`. Keep dormant-by-default when creds absent (the disabled gateway + no webhook handler).
- [ ] `go build ./...`; `go test ./...` (services/jobs/etc green; handlers pre-existing 31 unchanged). Reason through a dormant boot (no Nomba creds) + a configured boot. Commit: `feat(pm): wire live Nomba gateway + verifier (dormant-by-default)`.

## Final
Dispatch a review over the branch diff focused on the signature correctness + the walletId/merchantTxRef matching (money-path), fix findings, then finishing-a-development-branch (PR stacking on #37). Runtime creds still needed to actually go live: TSA parent accountId, client_id/secret, webhook secret, sandbox URL, the PM's Nomba walletId.
