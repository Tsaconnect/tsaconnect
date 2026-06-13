# P2P Escrow v2 — Two-Sided Escrow Contract (Design)

**Date:** 2026-05-29
**Status:** Contract IMPLEMENTED (2026-05-29) — `contracts/P2PEscrowV2.sol` built, 37-test Hardhat suite green, UUPS deploy script verified (CHECK_ONLY). **Awaiting re-audit before mainnet.** Follow-on sub-projects pending: relayer/FeeM, keeper (auto-release), dispute-v2 off-chain, backend integration, app, cutover/migration. Original design review status: Reviewed & approved (2026-05-29). Incorporates an independent design review — fixed the refund/cancel paths to branch on `funding`, guarded the `voucherFilled[0]` decrement, pinned the `uint96` cap + Permit2 pull-source + EIP-2771 owner/admin carve-outs, made the `finalizeRelease` guard concrete, documented the fee-incidence flip, resolved the `autoReleaseOptIn` storage — and per the owner's call: fee value unchanged for now but made **tunable without redeploy** (`FEE_BPS`/`TIMEOUT` → owner-settable, capped, fee snapshotted at open; UUPS upgradeability + storage gap kept).
**Phase:** P2P Phase 1, sub-project 1 of 5 (the contract — the dependency root)
**Supersedes/extends:** `smart-contracts/contracts/P2PEscrow.sol` (current pool/voucher model)
**Backed by:** `docs/p2p/design/2026-05-28-p2p-onchain-architecture-recommendation.md`, `docs/p2p/design/2026-05-28-p2p-telegram-wallet-architecture-research.md`, `docs/p2p/design/2026-05-28-p2p-master-roadmap.md`

---

## 1. Purpose & scope

Redesign `P2PEscrow` into a **two-sided, role-parameterized** escrow so that a user can **sell** crypto to a merchant (not just buy from one). The contract is made **design-complete** for all fund-touching v2 features so it passes through a **single re-audit**, even though the off-chain pieces that exercise some of those features ship as later sub-projects.

This spec covers **only the smart contract** (Solidity design, public interface, invariants, deploy/test strategy). Backend integration, the app, the relayer/keeper services, dispute-v2 off-chain tooling, and the cutover checklist are **out of scope** (each is its own follow-on sub-spec — see §13).

### Locked decisions (from brainstorming, 2026-05-29)
1. **Sequencing:** two-sided first; the contract is built **relayer-ready** (EIP-2771 + Permit2 hooks in) but the relayer service + Sonic FeeM ship later.
2. **Marketplace shape:** **merchants are the only ad-posters (makers)** — they post both SELL ads and BUY ads. Users are **takers** on both sides.
3. **Contract scope:** **design-complete, one audit** — two-sided roles + Permit2 + EIP-2771 + optimistic auto-release + loser-pays dispute fee + non-custodial arbitrator are all in the audited contract.
4. **Deployment:** current mainnet/testnet escrow is **pre-launch (no real funds)** → **fresh deploy**, no UUPS storage-compat constraint, no funds migration.
5. **Structure:** **unified `Trade` + a `funding` flag** (keep the proven pool/voucher path for sell-ads; add one JIT-deposit path for buy-ads; share all settlement internals).
6. **Release policy:** **manual seller release is the default**; `cancelByTimeout` stays; optimistic auto-release is **built-in but default-disabled** (later a per-merchant "⚡ auto-release" opt-in); the **arbitrator-backed dispute is the universal buyer backstop**.

---

## 2. The role model (the key simplification)

The blueprint's four roles (`cryptoDepositor / cryptoRecipient / fiatPayer / fiatReceiver`) **collapse to two**, because the party who provides crypto is always the party who receives fiat, and the party who receives crypto is always the one who pays fiat:

