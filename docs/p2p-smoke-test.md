# P2PEscrowV2 Two-Sided Smoke Test (Sonic Testnet)

End-to-end validation of the two-sided P2P trading feature against the **deployed
P2PEscrowV2** on Sonic testnet. Run before merging `feat/p2p-trading` to mainline.

V2 replaces the v1 sell-only pool/voucher escrow with one role-parameterized
contract that supports **two funding flows**:

- **Sell-ad — `PooledFromMaker` (funding=0):** a merchant pre-deposits crypto into
  an in-contract `available` pool and publishes a gasless **EIP-712 `AdVoucher`**.
  A buyer `openTrade`s against the voucher (moves `available → locked`), pays fiat
  off-platform, and the seller `release`s.
- **Buy-ad — `DepositedByTaker` (funding=1):** a merchant posts a buy ad (no pool,
  no voucher). The **user sells**: they JIT-deposit crypto via
  `openTradeWithDeposit(tradeId, token, amount, merchant)`, the merchant pays fiat,
  and the user `release`s to the merchant. **Role inversion:** in the DB/contract
  the depositing user is stored as `seller`, the merchant as `buyer`.

> **Status of this run (2026-05-30):** the read-only, off-chain, and live-API
> (no-funds) layers are **green** — see §4. The full on-chain broadcast round-trip
> (§9) and the manual app matrix (§10) require funded testnet wallets and are the
> remaining gate.

---

## 1. V2 deployment facts (Sonic testnet, chainId 14601)

| Item | Value | Verified |
|------|-------|----------|
| P2PEscrowV2 proxy | `0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7` | ✓ bytecode present |
| Implementation | `0xb9c2635FdD35826646A5c2dcc6CC48E7e19327ee` | — |
| Owner / system / deployer | `0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9` | ✓ on-chain owner matches |
| EIP-712 domain | name `TSAConnectP2PEscrow`, **version `2`** | ✓ ERC-5267 read |
| feeBps | `50` (0.5%) | ✓ |
| paused | `false` | ✓ |
| trustedForwarder / permit2 | `0x0` (gasless **disabled** by default) | ✓ |
| USDC (mock, 6 dp) | `0x685CD180e603A15507f575E7A347610b83d36d41` | ✓ accepted |
| USDT (mock, 6 dp) | `0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad` | ✓ accepted |
| MCGP (fee-discount token) | **MISCONFIGURED — see §8** | ⚠️ |

Backend cutover lives in `tsa-api-go/internal/config/config.go` (`sonicPresets["testnet"]`)
and `tsa-api-go/.env` (`SONIC_NETWORK=testnet`, `P2P_ESCROW_ADDRESS`,
`P2P_ESCROW_EIP712_VERSION=2`). **Mainnet stays on v1** (`eip712Version="1"`) — do not change.

---

## 2. The EIP-712 invariant (why version=2 matters)

The voucher domain `chainId` must equal what the RPC's `eth_chainId` actually reports
(`main.go` probes it at boot — **not** the configured `SONIC_CHAIN_ID`), and the
domain `version` must equal the deployed contract's (`"2"` for V2). A mismatch makes
every `ecrecover` resolve to a wrong address and rejects all signatures. The Go
typed-data builder (`p2p_trade_service.go`) uses domain name `TSAConnectP2PEscrow`,
version `s.eip712Version` (from env), chainId from the probe, and
`verifyingContract` = the proxy — which has been confirmed byte-for-byte equal to the
contract's `hashVoucher` digest (§4).

---

## 3. Prerequisites

- **Sonic testnet S (native gas)** for every EOA that broadcasts a tx. There is **no
  scriptable faucet** — use the web faucet at <https://testnet.soniclabs.com/account>.
  - Sell-ad: seller (deposit/approve, release) + buyer (openTrade, markPaid).
  - Buy-ad: taker/user (approve, openTradeWithDeposit, release) + merchant (markPaid).
- **Mock stablecoins** seeded into the crypto-providing EOAs (seller for sell-ads,
  taker for buy-ads). The mocks expose a public `mint(to, amount)`:
  - `FAUCET_TO=0x… npx hardhat run scripts/faucet-testnet-stables.js --network sonicTestnet` (USDC+USDT)
  - ⚠️ **do not** use `scripts/mint-to-user.js` — it targets stale token addresses.
