# P2P On-Chain Architecture — Recommendation (what works best for TSA Connect)

**Date:** 2026-05-28
**Companion to:** `2026-05-28-p2p-telegram-wallet-architecture-research.md` (custody landscape) and the smart-contract-P2P survey.
**Decision driver:** user wants great UX, is open to changing contracts, and (after seeing the custody tradeoff) wants to **stay smart-contract-based**. Concerns: per-transaction fees, and the current **sell-only** flow being under-designed.

---

## Verdict

**Stay non-custodial / on-chain. Make it *feel* gasless via gas sponsorship. Redesign the escrow to be two-sided with role parameters.** Do **not** go custodial (Telegram model) — it buys marginal smoothness at the price of money-transmission licensing, KYC/AML/proof-of-reserves, and geo-restrictions. On Sonic you can get ~custodial smoothness without any of that.

Two facts make this clear-cut:
1. **Sonic gas is <$0.01/tx and FeeM rebates up to ~90% of app fees to you** ([Sonic gas](https://docs.soniclabs.com/sonic/build-on-sonic/gas-pricing), [FeeM](https://docs.soniclabs.com/funding/fee-monetization)). The cost isn't the problem — the *signing friction + needing the gas token* is.
2. Every serious non-custodial P2P (LocalCryptos, LocalCoinSwap, OpenPeer) removes that friction with a **relayer / meta-transaction** so users only sign. Your stack can do the same.

---

## What you already have vs. best practice (gap analysis)

Your current `P2PEscrow`: pool vault (sellers deposit) + **EIP-712 voucher** ad creation (already gasless) + on-chain `openTrade` (buyer) + `release` (seller) + `cancelByTimeout` + admin dispute resolve. That's already ~70% of the proven design.

| Best-practice trait | You today | Action |
|---|---|---|
| Gasless ad creation (EIP-712 voucher) | ✅ have it | keep |
| Pool/vault custody | ✅ have it | keep |
| **Gasless trade actions** (open/release sponsored) | ❌ buyer & seller pay gas + sign | **add relayer / meta-tx (EIP-2771)** |
| **Two-sided** (buy & sell) | ❌ sell-only (hardcoded `Side: sell`) | **generalize to role-parameterized escrow** |
| **Approvals without `approve` tx** | ❌ approve+deposit | **Permit2** (USDT has no `permit`) |
| **Auto-release on timeout** (optimistic) | ⚠️ only `cancelByTimeout` | **add optimistic auto-release window** |
| Loser-pays dispute fee (anti-griefing) | ❌ | add small dispute fee, loser forfeits |
| Arbitrator can only pay a trade party (never self) | ✅ adminResolve picks direction | keep; **fix tsa-admin signing** (see risks) |
| Reputation / trust signals | ⚠️ minimal | off-chain reputation service (Phase 0) |

**Conclusion: this is an evolution + re-audit of your existing contract, not a teardown.**

---

## Recommended escrow design

### One role-parameterized contract (handles both directions)
The fiat leg is always off-chain, so **only the crypto side is ever escrowed**. Model a trade by roles, not "buyer/seller":

```
Trade {
  cryptoDepositor   // funds escrow with crypto
  cryptoRecipient   // receives crypto on release
  fiatPayer         // sends fiat off-chain
  fiatReceiver      // receives fiat off-chain
  token, amount, fee, bond
  state, fundedAt, fiatMarkedAt, autoReleaseAt
}
```
- **User BUYS crypto** (merchant sells from pool): depositor = merchant, recipient = user; fiatPayer = user.
- **User SELLS crypto** (merchant buys): depositor = user, recipient = merchant; fiatPayer = merchant.
- Only *who deposits* and *release direction* differ. **One state machine, one audit surface, one dispute path** for both.

State machine: `Funded → FiatMarkedSent → (Released | Refunded | Disputed → Resolved)`.

### Single shared contract, trades keyed by hash
Follow LocalCoinSwap: one deployed instance, `trades[tradeId]` mapping, minimal per-trade storage. Avoid per-trade or per-seller contract deployment (wastes gas). (Your current single P2PEscrow already matches this.) Reference blueprint (open source): https://github.com/LocalCoinSwap/trc20-escrow-contract

### Gas: sponsor everything
- **Relayer + EIP-2771 trusted forwarder** (OpenZeppelin `ERC2771Forwarder`, `ERC2771Context`) — lowest complexity, no bundler dependency; users sign instruction messages, relayer submits + pays. This is the LocalCryptos/LocalCoinSwap model.
- **Permit2** for pulling the depositor's tokens in one settlement tx (works for USDT *and* USDC, unlike bare EIP-2612 which USDT lacks). https://github.com/Uniswap/permit2
- **Keep a direct-call fallback** so the relayer is never a custody/censorship single point of failure.
- **Register the contract for Sonic FeeM** to reclaim up to ~90% of sponsored gas. Don't build per-trade gas metering at these prices.
- *Later upgrades (verify Sonic support first):* EIP-7702 (native batching/sponsorship on an EOA) or ERC-4337 + session keys ("sign once per session").

### Dispute model (layered)
1. **Optimistic auto-release window** (default): once fiat is marked received, a challenge window runs; if undisputed, **crypto auto-releases on timeout** — removes the seller's manual `release` as a hard blocker. Generous window (hours) to dodge timestamp jitter; "mark fiat sent" pauses/extends rather than letting it run blind.
2. **Platform (or bonded) arbitrator** on dispute: an `arbitrator` address that can only direct escrow to one of the two trade parties (never itself), gated by a party signature + winner flag. Add a **small dispute fee, loser forfeits / winner refunded** (OpenPeer) to deter griefing.
3. **Optional Kleros (ERC-792) appeal tier** later — too slow/costly as the primary path for small fiat trades, fine as last resort.

### Recommended happy-path lifecycle (sell-crypto direction; buy is roles swapped)
| Step | On/off-chain | Who signs | Gas |
|---|---|---|---|
| 1. Maker posts ad | off-chain | maker (EIP-712 order/voucher) | none |
| 2. Taker accepts | off-chain match | taker (EIP-712) | none |
| 3. Settle: pull depositor crypto → escrow | **on-chain** (Permit2 + deposit, batched) | depositor's Permit2 sig | platform (relayer) |
| 4. Fiat paid | off-chain | — | none |
| 5. Mark fiat received → opens window | on-chain | fiatReceiver | platform |
| 6. Auto-release after window (happy path) | **on-chain** (timeout finalize; keeper/relayer pokes) | nobody | platform |
| 5b/6b. Dispute (bonded) → arbitrator resolves | on-chain | disputer + arbitrator | platform |

**~2 on-chain txs on the happy path, both sponsored; user only ever signs.** Matches custodial-app smoothness while staying non-custodial.

---

## Phasing

- **Phase 0 (no contract change, ship now):** the custody-agnostic UX layer from the companion doc — Express auto-match, merchant trust stats (completion %, avg release time, ⚡, online), countdown timers + explicit order state machine + push, region-scoped payment picker, dispute wizard, merchant collateral. Biggest perceived-UX ROI; works on the current escrow.
- **Phase 1 (contract v2 + re-audit):** the two-sided, gas-sponsored, Permit2, optimistic-auto-release escrow above. This is where "create ad / release" stop feeling like transactions and the buy *and* sell sides both work.
- **Phase 2 (optional):** EIP-7702/4337 session keys; bonded/Kleros arbitration; on-chain reputation anchoring.

---

## Verify before building (flagged by research)
- **Sonic support for EIP-7702 (Pectra) and ERC-4337 bundlers/paymasters** — newer L1; confirm before depending on AA. (EIP-2771 relayer works regardless.)
- **Stablecoin `permit` support** — USDT lacks it → use Permit2.
- **Sonic FeeM registration terms** (eligibility/whitelisting).
- **tsa-admin wallet signing** — the arbitrator must be a signing key; per existing memory, tsa-admin has no wallet signing yet, so the dispute-resolve flow can't complete end-to-end. Prerequisite for any on-chain dispute resolution.

## Key references
- LocalCoinSwap contract (blueprint): https://github.com/LocalCoinSwap/trc20-escrow-contract
- LocalCryptos ETH escrow (live): https://etherscan.io/address/0xede26ccc85ae521d06b5ef2604952a421c9124b6
- OpenPeer docs (EVM P2P, gasless via relayer, dispute fee): https://docs.openpeer.xyz
- Permit2: https://github.com/Uniswap/permit2 · EIP-2771: https://eips.ethereum.org/EIPS/eip-2771 · OZ Metatx: https://docs.openzeppelin.com/contracts/5.x/api/metatx
- UMA optimistic oracle (auto-release pattern): https://blog.uma.xyz/articles/what-is-umas-optimistic-oracle
- Sonic gas / FeeM: https://docs.soniclabs.com/sonic/build-on-sonic/gas-pricing · https://docs.soniclabs.com/funding/fee-monetization
