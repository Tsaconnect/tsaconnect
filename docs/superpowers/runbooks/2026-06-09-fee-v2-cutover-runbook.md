# Fee v2 + FeeManager — Deploy & Cutover Runbook

**Date:** 2026-06-09
**Branch:** `feat/fee-tp-overhaul` (contains the fee/TP overhaul **and** the FeeManager after PR #35)
**Scope:** The combined go-live of the v2 fee/TP engine **and** the configurable on-chain FeeManager. They share **one flag** (`FEE_MODEL_V2_ENABLED`), so this is **one cutover**, not two.
**Audience:** Operator/deployer with the Gnosis Safe signers + backend env + RPC access.

> **READ THIS FIRST.** Everything in this release is dormant until the flag flips. The single most important invariant: **the backend boot guard refuses to start if `FEE_MODEL_V2_ENABLED=true` while any escrow still has `legacySplitEnabled=true` or `feeManager=0x0`** (`escrow_cutover_guard.go` → `main.go`). This is the double-pay interlock. The entire sequence below exists to satisfy it safely. Do the steps **in order**; do **not** flip the flag early.

---

## 0. Prerequisites & open items (P0 — resolve BEFORE mainnet)

| # | Item | Status | Owner |
|---|------|--------|-------|
| P0-1 | **Smart-contract security RE-AUDIT** of FeeManager.sol + P2PEscrowV3 + ProductEscrowV2 + ServiceContactV2 + MCGPRewardDistributor.sol | **REQUIRED, not done** | security |
| P0-2 | **FeeManager deploy script does not exist** (`smart-contracts/scripts/deploy-feemanager.js`). Must be authored: deploy UUPS proxy, owner = Safe, `CHECK_ONLY=1` dry-run, print address. | **MISSING** | backend |
| P0-3 | **PM target rates for `p2p` (gas) and `p2p_platform`** are not pinned in the spec. Confirm exact bps. | **DECISION** | PM |
| P0-4 | **Product gas "0.5%" semantics**: the seed is a flat `$0.10` (`product`/flatUSD); the PM target says 0.5%. Confirm whether it stays flat or becomes 0.5%-of-order, and how the cart quote computes it. | **DECISION** | PM |
| P0-5 | **Gnosis Safe provisioned** (threshold signers) and set as `owner()` of the FeeManager + all 3 escrows + the reward distributor. | TODO | ops |
| P0-6 | Every chain with a configured escrow has a **reachable RPC** at backend boot (the boot guard fails-safe if the reader is nil). | TODO | ops |

**Do not proceed to mainnet (§3) until P0-1…P0-6 are closed.** Testnet rehearsal (§2) can proceed now to validate the mechanics.

---

## 1. Artifacts & reference

### Contracts (audit-gated)
| Contract | Type | Script | Network names |
|---|---|---|---|
| `FeeManager.sol` | UUPS proxy | **author `deploy-feemanager.js`** (P0-2) | `sonicMainnet`(146)/`sonicTestnet`(14601)/`bscMainnet`(56)/`bscTestnet`(97) |
| `P2PEscrowV3` | UUPS upgrade | `scripts/upgrade-p2p-escrow-v3.js` (sets `legacySplitEnabled=true` via `initializeV3`) | same |
| `ProductEscrowV2` | UUPS upgrade | `scripts/upgrade-product-escrow-v2.js` (`initializeV2`) | sonic only |
| `ServiceContactV2` | UUPS upgrade | `scripts/upgrade-service-contact-v2.js` (`initializeV2`) | sonic only |
| `MCGPRewardDistributor.sol` | standalone (not upgradeable) | `scripts/deploy-mcgp-distributor.js` | sonic |

All upgrade scripts honor `CHECK_ONLY=1` (validate storage layout + print plan, no tx) and need `DEPLOYER_PRIVATE_KEY` + the proxy address env (`P2P_ESCROW_PROXY` / `PRODUCT_ESCROW_PROXY` / `SERVICE_CONTACT_PROXY`).

### Backend env vars (`internal/config/config.go`)
| Var | Default | Effect |
|---|---|---|
| `FEE_MODEL_V2_ENABLED` | `false` | **The master flag.** Gates the v2 engine, the admin set-fee endpoints (409 while OFF), and the boot guard. |
| `FEE_MANAGER_ADDRESS_SONIC` | unset | FeeManager proxy on Sonic. Validated `IsHexAddress`+non-zero at load (fatal if malformed). Unset = FeeManager dormant for Sonic. |
| `FEE_MANAGER_ADDRESS_BSC` | unset | FeeManager proxy on BSC. Unset = dormant. |
| `REWARD_DISTRIBUTOR_ADDRESS` | unset | MCGPRewardDistributor proxy. Unset = root-publish dormant. |
| `P2P_ESCROW_ADDRESS` / `_BSC` | preset | Live P2P escrow proxy per chain (already set). |
| `PRODUCT_ESCROW_ADDRESS` | `0x6c96…415d` | Sonic ProductEscrow proxy. |
| `SERVICE_CONTACT_ADDRESS` | `0x1304…91d1` | Sonic ServiceContact proxy. |
| `FEE_TP_PER_USD` | `1.0` | TP minting rate (pacing). |
| `FEE_GLOBAL_POOL_SETTLE_INTERVAL` | `24h` | Global-pool epoch cadence (dormant until flag). |
| `ALLSWAP_FEE_DEVIATION_MAX` | `0.10` | AllSwap anti-forgery threshold. |
| `ALLSWAP_CONSERVATIVE_FEE_BPS` | `350` | AllSwap fee when LiFi unreachable. |
| `REWARD_VELOCITY_*` | 20 / 24h | Anti-sybil claim gate. |

### Admin endpoints (all `AdminAuth` + `RequireP2PAdmin`; all **409 while flag OFF** except cutover-reset)
| Endpoint | Purpose |
|---|---|
| `POST /api/admin/fee-tp/cutover-reset` | One-time balance reset. Body `{label, confirm}`, `confirm="I_UNDERSTAND_THIS_ZEROES_BALANCES"`. |
| `POST /api/admin/fees/intent` | Create a rate intent. |
| `POST /api/admin/fees/plan` | Get the diff + unsigned `setRate` txs per chain. |
| `POST /api/admin/fees/confirm` | Verify receipt + on-chain read-back == intent, then write the replica. |
| `DELETE /api/admin/fees/intent/:id` | Cancel an intent (only while not fully applied). |
| `POST /api/admin/reward-distributions/publish` | Build a cumulative epoch → unsigned `setMerkleRoot` tx. |
| `POST /api/admin/reward-distributions/confirm` | Verify the broadcast `setMerkleRoot`. Body `{epoch, txHash}`. |

### Rate table — current seed vs PM target (apply at §2.E / §3.E)
| Feature key | token | Current | PM target | Note |
|---|---|---|---|---|
| `product_merchant` | `*` | 200 bps | 200 bps | unchanged (2%) |
| `product` (gas) | `*` | $0.10 flat | **0.5%** | **P0-4: confirm semantics** |
| `service_contact` | `*` | $0.10 flat | $0.10 flat | unchanged |
| `withdrawal` | `*` | 50 bps | 50 bps | unchanged (0.5%) |
| `otc` | `*` | 50 bps | **200 bps** | 0.5% → 2% |
| `swap`/`spot` | `*` | 10 bps | **300 bps** | 0.1% → 3% |
| `allswap` | `*` | (10) | **300 bps** | 3% |
| `p2p` (gas) | `*` | 50 bps | **TBD** | **P0-3** |
| `p2p_platform` | `*` | 100 bps | **TBD** | **P0-3** |

MCGP is fee-exempt on every feature (the `mcgpExempt` flag).

---

## 2. TESTNET REHEARSAL (Sonic testnet 14601) — do this first, end to end

> Goal: rehearse the exact mainnet sequence on testnet so timings, signer flow, and validations are proven before touching mainnet. Use the testnet Safe (or a test EOA owner). **This is the dress rehearsal for §3.**

### 2.A — Deploy FeeManager (testnet)
```bash
cd smart-contracts
# dry-run first
CHECK_ONLY=1 OWNER_ADDRESS=<testnet-safe> npx hardhat run scripts/deploy-feemanager.js --network sonicTestnet
# real
OWNER_ADDRESS=<testnet-safe> npx hardhat run scripts/deploy-feemanager.js --network sonicTestnet
```
**Record** the FeeManager proxy address → `FM_SONIC_TESTNET`.
**GATE:** `feeManager.owner() == <testnet-safe>`.

### 2.B — Seed FeeManager with CURRENT rates (Safe signs `setRate`)
Seed every feature in the rate table at its **CURRENT** value (NOT the PM target yet) so the escrows, once wired, charge exactly today's rates. Use `setRates` (batch) signed by the Safe.
**GATE:** for each feature, `feeManager.rateFor(feature, "*")` returns the current value (read-back).

### 2.C — Upgrade the escrows (deployed with `legacySplitEnabled=true`)
```bash
CHECK_ONLY=1 P2P_ESCROW_PROXY=<testnet-p2p> npx hardhat run scripts/upgrade-p2p-escrow-v3.js --network sonicTestnet
P2P_ESCROW_PROXY=<testnet-p2p>      npx hardhat run scripts/upgrade-p2p-escrow-v3.js     --network sonicTestnet
PRODUCT_ESCROW_PROXY=<testnet-prod> npx hardhat run scripts/upgrade-product-escrow-v2.js --network sonicTestnet
SERVICE_CONTACT_PROXY=<testnet-svc> npx hardhat run scripts/upgrade-service-contact-v2.js --network sonicTestnet
```
**GATE:** each `escrow.legacySplitEnabled() == true` (scripts assert this) and storage-layout validation passed.

### 2.D — Wire each escrow to the FeeManager (Safe signs)
For each escrow: `setFeeManager(FM_SONIC_TESTNET)` and `setTokenSymbol(tokenAddr, "USDT"/"USDC"/…)` for every accepted token.
**GATE:** `escrow.feeManager() == FM_SONIC_TESTNET`, and a sample `openTrade`/quote charges **today's rate** (escrow live-reads FeeManager → current rates → unchanged). `legacySplitEnabled` still `true` → on-chain split still happens. **Zero behavior change so far, flag still OFF.**

### 2.E — Deploy + fund the reward distributor
```bash
CHECK_ONLY=1 MCGP_TOKEN_ADDRESS=<testnet-mcgp> OWNER_ADDRESS=<testnet-safe> \
  npx hardhat run scripts/deploy-mcgp-distributor.js --network sonicTestnet
MCGP_TOKEN_ADDRESS=<testnet-mcgp> OWNER_ADDRESS=<testnet-safe> FUND_AMOUNT=<n> \
  npx hardhat run scripts/deploy-mcgp-distributor.js --network sonicTestnet
```
**Record** → `REWARD_DISTRIBUTOR_ADDRESS`.

### 2.F — Point the backend at the new contracts (still flag OFF)
Set in the testnet backend env, then **restart**:
```
FEE_MANAGER_ADDRESS_SONIC=<FM_SONIC_TESTNET>
REWARD_DISTRIBUTOR_ADDRESS=<distributor>
# FEE_MODEL_V2_ENABLED stays false
```
**GATE:** backend boots cleanly. Boot guard is a **no-op** (flag OFF). The drift reconciler now compares FeeManager↔replica (chain==replica==current). Charging still uses the legacy path.

### 2.G — One-time cutover-reset (flag still OFF)
```bash
curl -X POST $TESTNET/api/admin/fee-tp/cutover-reset \
  -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" \
  -d '{"label":"2026-06-09-testnet-rehearsal","confirm":"I_UNDERSTAND_THIS_ZEROES_BALANCES"}'
```
**GATE:** 200 + `ResetSummary`. Archives tp/cashback ledgers → `*_legacy`, zeros balances, clears v2 state, records the `cutover_batches` sentinel. (Re-run with the same label → 409.) **Must be before the flag flip** so pre-cutover cashback can't re-mint as MCGP.

### 2.H — THE WINDOW (keep tight; there is a brief no-reward gap)
> Order is forced by the boot guard. Do **not** flip the flag before the splits are off.
1. **Flip on-chain splits off** — Safe signs `setLegacySplitEnabled(false)` on **all 3 escrows**. On-chain cashback/upline split stops; fee now goes 100% to system. *(From here until step 3, no reward is distributed — minimize the gap.)*
   **GATE:** each `escrow.legacySplitEnabled() == false`.
2. **Flip the flag** — set `FEE_MODEL_V2_ENABLED=true`, **restart** the backend.
   **GATE (the interlock):** the boot guard runs — it eth_calls each configured escrow and **passes only if all are `legacySplitEnabled=false` + `feeManager!=0`**. Look for `[fee-v2] escrow cutover guard PASSED`. If it **fatals**, the splits weren't all flipped → fix and restart. The off-chain v2 engine is now the sole splitter, at **current** rates → the gap closes.
3. **Apply PM target rates** (flag is ON, 409 is lifted) — per feature in the rate table, `POST /fees/intent` → `/fees/plan` → Safe signs the returned `setRate` tx → `POST /fees/confirm {intentId, chainId, txHash}`. Confirm verifies receipt + on-chain read-back == intent before writing the replica.
   **GATE:** `quote == charge` for each feature; `fee_rate_drift_total` stays 0.
4. **Publish the reward root** — `POST /api/admin/reward-distributions/publish` → Safe signs the returned `setMerkleRoot` tx → `POST /reward-distributions/confirm {epoch, txHash}`.

### 2.I — Validate the rehearsal
- A test trade/withdrawal/swap on each feature charges the **PM rate**, accrues a `FeeEvent`, and the off-chain engine splits cashback+TP in MCGP.
- Withdrawal fee + FeeEvent are in one DB tx (no leak).
- A test user can claim MCGP via the Rewards screen.
- `quote == charge`, conservation/drift metrics clean, boot guard PASSED.

**If the rehearsal is clean, proceed to §3. If not, fix on the branch and re-rehearse.**

---

## 3. MAINNET CUTOVER (Sonic 146; repeat per chain for BSC when it launches)

**Pre-flight (all must be true):** P0-1…P0-6 closed; testnet rehearsal clean; a maintenance window scheduled; the Safe signers available; a DB backup taken; the rollback plan (§4) reviewed.

Run the **identical** sequence as §2, against mainnet, with the mainnet Safe and `--network sonicMainnet`:

- **3.A** Deploy FeeManager (mainnet) → record `FM_SONIC`. Owner = mainnet Safe (behind the 6h timelock if used).
- **3.B** Seed FeeManager with **current** rates (Safe `setRates`); read-back verify.
- **3.C** Upgrade escrows (`--network sonicMainnet`, `CHECK_ONLY=1` first). `legacySplitEnabled=true`.
- **3.D** Wire `setFeeManager(FM_SONIC)` + `setTokenSymbol` per token; verify a live quote == today's rate.
- **3.E** Deploy + **fund** MCGPRewardDistributor; record address.
- **3.F** Set `FEE_MANAGER_ADDRESS_SONIC` + `REWARD_DISTRIBUTOR_ADDRESS` in the mainnet env; restart (flag still OFF); confirm clean boot + no-op guard.
- **3.G** `POST /api/admin/fee-tp/cutover-reset {label:"2026-06-09-mainnet-cutover", confirm:"I_UNDERSTAND_THIS_ZEROES_BALANCES"}`. **Verify 200 + summary.**
- **3.H** THE WINDOW (tight): (1) Safe `setLegacySplitEnabled(false)` on all 3 escrows → verify `false`; (2) set `FEE_MODEL_V2_ENABLED=true` + restart → **confirm the boot guard PASSED** (this is the go/no-go); (3) apply PM rates via intent→plan→sign→confirm → verify quote==charge; (4) publish + confirm the reward root.
- **3.I** Post-cutover validation (§2.I) on mainnet. **Record the cutover block/timestamp here:** `__________`.

> **BSC:** repeat 3.A–3.D + 3.H.1/3.H.3 per the BSC escrow/FeeManager once BSC P2P launches (`FEE_MANAGER_ADDRESS_BSC`, `--network bscMainnet`). The cutover-reset (3.G) and the flag (3.H.2) are backend-global — done once, not per chain.

---

## 4. Rollback / abort

| Stage reached | To roll back |
|---|---|
| Before 3.H (flag still OFF) | Fully reversible with no user impact: unset `FEE_MANAGER_ADDRESS_*`/`REWARD_DISTRIBUTOR_ADDRESS` + restart → back to legacy. The escrow upgrades are deployed but `legacySplitEnabled=true` = today's behavior; the FeeManager is just an unused contract. (The cutover-reset, if already run in 3.G, is **one-way** — only run it when committed to proceeding.) |
| After 3.H.1 (`legacySplitEnabled=false`) but before flag ON | Safe `setLegacySplitEnabled(true)` on all escrows → on-chain split resumes (legacy behavior). |
| After 3.H.2 (flag ON) | To revert: Safe `setLegacySplitEnabled(true)` on all escrows **first**, then set `FEE_MODEL_V2_ENABLED=false` + restart (order matters — the boot guard does not block flag-OFF, but reverting splits first keeps reward behavior consistent). Note the cutover-reset is not undone (balances were intentionally zeroed); restoring legacy balances means restoring from the `*_legacy` archive tables + the DB backup. |
| Contract-level defect found | UUPS upgrade back to the prior implementation (Safe, re-audit the rollback impl). The proxy address + state are preserved. |

**Code-deploy reversibility note:** deploying a V2/V3 implementation is a normal UUPS upgrade — rolling the *code* back is another upgrade, not a flag flip. The `legacySplitEnabled` bool only reversibly gates *future* settlements; already-settled fees are immutable.

---

## 5. Post-cutover monitoring (first 24–48h)

- `fee_rate_drift_total` — must stay 0 (FeeManager↔replica in sync). Any increment → investigate the reconciler/RPC.
- `fee_v2_settle_failed_total` — permanent settle failures; manual re-drive if non-zero.
- `fee_rate_cache_miss_total` — the §11 fallback ladder firing; should be ~0 in steady state.
- Conservation: per-feature split sums to FeeUSD; the P2P money invariant (`token_amount` Σ == on-chain `balanceOf` − ad-pool remaining).
- Quote==charge spot-checks across product/withdrawal/swap/OTC.
- Reward claims succeed for KYC-verified users; treasury MCGP balance vs. outstanding claimable.

---

## Appendix — critical invariants (do not violate)

1. **Boot guard:** `FEE_MODEL_V2_ENABLED=true` ⇒ every configured escrow must already be `legacySplitEnabled=false` + `feeManager!=0`. The server fatals otherwise. → splits off **before** flag on, always.
2. **Reset before flag:** run `cutover-reset` while the flag is OFF, exactly once, or pre-cutover cashback double-pays as MCGP.
3. **Seed current rates first:** the FeeManager holds **today's** rates before the escrows are wired, so wiring changes nothing; PM rates are applied only after the flag is on.
4. **Rates via confirmed read-back:** the replica is written only from an on-chain `rateFor` read-back that equals the intent (verify-then-write). Never hand-edit `fee_config`.
5. **No backend keys:** every on-chain write is a Safe-signed tx the backend only *prepares* and *confirms*. The backend holds no key.
6. **MCGP is fee-exempt** on every feature.
