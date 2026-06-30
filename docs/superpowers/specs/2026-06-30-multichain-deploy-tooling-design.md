# Multi-chain deploy tooling — Sonic + BSC ready for all contracts

**Date:** 2026-06-30
**Status:** Design — approved, pending spec review
**Repo affected:** `tsa-dev/smart-contracts` (Hardhat). No contract source changes; tooling only.

## Problem

All production contracts are standard EVM and the Hardhat config already targets
Sonic (testnet/mainnet) and BSC (testnet/mainnet). But the **deploy tooling** is
not chain-portable: scripts are bespoke per-(contract, network), and the
chain-specific addresses they need (USDT/USDC/MCGP, Permit2, forwarder, owner,
admins, system wallet) are scattered across env vars and hardcoded inline
defaults. Only **P2PEscrowV2** has a BSC bundle today. Deploying the rest to BSC
means hand-porting each script with new addresses — error-prone and divergent.

## Goal

Make every latest-version contract deployable to Sonic **or** BSC by selecting
the Hardhat network, from a single source of truth for per-chain addresses.
Replace the scattered bespoke deploy/wire/upgrade scripts with one config-driven
system. **Tooling only — no on-chain transactions are broadcast as part of this
work** (the operator deploys when ready).

## Decisions (from brainstorming)

1. **Scope:** Tooling ready, no live deploy. Deliver config + unified scripts +
   dry-run/compile verification; do not broadcast to any real chain.
2. **Contracts (latest of each family):** `P2PEscrowV3`, `ProductEscrowV2`,
   `ServiceContactV2`, `FeeManager`, `MCGPRewardDistributor`. Legacy V1/V2 get no
   new BSC path.
3. **MCGPRewardDistributor:** included and network-parameterized, but its BSC
   deploy fails fast until an MCGP-on-BSC token address exists in config (MCGP is
   Sonic-only today). No special-casing — just an unconfigured address.
4. **Approach C (full refactor):** replace the bespoke deploy/wire/upgrade scripts
   with the config-driven system and delete them. Chosen over keeping the old
   scripts alongside (A) for a single clean source of truth. Safety: behavior is
   migrated and dry-run-verified before any old script is deleted; deletions are
   recoverable from git history.

## Architecture

New layout under `smart-contracts/deploy/`:

### `deploy/chains.js` — single source of truth

Per-chain config keyed by Hardhat network name:

```js
module.exports = {
  sonicMainnet: { tokens: { USDT, USDC, MCGP }, permit2, forwarder, owner, admins, systemWallet },
  sonicTestnet: { ... },
  bscMainnet:   { tokens: { USDT, USDC, MCGP: null }, permit2: CANONICAL_PERMIT2, forwarder, owner, admins, systemWallet },
  bscTestnet:   { ... },
};
```

