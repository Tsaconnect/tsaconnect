# Runbook — turn on the USDC MCGP swap (Contract B)

Push-button deploy + wiring for the **already-merged** USDC feature (`McgpEntitlementSwap`,
`/api/mcgp/*`, `app/wallet/mcgp`). The feature is **dormant** until `MCGP_SWAP_B_ADDRESS` is set, so
nothing activates before step 5.

## 0. Prerequisites
- **Owner multisig** address (Gnosis Safe) — will own the contract.
- **Treasury wallet** holding MCGP (premium inventory) + USDC (redemption float), with its key for
  the deployer (or run funding as separate multisig txs).
- Sonic RPC + a funded deployer EOA (gas in S).
- The **pre-launch holder snapshot** (see step 3).
- Addresses (Sonic mainnet): MCGP `0x517600323e5E2938207fA2e2e915B9D80e5B2b21`,
  USDC `0x29219dd400f2Bf60E5a23d13Be72B486D4038894`.

## 1. Deploy Contract B
```bash
cd mcgp-otc-contracts && npm install
# Dry-run first:
CHECK_ONLY=1 OWNER_ADDRESS=<multisig> npx hardhat run scripts/deploy-entitlement-swap.js --network sonic
# Real deploy (set prices in USDC 6dp per MCGP; defaults = buy $0.02 / sell $0.018 / legacy $0.01):
OWNER_ADDRESS=<multisig> \
PREMIUM_BUY_PRICE=20000 PREMIUM_SELL_PRICE=18000 LEGACY_SELL_PRICE=10000 \
npx hardhat run scripts/deploy-entitlement-swap.js --network sonic
# Note the printed address -> <SWAP_B_ADDR>. Verify:
npx hardhat verify --network sonic <SWAP_B_ADDR> <multisig> <MCGP> <USDC>
```
If `OWNER_ADDRESS` ≠ deployer, the script prints the `addPhase(...)` calls to submit **from the
multisig** (deploy can't configure phases it doesn't own).

## 2. Confirm price phases
Each `PriceKind` (PremiumBuy=0, PremiumSell=1, LegacySell=2) needs one active phase. Verify:
```bash
# via cast/ethers: getPhases(0|1|2) non-empty, activePhase(kind)=0
```
Adjust later from the multisig with `updatePhasePrice(kind, idx, newPrice)` / `setActivePhase`.

## 3. Seed legacy entitlements (snapshot)
Build `legacyEntitlement[user]` = each pre-launch holder's MCGP `balanceOf` at the chosen **snapshot
block N** (anything acquired after N is not legacy). Export `[address, amount(wei)]` pairs, then:
```bash
# Owner-only; batch (≤ ~400 per tx to stay under gas). Submit from the multisig.
seedLegacy([addr1, addr2, ...], [amt1, amt2, ...])
```
For a large holder set, prefer batched `seedLegacy` calls. (A merkle `claimLegacy` variant is noted
in the spec as a future option if the set is very large.)

## 4. Fund backing + float (treasury, from the multisig or treasury EOA)
```bash
# Premium must be 1:1 backed before any buy; USDC float covers redemptions.
mcgp.approve(<SWAP_B_ADDR>, <MCGP_AMOUNT>);  fund(<MCGP>, <MCGP_AMOUNT>)   # premium inventory
usdc.approve(<SWAP_B_ADDR>, <USDC_AMOUNT>);  fund(<USDC>, <USDC_AMOUNT>)   # redemption float
# Sanity: backing() -> (held, owed); right after funding owed=0, held=<MCGP_AMOUNT>.
```

## 5. Wire the API (flips the feature live)
```bash
# tsa-api-go env:
MCGP_SWAP_B_ADDRESS=<SWAP_B_ADDR>
# restart the server. Expect logs:
#   [config] MCGP swap (Contract B) enabled: <SWAP_B_ADDR>
# and the mcgp-swap-reconciler registered. /api/mcgp/* now respond.
```
Verify: `GET /api/mcgp/quote?kind=premiumBuy&amount=1000000000000000000` returns a USDC quote;
`GET /api/mcgp/balance` (authed) returns zeros for a fresh user.

## 6. App
```bash
cd tsa-app && npx expo start   # regenerates expo-router types for /wallet/mcgp/*
```
Smoke test on device: Buy (approve USDC → buyPremium → balance up) → Pay @username → Sell → confirm
the ledger + on-chain `backing()` stay consistent.

## 7. Verify end-to-end (testnet first)
- `buyPremium` of X → `premiumBalance(user)` = X, `backing()` owed += X, held ≥ owed.
- `spend` X to a second user → balances move, owed unchanged.
- `sellPremium` / `withdrawPremiumToWallet` → owed decreases; `withdraw(MCGP, excess)` only removes
  `held-owed`.
- `sellLegacy` up to entitlement → USDC out; beyond entitlement reverts.
- Reconciler logs no `mcgp_swap_backing_drift_total`.

## Rollback / kill-switch
- **Pause:** `pause()` from the multisig — blocks buy/spend/sell; `withdrawPremiumToWallet` stays
  available (trustless exit).
- **Full dormant:** unset `MCGP_SWAP_B_ADDRESS` + restart → routes + reconciler disappear; on-chain
  balances remain claimable via `withdrawPremiumToWallet`.
- Excess MCGP/USDC recoverable by the owner via `withdraw(token, to, amount)` (MCGP excess-only).