| Field | Economic role | Provides / receives | On-chain action |
|---|---|---|---|
| **`seller`** | crypto-provider **=** fiat-receiver | gives crypto, gets fiat | **`release`s** after confirming fiat receipt |
| **`buyer`**  | crypto-recipient **=** fiat-payer | gets crypto, pays fiat | **`markPaid`s** |

These are **exactly today's semantics** — the current contract's `seller`/`buyer` fields already encode them. Two-sidedness is captured entirely by **(a) who plays `seller`** and **(b) how the crypto is escrowed**:

- **Sell-ad** (merchant sells crypto): `seller` = merchant (crypto from the merchant's **pool**), `buyer` = user. *Unchanged from today.*
- **Buy-ad** (merchant buys crypto): `seller` = user (crypto **JIT-deposited** by the user at open), `buyer` = merchant.

Because role assignment lines up the authorization checks, the **`markPaid` / `release` / `raiseDispute`** functions and the **open** path share their authorization logic across both flows — only the identities differ. Buy-ad opens need **no on-chain signature** (the user funds their own escrow and names the merchant as recipient; the merchant's terms stay off-chain in the backend).

> ⚠️ **Sharing is NOT total — the refund/cancel paths must branch on `funding`.** `cancelByTimeout` and `adminResolve(false)` in the current contract are hardcoded to the pooled model: they credit `available[seller]` and unconditionally decrement `voucherFilled[voucherHash]`. For a JIT buy-ad (`seller` = user with no pool, `voucherHash = 0`) that would (a) strand the user's funds as a phantom in-contract pool balance, (b) leave `accountedBalance` un-decremented so the funds become **sweepable as "excess" by the owner** (an `accountedBalance`-fence breach, not just a UX bug), and (c) underflow/corrupt `voucherFilled[bytes32(0)]`. Every refund/cancel/resolve path therefore MUST branch on `funding` (§5.3) and MUST guard the voucher-capacity decrement with `if (voucherHash != 0)`.

---

## 3. Data structures

### Trade
```solidity
enum Status   { None, Pending, Paid, Released, Cancelled, Disputed, Resolved }
enum Funding  { PooledFromMaker, DepositedByTaker } // 0 = sell-ad, 1 = buy-ad

struct Trade {
    address seller;        // crypto-provider / fiat-receiver / releaser
    address buyer;         // crypto-recipient / fiat-payer
    address token;
    uint96  amount;
    uint40  createdAt;
    uint40  autoReleaseAt; // 0 until buyer marks paid AND auto-release is enabled
    Status  status;
    Funding funding;
    uint16  feeBps;        // snapshotted from `feeBps` storage at open; in-flight trades immune to later fee changes
    bytes32 voucherHash;   // sell-ads only; bytes32(0) for buy-ads
}
```
`feeBps`, `createdAt`, `autoReleaseAt`, `status`, `funding` all pack into one slot, so the snapshot costs no extra storage slot.
```solidity
```

`AdVoucher` (sell-ads only) is unchanged from the current contract.

### Storage (additions over current contract in **bold**)
- `mapping(address => mapping(address => uint256)) available` — per-(seller,token) pool (sell-ad makers only).
- `mapping(address => mapping(address => uint256)) locked` — per-(seller,token) reserved against open trades.
- `mapping(bytes32 => uint256) voucherFilled` — per-voucher cumulative fill (sell-ads).
- `mapping(bytes32 => bool) voucherCancelled` — seller voucher revocation (sell-ads).
- `mapping(bytes32 => Trade) trades`.
- `mapping(address => uint256) accountedBalance` — running `available + locked` per token; backs `sweepExcess`.
- **`uint16 feeBps`** — was `constant FEE_BPS = 50` (0.5%); now owner-settable storage (default `50`), **hard-capped at `MAX_FEE_BPS` (e.g. 500 = 5%)** so the setter can't be abused. **Snapshotted onto each `Trade` at open** (in-flight trades use the rate agreed at open).
- **`uint40 paymentWindow`** — was `constant TIMEOUT = 30 minutes`; now owner-settable storage (default `30 min`), bounded `[MIN_WINDOW, MAX_WINDOW]`. `cancelByTimeout` checks `createdAt + paymentWindow`. (Applies globally; the minor in-flight effect is only on when a *non-paying* trade can be reclaimed by the seller.)
- **`uint40 autoReleaseWindow`** — global; `0` = auto-release disabled (default). Combined with the per-seller `autoReleaseOptIn` below (both required to fire). A per-token override map is a possible later refinement, out of scope here.
- **`uint256 disputeFee` / bond config** — `0` = disabled (default).
- **`address trustedForwarder`** — EIP-2771; unset/`address(0)` until the relayer ships.
- **`address permit2`** — Uniswap Permit2 singleton (set if available on Sonic).
- **`mapping(address => bool) autoReleaseOptIn`** — per-merchant ⚡ opt-in, with `setAutoReleaseOptIn(bool)` (caller-set) **defaulting to false**. Included in the audited storage layout now (it cannot be "deferred off-chain" — `markPaid`/`finalizeRelease` read it). Default-false preserves today's behavior; the off-chain ⚡ UX simply flips it later. (Auto-release fires when `autoReleaseWindow > 0` **and** the seller has opted in.)

---

## 4. State machine

```
None ──openTrade / openTradeWithDeposit──▶ Pending
Pending ──markPaid──▶ Paid
Pending ──cancelByTimeout (payment window elapsed)──▶ Cancelled
Pending|Paid ──raiseDispute──▶ Disputed
Pending|Paid ──release / adminRelease──▶ Released
Paid ──finalizeRelease (only if auto-release enabled & window elapsed & undisputed)──▶ Released
Disputed ──adminResolve──▶ Resolved
```
Terminal: `Released`, `Cancelled`, `Resolved`. (Same as today plus the `finalizeRelease` edge.)

**Release paths:**
1. **Manual (default):** `seller` calls `release`. Always available; the fast path.
2. **Timeout cancel:** after the payment window, anyone may `cancelByTimeout` a still-`Pending` trade → refund (protects the seller against a non-paying buyer).
3. **Optimistic auto-release (built-in, default OFF):** if `autoReleaseWindow > 0` **and** `autoReleaseOptIn[seller]`, `markPaid` sets `autoReleaseAt = uint40(now + window)`; after it elapses and while still `Paid` and undisputed, **anyone** (keeper, relayer, or the buyer) may call `finalizeRelease` → settle to buyer (destination is fixed, so "anyone-callable" is safe). Raising a dispute first flips status to `Disputed`, so a later `finalizeRelease` reverts on the status check — the race is resolved atomically.
   - **Concrete guards (must be implemented exactly):** `finalizeRelease` is `nonReentrant whenNotPaused` and requires `t.status == Status.Paid && t.autoReleaseAt != 0 && block.timestamp >= t.autoReleaseAt`. The `autoReleaseAt != 0` check is mandatory — a bare `block.timestamp >= autoReleaseAt` is always true when the field is 0 and would auto-release every paid trade. `markPaid` is the **only** writer of `autoReleaseAt`. With the window at `0` (default), `autoReleaseAt` is never set and `finalizeRelease` always reverts — i.e., today's behavior.

---

## 5. Flows end-to-end

### 5.1 Sell-ad (pooled — unchanged)
1. `deposit(token, amount)` (or Permit2 variant) — merchant tops up `available`.
2. Merchant signs an `AdVoucher` off-chain (gasless); backend stores it.
3. `openTrade(tradeId, voucher, sig, amount)` — verifies the seller's signature, voucher liveness, per-trade bounds, and capacity; moves `amount` from `available` → `locked`; creates `Trade{ seller: voucher.seller, buyer: msg.sender, funding: PooledFromMaker, voucherHash }`.
4. Buyer `markPaid(tradeId)`.
5. Seller `release(tradeId)` → settle to buyer minus fee.
6. Failure: `cancelByTimeout` returns `locked → available` and refunds voucher capacity; `raiseDispute` → `adminResolve`.

### 5.2 Buy-ad (JIT deposit — new)
1. Merchant posts a buy-ad **off-chain only** (terms: token, rate, limits, payment methods). No on-chain action.
2. `openTradeWithDeposit(tradeId, token, amount, merchant)` — `nonReentrant whenNotPaused`. Guards mirror `openTrade`: `tradeId` unused, `amount > 0`, **`amount <= type(uint96).max`** (mandatory — `Trade.amount` is `uint96`; without this the cast truncates and pulls/locks a different sum than recorded → `accountedBalance` corruption), `acceptedTokens[token]`, **`merchant != _msgSender()`** (self-trade). **Pulls the crypto from `_msgSender()`** (Permit2 sig or `transferFrom` fallback — the pull source is always the meta-tx signer, *never* a named param) into `locked[_msgSender()][token]`; `accountedBalance[token] += amount`; creates `Trade{ seller: _msgSender(), buyer: merchant, funding: DepositedByTaker, voucherHash: 0 }`.
3. Merchant `markPaid(tradeId)` (the merchant is the fiat-payer here).
4. User `release(tradeId)` → settle crypto to merchant minus fee, after confirming fiat receipt.
5. Failure: `cancelByTimeout` / `adminResolve(false)` refund **out to the user's wallet** (`funding`-branched — see §5.3); `raiseDispute` → `adminResolve`.

### 5.3 The `funding`-dependent branch: refund destination
**Applies to every non-release exit: `cancelByTimeout` AND `adminResolve(false)`.** Both currently hardcode the pooled path; both must become:
- `PooledFromMaker` → `locked[seller] -= amt; available[seller] += amt` (stays in-contract for reuse; `accountedBalance` **unchanged**); and `if (voucherHash != 0) voucherFilled[voucherHash] -= amt` (refund capacity).
- `DepositedByTaker` → `locked[seller] -= amt; accountedBalance[token] -= amt; IERC20.safeTransfer(seller, amt)` (the user has no pool, so funds leave the contract); **no** `voucherFilled` touch (`voucherHash == 0`).

The `if (voucherHash != 0)` guard on the `voucherFilled` decrement is mandatory on **both** paths — an unguarded decrement of `voucherFilled[bytes32(0)]` underflows (reverts) for buy-ads.

`release`/settle is **identical** for both flows: `locked -=`, `accountedBalance -=`, transfer `amount − fee` to `buyer`, `fee` to `systemWallet`. (`adminResolve(true)` mirrors `release`.)

---

## 6. Dispute v2

- `raiseDispute(tradeId)` — either party, from `Pending`/`Paid`; freezes the trade. Unchanged.
- `adminResolve(tradeId, releaseToBuyer)` — **non-custodial arbitrator**: directs escrow to **exactly one of the two trade parties, never to itself**.
  - `true` → settle to buyer minus fee (like `release`).
  - `false` → refund to seller, **routed per `funding`** (pool-credit vs transfer-out).
- **Hard prerequisite:** the arbitrator must be a signing key. tsa-admin currently has **no wallet signing** — flagged; the contract supports the arbitrator address regardless, but end-to-end dispute resolution can't complete until that prereq lands (own sub-project).
- **Loser-pays dispute fee/bond:** designed in but **configurable, default `0` (disabled)** — same "build-it-in, enable-later" philosophy as auto-release. Amounts/UX deferred to the dispute-v2 sub-project (requiring a bond payment at dispute time is friction better introduced once gasless lands).

---

## 7. Gasless hooks (baked into the audited contract; clients ship later)

- **EIP-2771** — inherit `ERC2771ContextUpgradeable`; resolve **user-facing** authorization checks via `_msgSender()`. **Two carve-outs are mandatory:** (1) **owner/upgrade/admin paths** (`onlyOwner`, `_authorizeUpgrade`, `sweepExcess`, the admin setters) must keep real-`msg.sender`/`owner()` semantics — a trusted forwarder must never be able to impersonate the owner or an admin; (2) the **token-pull source** in `deposit`/`openTradeWithDeposit`/Permit2 paths is `_msgSender()`, never a named address param — pulling "from" an address-by-name would let a caller/relayer drain any third party who merely approved Permit2. Configurable `trustedForwarder` (unset/disabled until the relayer sub-project). Baked in now because it changes `_msgSender()` semantics contract-wide — doing it later forces a second audit.
- **Permit2** — pull the depositor's tokens via a Uniswap Permit2 signature (works for USDT, which lacks `permit`) in `deposit` and `openTradeWithDeposit`, **with a plain `transferFrom` direct-call fallback** so the relayer is never a custody/censorship single point of failure.
- **Sonic FeeM** registration is a deploy-time operational step, not contract logic.
- **Hygiene:** the current contract mixes the **non-upgradeable** `ReentrancyGuard` with upgradeable bases; v2's fresh deploy standardizes on the **upgradeable** variants throughout (`ReentrancyGuardUpgradeable`, etc.) and adds storage gaps as appropriate.

---

## 8. Fee model (preserved; redesign deferred)

Keep the **0.5% default** on release; **MCGP fee-free**; fee taken **out of the escrowed crypto** so the `buyer` (crypto-recipient) receives `amount − fee`. Keep `systemWallet` + the "`_systemRecipient` reverts if unset" guard. Per your call, the fee value is unchanged for now — but the rate is now an **owner-settable storage variable** (`feeBps`, default 50, hard-capped at `MAX_FEE_BPS`) rather than a `constant`, so it can be tuned later **without a redeploy**. `_splitFee` reads the trade's **snapshotted** `t.feeBps` (set at open), not the live storage value, so changing `feeBps` never affects in-flight trades and removes any owner-front-run-the-release vector. MCGP still snapshots `0`.

> **Fee incidence flips between flows — documented and intended as a placeholder.** Because the fee is deducted from the crypto paid to `buyer` (= crypto-recipient = fiat-payer): on a **sell-ad** the fee is borne by the **user** (the buyer), exactly as today; on a **buy-ad** the fee is borne by the **merchant** (the buyer/crypto-recipient), who pays full fiat but receives `amount − fee` crypto. This is a known consequence of keeping the existing mechanism unchanged. It is acceptable **only as a placeholder**: the maker-only / taker-zero / success-only redesign stays **deferred** (roadmap "Fee model — DEFERRED") and will revisit *who* bears the fee. No code complexity is added for it now.

---

## 9. Preserved invariants (the new JIT paths must honor these)

1. **`accountedBalance` + `sweepExcess` fence:** JIT deposit `+=`, JIT refund-out `−=`; pooled paths unchanged. `balanceOf(this) ≥ accountedBalance[token]` always; only the excess is sweepable.
2. **`nonReentrant`** on all fund-movers, including `openTradeWithDeposit` (external token pull) and Permit2 paths.
3. **Pause-guard map (explicit):** `whenNotPaused` on `deposit`, `openTrade`, **`openTradeWithDeposit`** (pulls funds, like `deposit`), `markPaid`, `release`, `adminRelease`, **`finalizeRelease`** (a settlement — must not fire during an incident pause). **NOT** pause-guarded: `withdraw`, `cancelByTimeout`, `adminResolve` (sellers/admins must recover/resolve during a pause).
4. **Only standard ERC-20s** whitelisted (no fee-on-transfer/rebasing — would break `accountedBalance`).
   - **Owner-power bound:** `setFeeBps`/`setPaymentWindow` are capped (`MAX_FEE_BPS`, `[MIN_WINDOW, MAX_WINDOW]`); the fee is snapshotted at open; `sweepExcess` still cannot reach principal. So even a compromised owner cannot retroactively over-charge an open trade or drain deposits.
5. **Self-trade guards:** sell-ad `voucher.seller != _msgSender()`; buy-ad `merchant != _msgSender()` (the role fields invert, so the literal check differs — both mean "buyer ≠ seller").
6. **Backend-side (documented, unchanged):** EIP-712 voucher domain `chainId` must match the chain's actual `eth_chainId`; money invariant (Σ active-trade `locked` == on-chain `locked`) extends to JIT trades — which requires the backend to mirror the contract's `Funding` discriminator onto a `p2p_trades` column (see §13.4) so the invariant and the reconciler's refund-routing expectation can be checked per flow.

---

## 10. Public interface (delta from current contract)

**Unchanged:** `deposit`, `withdraw`, `hashVoucher`, `cancelVoucher`, `openTrade`, `markPaid`, `release`, `adminRelease`, `cancelByTimeout`, `raiseDispute`, `adminResolve`, `getTrade`, `balanceOf`, admin/config setters, `pause`/`unpause`, `sweepExcess`.

**New:**
- `openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant)` — buy-ad JIT open (+ Permit2 overload).
- `depositWithPermit2(...)` / `openTradeWithDepositPermit2(...)` — Permit2 variants of the two funding entry points.
- `finalizeRelease(bytes32 tradeId)` — optimistic auto-release finalizer (reverts unless enabled & elapsed & `Paid` & undisputed).
- `setAutoReleaseWindow(...)`, `setDisputeFee(...)`, `setTrustedForwarder(address)`, `setPermit2(address)` — owner config for the built-in-but-disabled features.
- `setFeeBps(uint16)` (≤ `MAX_FEE_BPS`), `setPaymentWindow(uint40)` (within `[MIN_WINDOW, MAX_WINDOW]`), `setAutoReleaseOptIn(bool)` (caller-set) — newly-tunable economic parameters (replace the old `FEE_BPS`/`TIMEOUT` constants).

**New events:** `TradeOpenedWithDeposit`, `TradeAutoReleased`, `DisputeFeeApplied`, `AutoReleaseWindowUpdated`, `TrustedForwarderUpdated`, `Permit2Updated` (+ keep all existing events).

---

## 11. Deploy & migration

Pre-launch (no real funds) → **fresh deploy**:
1. Deploy v2 behind a new UUPS proxy; new storage layout (no compat constraint).
2. `initialize(owner, admins)`; set `systemWallet`, accepted tokens, `mcgpToken`; leave `trustedForwarder`/`autoReleaseWindow`/`disputeFee` unset (disabled); set `permit2` if available on Sonic.
3. Repoint `sonicPresets[mainnet].p2pEscrow` and `[testnet].p2pEscrow` in `tsa-api-go/internal/config/config.go`.
4. Merchants re-deposit into v2; deprecate the old proxy.

A migration helper in the contract is **not** needed (nothing to migrate).

### 11.1 Upgradeability & tunability (per the "adjust without redeploy" requirement)
- **Logic** is upgradeable: v2 keeps the **UUPS** pattern (`UUPSUpgradeable` + `_authorizeUpgrade` `onlyOwner`), so behavior can be changed by upgrading the implementation behind the existing proxy — no redeploy, no address change, balances preserved. The implementation **must declare a storage `__gap`** and follow append-only storage rules so future upgrades stay layout-safe (the *current* contract has no gap — add one in v2).
- **Parameters** are adjustable without even an upgrade, via the owner setters above (`feeBps`, `paymentWindow`, `autoReleaseWindow`, `disputeFee`, `trustedForwarder`, `permit2`, `systemWallet`, accepted tokens, admins, `mcgpToken`).
- **Security tradeoff (call-out):** making `feeBps`/`paymentWindow` settable widens owner power vs. today's constants. Mitigations baked in: **hard max-caps** on both, **fee snapshot-at-open** (in-flight trades immune), and the existing **`sweepExcess` fence** which still prevents the owner from ever touching `available`/`locked` principal. Recommend the owner key be a **multisig/timelock** (operational, not contract).

---

## 12. Testing & audit

Hardhat suite mirroring `test/P2PEscrow.test.js`, adding:
- Both flows' happy paths (sell-ad pooled; buy-ad JIT).
- `cancelByTimeout` refund routing per `funding` (pool-credit vs transfer-out).
- `dispute → adminResolve` both directions, refund routing per `funding`.
- Fee split + MCGP fee-free, both flows.
- `accountedBalance` invariant asserted after **every** path.
- Permit2 deposit/open + `transferFrom` direct-call fallback.
- Forwarded-call `_msgSender()` resolution (EIP-2771) on every user-facing fn.
- Auto-release: disabled-by-default (`finalizeRelease` reverts at window `0`); enabled path (set window → markPaid → finalize after elapse; dispute-before-finalize blocks it).
- Voucher overfill/reuse/expiry/cancel (sell-ad); self-trade + reentrancy guards both flows.
- **Tunable params:** `setFeeBps`/`setPaymentWindow` respect their caps (revert above max / outside bounds); a fee change does **not** alter an already-open trade's settlement (snapshot holds); `setAutoReleaseOptIn` gates `finalizeRelease`; UUPS upgrade preserves balances + storage layout (gap present).

**Re-audit the full contract before mainnet** (this is the single audit the design-complete scope buys us).

---

## 13. Scope boundary — follow-on sub-specs (NOT in this spec)

1. **Gasless service** — relayer (EIP-2771 forwarder ops), Permit2 client-side signing flows, Sonic FeeM registration, build-vs-buy (Biconomy/Pimlico/ZeroDev).
2. **Optimistic auto-release rollout** — keeper job + the per-merchant "⚡ auto-release" opt-in UX + window policy.
3. **Dispute v2 (off-chain)** — evidence storage, self-serve wizard, **tsa-admin wallet signing**, dispute-fee amounts/UX, arbitrator console.
4. **Backend integration** — role-based trade lifecycle, buy-ad model + endpoints, `openTradeWithDeposit` tx-building, reconciler/watchdog updates for JIT trades, two-sided money invariant. **Must mirror the contract's `Funding` enum to a `p2p_trades` column** (today's `P2PTrade` has no funding/side discriminator and `P2PAd.Side` defaults `'sell'`) so the reconciler knows whether a refund returns to a pool or to the user's wallet.
5. **App** — sell-side UI, two-sided (Buy/Sell) browse, Permit2/meta-tx signing flows, new trade screens.

---

## 14. Open prerequisites (verify before/inside build)
- **Permit2 deployed on Sonic** (address) — needed for the Permit2 paths; the `transferFrom` fallback works regardless.
- **EIP-2771 forwarder** choice (OZ `ERC2771Forwarder`) — contract just needs the address; relayer is a later sub-project.
- **Sonic FeeM** registration terms/eligibility.
- **tsa-admin wallet signing** — hard prereq for end-to-end dispute resolution (own sub-project); contract is ready regardless.
- **Fee model** — stays deferred; contract keeps 0.5%/MCGP-free.

## References
- Current contract: `smart-contracts/contracts/P2PEscrow.sol`
- On-chain recommendation: `docs/p2p/design/2026-05-28-p2p-onchain-architecture-recommendation.md`
- Custody research: `docs/p2p/design/2026-05-28-p2p-telegram-wallet-architecture-research.md`
- Permit2: https://github.com/Uniswap/permit2 · EIP-2771: https://eips.ethereum.org/EIPS/eip-2771 · OZ Metatx: https://docs.openzeppelin.com/contracts/5.x/api/metatx
