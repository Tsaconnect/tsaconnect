# P2P Phase-1 Smoke Test (Sonic Testnet)

End-to-end validation of the P2P trading feature against Sonic testnet.
Treats every state transition as a distinct test case. Run before merging
`feat/p2p-trading` to mainline.

## 0. Prerequisites

- **Sonic testnet S** for two test EOAs (seller, buyer) and one admin EOA
  (also acts as the operator/admin signer in Phase 1)
- **Sonic testnet USDT/USDC/MCGP** seeded into the seller wallet
  - MCGP testnet: `0x517600323e5E2938207fA2e2e915B9D80e5B2b21`
  - USDC testnet: `0x29219dd400f2Bf60E5a23d13Be72B486D4038894`
  - USDT testnet address: set `TESTNET_USDT_ADDRESS` if needed
- **Persona sandbox** ID for two test users (seller, buyer)
- **Two devices** (or simulators) running `tsa-app`
- **PostgreSQL** instance with migrations applied
- **Cloudinary** API key (existing TSA Connect setup)

## 1. Deploy `P2PEscrow.sol`

```bash
cd smart-contracts

# .env in this directory:
#   DEPLOYER_PRIVATE_KEY=...
#   INITIAL_ADMINS=<admin-EOA-address>
#   SYSTEM_WALLET=<fee-collector-address>  # optional; defaults to owner

# Dry-run first
CHECK_ONLY=1 npx hardhat run scripts/deploy-p2p-escrow.js --network sonicTestnet

# Deploy for real
npx hardhat run scripts/deploy-p2p-escrow.js --network sonicTestnet

# Note the `P2P_ESCROW_ADDRESS=…` line from the output.
```

## 2. Configure backend

In `tsa-api-go/.env` (or your deployment config):

```env
P2P_ESCROW_ADDRESS=<proxy address from step 1>
P2P_CHAIN_ID=14601
P2P_OPERATOR_ADDRESS=<the INITIAL_ADMINS address>
P2P_FEE_BPS=50          # 0.5% maker fee
```

Restart `tsa-api-go`. Look in the logs for:
- `p2p escrow adapter initialised` (or equivalent)
- `chain reconciler started` (30s ticker)
- `timeout watchdog started` (1m ticker)

## 3. Manual test matrix

For each row, record pass/fail and tx hashes in a shared sheet.

### A. Happy path (release)

1. **Seller**: open `/p2p` → tap "Sell" → fill ad form (USDT, 100, rate 1500 NGN, NG bank PM, min ₦1,000 / max ₦150,000) → sign create-ad tx → wait for receipt
2. Verify ad appears in **/p2p/my-ads** as `active`
3. **Buyer**: open `/p2p` → tap the ad → "Open trade" → enter ₦15,000 (10 USDT) → submit
4. Verify trade appears in **/p2p/my-trades** as `pending_payment` for buyer; seller sees a system message
5. **Buyer**: tap "I have paid" → upload proof image → confirm
6. Verify trade flips to `paid`; chat shows the proof image + system message
7. **Seller**: tap "Release" → sign release tx
8. Verify trade flips to `released`; both wallets reflect USDT movement on chain
9. **/p2p/my-ads** shows ad remaining = 90, reserved = 0

### B. Timeout cancellation

1. Repeat A.1–A.3 to open a fresh trade
2. Do NOT mark paid as buyer
3. Wait 30+ minutes (or temporarily lower TIMEOUT for testing)
4. Verify watchdog cancels the trade automatically; system message appears
5. Verify ad `remaining` is restored to its pre-open value on chain (re-read via Sonic explorer)

### C. Dispute resolved to buyer

1. Repeat A.1–A.6 to reach `paid` state
2. **Seller**: do NOT release
3. **Buyer**: tap "Open dispute" → enter reason → sign raiseDispute tx
4. Trade flips to `disputed`
5. **Admin**: open `/admin/p2p-disputes` → tap "Resolve" → choose "Refund buyer" → add note → sign
6. Verify trade flips to `resolved`; buyer wallet shows USDT credited; ad reserved drops by trade amount

### D. Dispute resolved to seller

1. Repeat A.1–A.6
2. **Seller**: open dispute, "buyer claims to have paid but never did"
3. **Admin**: resolve "Release to seller"
4. Verify ad `remaining` is restored (funds re-pool into the ad)

### E. Ad close

1. Create a fresh ad (A.1)
2. **Seller**: open `/p2p/my-ads` → "Close ad"
3. Sign cancel tx
4. Verify ad status flips to `cancelled`; on-chain reads confirm reserved=0 for the ad

### F. Pause / resume

1. Create an ad (A.1)
2. **Seller**: open `/p2p/my-ads` → "Pause"
3. Verify ad disappears from marketplace browse (`/p2p`)
4. Resume → verify it reappears

### G. Stale-rate gating

1. Stop the rates-source process (or block the upstream domain in `/etc/hosts`)
2. Wait for `StaleSeconds > 0`
3. **Seller**: try to create an ad → expect a friendly error referencing stale rates

### H. Reconciler drift correction

1. Reach `paid` state (A.6)
2. Run a manual transaction through Sonic explorer or hardhat console that calls
   `release(tradeId)` directly bypassing the backend (impersonate seller key)
3. Within 30s the reconciler should flip DB → `released` and emit the events

### I. Post-broadcast confirm failure

1. Reach `paid` state (A.6)
2. Briefly take `tsa-api-go` offline (or block its DB)
3. **Seller**: tap "Release" → sign → broadcast succeeds → confirm step fails
4. Expect the modal: *"Release submitted, server confirm failed"* with the tx hash
5. Bring `tsa-api-go` back; within 30s the reconciler should mark it `released`

### J. Non-party access

1. Sign in as a third user (not buyer, not seller)
2. Deep-link to `/p2p/trade/<some-other-tradeId>`
3. Expect the "Not your trade" screen with a back button (no spinner hang)

## 4. Money-invariant check

After each test, run:

```sql
SELECT token,
       SUM(token_amount) AS active_reserved
FROM p2p_trades
WHERE status IN ('pending_payment','paid','releasing','disputed')
GROUP BY token;
```

Cross-reference with `P2PEscrow.balanceOf(token)` on Sonic. They must match (less
the live ad pool `remaining` sum). Any drift gets logged as `p2p_reconciler_drift_total`.

## 5. WebUI polish gates

Eyeball each screen at 320pt-wide (smallest supported) and 480pt-wide:

- `/p2p` marketplace — header chips don't wrap awkwardly
- `/p2p/my-ads` — action buttons stay readable
- `/p2p/trade/[id]` — chat bubbles wrap, status bar legible
- `/admin/p2p-disputes` — resolve modal not cut off

## 6. Sign-off

Smoke test passes when:
- A–F complete without manual intervention
- G blocks with a friendly error
- H, I, J recover within 60s or show the expected UI
- §4 invariant holds across all trades during the run
- No new errors in `tsa-api-go` logs beyond expected reconciler chatter