- Known addresses pre-filled: existing Sonic main/test and BSC-testnet addresses
  (migrated from the current scripts' hardcoded defaults / `.env`), the canonical
  Permit2 `0x000000000022D473030F116dDEE9F6B43aC78BA3` on BSC, and BSC-mainnet
  USDT (`0x55d398326f99059fF775485246999027B3197955`) / USDC
  (`0x8AC76a51cc950d9822D68b83FE1Ad97B32Cd580d`).
- `MCGP` on BSC stays `null` (no token yet); scripts needing it fail fast.
- Secrets (deployer keys) remain in `.env`; only public addresses live here.

### `deploy/lib.js` — shared helpers

- `resolveChain(hre)` → config row for the active `--network`; errors on an
  unconfigured network.
- `requireAddr(cfg, path)` → fail-fast accessor; throws
  `"<field> not configured for <network>"` on a missing/`null` address.
- Address **ledger**: read/write `deployments/<network>.json` recording each
  contract's proxy + implementation address, so wiring and upgrades can resolve
  dependencies (e.g. an escrow finding the FeeManager).
- Wiring helpers: token acceptance, token symbols, FeeManager linkage.

### `deploy/<contract>.js` — one unified script per family

Each resolves the active chain, deploys the **latest** version, runs its
post-deploy wiring (preserving exactly what the replaced scripts did), and writes
to the ledger. Runnable on any network: `npx hardhat run deploy/feemanager.js --network bscTestnet`.

| Script | Deploys | Post-deploy wiring (preserved) |
|---|---|---|
| `feemanager.js` | `FeeManager` UUPS proxy, `initialize(owner)` | chain-agnostic; rate seeding is a separate op |
| `p2p-escrow.js` | `P2PEscrowV3` proxy, `initialize(owner, admins)` | `setSystemWallet`; `setTokenAcceptance` per token; `setMcgpToken` only if MCGP configured (skipped on BSC); `setPermit2` / `setTrustedForwarder` if configured; FeeManager link + `setTokenSymbol` per token |
| `product-escrow.js` | `ProductEscrowV2` proxy | `setMcgpToken` (if configured); `setTokenAcceptance`; FeeManager link |
| `service-contact.js` | `ServiceContactV2` proxy | `setTokenAcceptance`; `setFeeAmount(token, fee)` |
| `mcgp-distributor.js` | `MCGPRewardDistributor` (non-proxy), `constructor(mcgp, owner)` | — (BSC fails fast until MCGP-on-BSC configured) |

UUPS proxies deploy via OZ `upgrades.deployProxy({ kind: 'uups' })`; the
`.openzeppelin/<network>.json` manifests keep working unchanged.

### Orchestration, idempotency, upgrades

- **Order:** escrows reference the FeeManager, so `feemanager → {p2p, product,
  service} → mcgp-distributor`. Escrow scripts read the FeeManager address from
  the ledger (or an override). A thin `deploy/all.js` runs them in order for a
  fresh chain; each is also runnable standalone.
- **Idempotency:** each script checks the ledger first; if a proxy already exists
  for the network it re-runs **wiring only** (no redeploy) and logs that — safe
  re-runs on Sonic, no duplicate proxies.
- **Upgrades:** the three `upgrade-*.js` collapse into one config-driven
  `deploy/upgrade.js <contract>` that `upgradeProxy`'s the existing proxy
  (address from ledger / OZ manifest) to the latest impl. Fresh BSC chains have
  nothing to upgrade — they deploy the latest version directly.

## Scripts removed vs kept

**Deleted (superseded) → replacement:**
- `deploy-feemanager.js` → `deploy/feemanager.js`
- `deploy-mainnet-escrow.js` → `deploy/product-escrow.js`
- `deploy-mainnet-service-contact.js` → `deploy/service-contact.js`
- `deploy-mcgp-distributor.js` → `deploy/mcgp-distributor.js`
- `deploy-p2p-escrow.js`, `deploy-p2p-escrow-v2.js` → `deploy/p2p-escrow.js`
- `deploy-bsc-testnet-p2p-bundle.js`, `deploy-bsc-testnet-p2p-v2-bundle.js` → `deploy/p2p-escrow.js`
- `deploy-fresh-escrows-v2-testnet.js` → `deploy/product-escrow.js` + `deploy/service-contact.js`
- `deploy-testnet.js`, `deploy-testnet-p2p-bundle.js`, `deploy-testnet-upgradeable.js` → unified scripts
- `wire-bsc-p2p-escrow.js`, `wire-p2p-escrow-feemanager.js` → folded into escrow wiring
- `upgrade-p2p-escrow-v3.js`, `upgrade-product-escrow-v2.js`, `upgrade-service-contact-v2.js` → `deploy/upgrade.js`

**Refactored (kept):** `seed-feemanager-rates.js`, `seed-feemanager-pm-rates.js`
— source the FeeManager/token **addresses** from `chains.js` instead of
hardcoding them; the rate values they seed stay in the seed scripts (operational
data, not deployment config).

**Kept untouched (not deployment — diagnostics / ops / tests):** all `check-*`,
all `smoke-*` (+ README), `faucet-testnet-stables`, `mint-to-user`,
`discover-bsc-tokens`, `probe-pool-balance`, `prep-product-escrow-testnet`,
`flip-splits-off-testnet`, `add-admin`, `forceimport-p2p-escrow-v2`,
`deploy-bsc-test-tokens` (BSC-testnet mock-token helper feeding `chains.js`).

Note: exact filenames will be reconciled against the live `scripts/` directory
during implementation; any deploy/wire/upgrade script not listed above is mapped
to a unified replacement (or explicitly kept) before deletion.

## Error handling

- Unconfigured network (`--network` not in `chains.js`) → `resolveChain` throws.
- Missing required address (e.g. MCGP on BSC) → `requireAddr` fails fast with a
  network-qualified message; no partial deploy.
- Re-run on an already-deployed network → wiring-only, never a duplicate proxy.

## Testing / verification (no real chain)

- `npx hardhat compile` clean.
- **Dry-run harness:** on the in-process Hardhat network, deploy mock ERC-20s,
  then run every unified deploy script end-to-end against them — proves deploy +
  full wiring without broadcasting. This is the gate before any old script is
  deleted.
- **Config-completeness check:** asserts `sonicMainnet`, `sonicTestnet`,
  `bscTestnet` have all required addresses and `bscMainnet` has everything except
  MCGP — so a known-good Sonic deploy can't silently regress.
- Existing `npx hardhat test` contract suite stays green (contracts unchanged).
- Unit tests for `deploy/lib.js` (`resolveChain` / `requireAddr` fail-fast).

## Out of scope

- Any live on-chain deployment (operator runs scripts when ready).
- Contract source changes.
- Deploying an MCGP token on BSC (the reward distributor stays gated on it).
- Backend multi-chain wiring changes in `tsa-api-go` (addresses are consumed via
  existing per-chain env config once a real deploy happens).
