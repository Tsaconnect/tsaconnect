# PM Test Guide — Fees + Trade-Points (TP) Rewards

**What this is:** the new fee model + Trade-Points reward engine, live on **Sonic testnet**. Every fee a user pays now splits into system revenue, cashback (buyer + their direct upline), and a TP pool distributed across the buyer's 1st–10th-generation uplines. TP accumulates a user up a **15-level named ladder**; each level crossed pays a cash-equivalent reward in MCGP that the user claims.

**Environment:** Sonic **testnet** (chain 14601). The v2 engine is **ON**. This is a test build — balances/data are test only.

---

## 1. Demo account (pre-seeded so you see the feature populated)

| | |
|---|---|
| **Email** | `demo@tsaconnect.app` |
| **Password** | `DemoTest#2026` |
| Seeded state | **TP Level 4 — Connector**, ~1,434 TP, ~$2,250 cashback, claimable MCGP, with upline generations also populated |

Log in as the demo account to see a fully-populated Rewards/TP dashboard immediately. (You can also create your own account and generate activity — see §3.)

---

## 2. The 15 TP levels (new names)

| Lvl | Name | | Lvl | Name |
|---|---|---|---|---|
| 1 | Beginner | | 9 | Director |
| 2 | Explorer | | 10 | Legend |
| 3 | Trader | | 11 | Visionary |
| 4 | Connector | | 12 | Sovereign |
| 5 | Influencer | | 13 | Titan |
| 6 | Ambassador | | 14 | Pinnacle |
| 7 | Elite | | 15 | Icon |

Level *n* needs 10^(n-1) TP (L1=1, L2=10, L3=100, …). Each level's reward = 1% of its threshold. **L6+ also carry a physical prize** (admin-fulfilled).

---

## 3. What to test → expected result

### A. Rewards / TP dashboard (the headline screen)
1. Open the **Rewards / Trade-and-Earn** screen as the demo user.
   - **Expect:** a hero showing the current rank by **name** ("Level 4 · Connector"), progress toward the next rank ("…% to Level 5 · Influencer"), TP balance, cashback, and claimable MCGP.
   - The **15-level ladder** lists each rung as "Level n · Name" with its TP threshold + reward; crossed levels are marked earned, the current one in-progress, the rest locked.
   - L6+ rows show a **prize** chip.

### B. Earning (fees → cashback + TP)
2. Do a fee-generating action — a **product purchase**, a **P2P trade**, or a **withdrawal**.
   - **Expect:** the fee is charged, and shortly after, the buyer's **cashback** and **TP** increase. The buyer *and their direct referrer* both get cashback; TP flows up the buyer's referral generations (most to the buyer, less to each higher generation).
   - Crossing a TP threshold fires a **level-up notification** naming the rank ("You reached TP Level 6 — Ambassador!") — in-app, push, and email.

### C. Fees shown at checkout / withdrawal
3. On checkout / withdrawal, confirm the **fee shown matches what's charged** (quote == charge): a percentage merchant fee + a flat gas fee on product; the % fee on withdrawals/swaps.

### D. Claim
4. Open the claim flow from the Rewards screen.
   - **Expect on testnet:** you can see the claimable amount and start a claim, but the **on-chain settlement will fail** — see Known Limitations. The app should show a clear error, **not** a false "claimed!" success.

---

## 4. Known limitations on this testnet build (by design / env, not bugs)

- **On-chain claim can't complete on testnet.** The MCGP token isn't deployed on Sonic testnet, so the claim transaction has nothing to transfer. You can test the whole flow up to the on-chain step; the claim itself is validated on mainnet (or once MCGP is on testnet). Everything *before* the claim — fees, cashback, TP, levels, the claimable computation — works and is what to evaluate here.
- **PM fee rates not yet applied.** Rates are at their current values; the PM-confirmed targets (OTC 2%, Spot/Allswap 3%) and the open P2P/Product-gas numbers are applied at the deliberate rate cutover. Pin the P2P/Product-gas numbers when ready and they'll go live.
- **Sonic-only.** BSC is scoped out of this test build (it gets its own cutover).
- **Cashback → claimable timing.** Cashback accrues to a "cashback earned" balance and converts to claimable MCGP on reward processing (within ~1 min). A large cashback with a small immediate claimable is expected mid-conversion, not a bug.

---

## 5. Reference (testnet, chain 14601)

| Contract | Address |
|---|---|
| FeeManager | `0xfc9c062E68ad71121f9709c5275C3DE94086bb1a` |
| P2P escrow (V3) | `0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7` |
| Product escrow (V2) | `0x4A9059E53e7fc11f9c3264685e25614Fde8cB2b7` |
| Service escrow (V2) | `0x58A1fb3383de18DF75c686127Edc8cA9F56e9638` |
| Reward distributor | `0xD2Cc612F6C01c0dea9058567759282F9d862c4f4` (unfunded; MCGP not on testnet) |

The fee/TP engine, the FeeManager (admin-configurable on-chain rates), and the cutover were validated end-to-end on testnet before this build.