- **`DEPLOYER_PRIVATE_KEY`** (a key holding testnet S) for the automated round-trip
  in §9 — it self-funds the ephemeral wallets and mints their tokens.
- **Persona sandbox** + **two app devices** + **PostgreSQL** + **Cloudinary** for the
  manual app matrix (§10).

---

## 4. Automated checks already run (no funds required)

These cover the highest-risk integration surface and are all **green**. Scripts live
in `smart-contracts/scripts/`.

| Check | Script / method | Result |
|-------|-----------------|--------|
| Contract live, owner, paused, fee, domain | `node scripts/smoke-probe-v2.js` | ✓ owner matches, paused=false, feeBps=50, domain name=`TSAConnectP2PEscrow` version=`2` chainId=`14601` verifyingContract=proxy |
| **Voucher digest parity vs live contract** | `node scripts/smoke-voucher-parity.js` | ✓ ethers EIP-712 digest (from the exact Go domain/types) **== on-chain `hashVoucher`**; signature recovers to signer; a v1-domain digest does **not** match (proves version=2 is load-bearing) |
| Go EIP-712 builder vs contract typehash | code review (`p2p_trade_service.go:2978` vs `P2PEscrowV2.sol:78,163`) | ✓ domain name + AdVoucher field order/types identical |
| `getTrade` 10-field tuple shape | code vs `P2PEscrowV2.sol:276` | ✓ `feeBps` uint16 @ idx7, `autoReleaseAt` @ idx8, `voucherHash` @ idx9 |
| Backend builds & boots on testnet config | `go build ./...` + boot log | ✓ `Sonic network = testnet (chain 14601…)`, `P2P escrow contract verified … (170 bytes)`, routes live (no "dormant"), reconciler(30s)+watchdog(1m) up |
| **Live API buy-ad creation** | `node scripts/smoke-drive-buyad.js` (isolated `tsa_smoke` DB) | ✓ auth + KYC gate, payment-method create, buy-ad created `active`/`typedData=null` (OER live-rate + PM/currency + token resolution all exercised) |
| Live API open-trade guard | same | ✓ correctly **refuses to prepare** an open that would revert (unfunded taker → `estimate gas (would revert)`); **no orphan trade / capacity drift** on the failed prepare |

> The sell-ad/voucher path can't be driven through the live API without funds because
> `CreateAd` (sell) reads on-chain `available >= amount` and refuses to mint an
> unbacked voucher. The voucher cryptography is instead proven directly against the
> live contract by `smoke-voucher-parity.js`.

---

## 5. Verify the deployed V2 (read-only)

```bash
cd smart-contracts
node scripts/smoke-probe-v2.js          # no key needed
node scripts/smoke-voucher-parity.js    # no key needed
```

