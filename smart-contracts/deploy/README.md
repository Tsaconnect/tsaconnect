# Deploy tooling

Per-chain config lives in `deploy/chains.js` (addresses only; keys in `.env`).
Deploy any contract to any network:

    npx hardhat run deploy/feemanager.js --network bscTestnet
    npx hardhat run deploy/all.js --network sonicMainnet      # full stack, in order

Networks: sonicMainnet, sonicTestnet, bscMainnet, bscTestnet.
MCGPRewardDistributor requires an MCGP token; it is skipped/fails-fast on BSC
until `chains.js` has a BSC MCGP address. Upgrades:

    UPGRADE_CONTRACT=FeeManager npx hardhat run deploy/upgrade.js --network sonicMainnet

Deployed addresses are recorded in `deployments/<network>.json` (git-ignored).

**Re-running a deploy script is NOT idempotent** — it deploys a fresh proxy and overwrites the ledger entry. To upgrade an existing proxy instead, use `deploy/upgrade.js`.

## Note on upgrade.js vs upgrade-p2p-escrow-v3.js

The generic `deploy/upgrade.js` intentionally does NOT carry the one-shot
`initializeV3` re-initializer call that existed in the now-deleted
`scripts/upgrade-p2p-escrow-v3.js`. That call was a historical V2→V3 migration
step (initializer-guarded) and would revert if run again. If a future one-shot
re-initialization is ever needed for a new contract version, write a dedicated
migration script and run it separately after the proxy upgrade.