Expect the domain assertions to print `MATCH ✓` / `version=2 (V2 OK)`. (The probe's
`balanceOf(token)` and `defaultFeeBps()` lines print `ERR` — expected, those exact
signatures don't exist on V2; use `balanceOf(seller,token)` and `feeBps()`.)

A genuine redeploy (rarely needed) uses `scripts/deploy-p2p-escrow-v2.js`
(`OWNER_ADDRESS` / `SYSTEM_WALLET_ADDRESS` / `ADMIN_ADDRESSES` / `ACCEPTED_TOKENS` /
`MCGP_TOKEN_ADDRESS`, `CHECK_ONLY=1` for dry-run).

---

## 6. Configure & boot the backend

```bash
cd tsa-api-go
# .env already cut over: SONIC_NETWORK=testnet selects the testnet preset bundle
# (RPC, chainId 14601, token addresses, p2pEscrow proxy, eip712Version=2).
go run ./cmd/server
```

Boot-log checklist:
- `[config] Sonic network = testnet (chain 14601, P2PEscrow 0x1B1ade…)`
- `testnet/sonic blockchain client connected (chain ID: 14601, RPC: …testnet.soniclabs.com)`
- `P2P escrow contract verified at 0x1B1ade… on chain 14601`
- `Escrow contract owner (on-chain): 0x38cE9a4C…`
- reconciler (30s) + timeout watchdog (1m) started; **no** `P2P trade routes dormant`.

`P2P_ESCROW_ADDRESS` presence is what makes `/api/p2p/*` live (`IsConfigured()`); every
endpoint also requires a JWT + `verificationStatus=="verified"` user.

---

## 7. `getTrade` & event reference

`getTrade(tradeId)` → `(seller, buyer, token, amount, createdAt, status uint8,
funding uint8, feeBps uint16, autoReleaseAt, voucherHash)`.

`Status`: `0 None · 1 Pending · 2 Paid · 3 Released · 4 Cancelled · 5 Disputed · 6 Resolved`.
`Funding`: `0 PooledFromMaker · 1 DepositedByTaker`.

Events the reconciler/confirm paths parse: `Deposited`, `TradeOpened`,
**`TradeOpenedWithDeposit`** (new), `TradePaid`, `TradeReleased`, `TradeAutoReleased`,
**`TradeCancelled(tradeId, amount, returnedToPool)`** (changed), `TradeDisputed`,
`TradeResolved(tradeId, releasedToBuyer, amountMoved, feeToSystem)`,
`VoucherCancelled`.

---

## 8. ⚠️ Known config issue — MCGP testnet address

`MCGP_TOKEN_ADDRESS` is wrong at **three layers**, all pointing at
`0x517600323e5E2938207fA2e2e915B9D80e5B2b21`, which is the **mainnet** MCGP — it has
**no bytecode on testnet**:

1. `config.go` `sonicPresets["testnet"].mcgpAddress` (line ~53)
2. `tsa-api-go/.env` `MCGP_TOKEN_ADDRESS` / `TESTNET_MCGP_ADDRESS`
3. On-chain: the deployed escrow's `mcgpToken()` and `acceptedTokens()` both point at
   `0x5176…`, not the real testnet MCGP.

The real testnet MCGP mock is `0xb5B073BF578fE24311ED50e540D9d632f1679261`
("Mock MCGP", 18 dp), and it is **not** whitelisted on the escrow.

**Impact:** none for USDC/USDT trades — the contract's only use of `mcgpToken` is
`_feeBpsForToken` (`if (token == mcgpToken) return 0;`); it never calls into the MCGP
contract, so `release`/fee math is unaffected. MCGP is simply **untradeable** on
testnet (a `token=MCGP` trade would revert at `transferFrom` on a no-code address).

**Fix (when MCGP-on-testnet is needed):** point all three layers at
`0xb5B073BF…1679261`, then (owner-only, needs the deployer key)
`setTokenAcceptance(0xb5B073BF…, true)` and `setMcgpToken(0xb5B073BF…)`.

---

## 9. On-chain round-trip (the funded leg)

Exercises **both** happy paths on the real contract. Self-funds 4 ephemeral wallets
from one funded `DEPLOYER_PRIVATE_KEY` (native S + minted USDT):

```bash
cd smart-contracts
CHECK_ONLY=1 DEPLOYER_PRIVATE_KEY=0x… npx hardhat run scripts/smoke-roundtrip-v2.js --network sonicTestnet   # plan only, no broadcast
DEPLOYER_PRIVATE_KEY=0x…             npx hardhat run scripts/smoke-roundtrip-v2.js --network sonicTestnet   # execute
```

It asserts, on-chain:
- **Sell-ad:** `deposit` (available=amount) → sign voucher → `openTrade` (available→locked,
  funding=0, seller/buyer correct) → `markPaid` → `release` (buyer gets amount−fee,
  systemWallet gets fee).
- **Buy-ad:** `approve` → `openTradeWithDeposit` (locked=amount, funding=1, seller=taker
  buyer=merchant) → `markPaid` (by merchant) → `release` (by taker; merchant gets amount−fee).

> Timeout-cancel (`cancelByTimeout`) needs the 30-min `paymentWindow` to elapse (min 5
> min via owner `setPaymentWindow`) and is left to the manual matrix. Dispute +
> `adminResolve` require the deployer to be an on-chain admin.

---

## 10. Manual app test matrix (two devices)

Record pass/fail + tx hashes. **A1/A2** are the two happy paths; run both.

### A1. Sell-ad happy path (PooledFromMaker)
1. **Merchant**: `/p2p` → Sell → ad form (USDT, rate, NG bank PM, fiat limits) → deposit into pool → sign the EIP-712 voucher → ad goes `active`.
2. **Buyer**: open the ad → enter fiat amount → openTrade → trade `pending_payment`.
3. **Buyer**: "I have paid" + proof image → `paid`.
4. **Merchant**: Release → sign → `released`; verify on-chain `balanceOf(seller,token)` `available`/`locked` moved and buyer received amount−fee.

### A2. Buy-ad happy path (DepositedByTaker)
1. **Merchant**: `/p2p` → Buy ad (USDT, rate, merchant's own PM) → `active` immediately (no voucher).
2. **User**: open ad → enter crypto amount → approve (exact amount) → `openTradeWithDeposit` → trade `pending_payment` (user=seller, merchant=buyer).
3. **Merchant**: send fiat → "I have paid" + proof → `paid`.
4. **User**: Release → `released`; verify merchant received amount−fee.

### B1/B2. Timeout cancel
- **B1 sell-ad:** open, don't pay, wait `paymentWindow` → `cancelByTimeout`; `TradeCancelled(returnedToPool=true)`, seller `available` restored.
- **B2 buy-ad:** open, merchant doesn't pay, wait → `TradeCancelled(returnedToPool=false)`, crypto returned to the **user's wallet**, `accountedBalance` decremented.

### C. Dispute → buyer (both fundings)
Reach `paid`/`pending` → party raises dispute → admin `adminResolve(releaseToBuyer=true)` → `TradeResolved(true, amount−fee, fee)`, buyer credited.

### D. Dispute → seller/refund (both fundings)
Admin `adminResolve(false)` → `TradeResolved(false, 0, 0)`; refund is funding-branched (sell-ad re-pools to `available`; buy-ad transfers to the user's wallet). `disputeFeeBps` is **reserved/no-op** today.

### E. Ad close
Sell-ad: cancel voucher (`VoucherCancelled`) or off-chain deactivate. Buy-ad: pure off-chain DB deactivate (no on-chain voucher). No per-ad on-chain `reserved`.

### F. Pause / resume — off-chain ad visibility only.

### G. Stale-rate gating
Block the rates upstream → `CreateAd` fails with a friendly stale-rate error.

### H. Reconciler drift correction
Reach `paid`, call `release(tradeId)` directly on-chain (bypass backend) → within 30s the reconciler flips DB → `released` (reads the funding enum to pick the refund branch).

### I. Post-broadcast confirm failure
Reach `paid`, take the API offline, sign+broadcast release → confirm fails with the tx hash surfaced; bring API back → reconciler reconciles within ~30s.

### J. Non-party access
Third user deep-links to someone else's trade → "Not your trade" screen, no spinner hang.

---

## 11. Dormant features — confirm disabled, do NOT exercise

- **Auto-release** (`finalizeRelease` / opt-in): `autoReleaseWindow` defaults `0` (off).
- **Permit2** deposit/open variants: `permit2()` is `0x0` (off).
- **EIP-2771** gasless: `trustedForwarder()` is `0x0` (off).

These exist on V2 but are off by default; confirm they read zero, don't test them.

---

## 12. Money-invariant check

```sql
SELECT token, SUM(token_amount) AS active_locked
FROM p2p_trades
WHERE status IN ('pending_payment','paid','releasing','disputed')
GROUP BY token;
```

Cross-reference per token with on-chain `accountedBalance(token)` (V2 has **no**
single-arg `balanceOf(token)`; per-party balances are `balanceOf(seller,token) →
(available, locked)`). For active trades the `locked` side is keyed on `t.seller`
(which is the **taker** for buy-ads). Invariant: `accountedBalance(token)` ==
Σ`available[*][token]` + Σ`locked[*][token]`. Drift logs as `p2p_reconciler_drift_total`.

---

## 13. WebUI polish gates

Eyeball at 320pt and 480pt wide:
- `/p2p` marketplace (Buy/Sell tabs, currency + payment chips)
- `/p2p/ad/[adId]` — both sell-open and buy-open (JIT deposit/approve) layouts
- `/p2p/my-ads`, `/p2p/trade/[id]` (chat bubbles, status/countdown bar)
- `/admin/p2p-disputes` resolve modal

---

## 14. Sign-off

Passes when:
- §4 automated layer green (already met on this branch).
- §9 both on-chain round-trips pass.
- A1 **and** A2 happy paths complete; B1/B2 both refund branches; C/D both dispute
  directions across both fundings; E/F as expected.
- G blocks with a friendly error; H/I/J recover within ~60s.
- §12 invariant holds throughout.
- MCGP (§8) either fixed or explicitly accepted as out-of-scope for the run.
- No unexpected errors in `tsa-api-go` logs beyond reconciler chatter.
