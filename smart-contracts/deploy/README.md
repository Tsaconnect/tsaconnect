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

**Re-runs:** `deploy/all.js` reuses any contract already recorded in
`deployments/<network>.json`, so re-running it after a mid-sequence failure
resumes rather than orphaning fresh proxies. Set `FORCE_REDEPLOY=1` to redeploy
everything. Running a single `deploy/<contract>.js` module directly always
deploys a fresh proxy and overwrites its ledger entry — to update an existing
proxy in place, use `deploy/upgrade.js`.

## Note on upgrade.js vs upgrade-p2p-escrow-v3.js

The generic `deploy/upgrade.js` intentionally does NOT carry the one-shot
`initializeV3` re-initializer call that existed in the now-deleted
`scripts/upgrade-p2p-escrow-v3.js`. That call was a historical V2→V3 migration
step (initializer-guarded) and would revert if run again. If a future one-shot
re-initialization is ever needed for a new contract version, write a dedicated
migration script and run it separately after the proxy upgrade.

## Pool (TSA Liquidity)

`deploy/pool.js` deploys the zero-fee liquidity pool (a `FeeManager` plus a `P2PEscrowV3` accepting only the pool tokens) on any network whose `chains.js` entry has a `pool` block (BSC today):

    POOL_RELEASE_ADMIN=0x… POOL_SYSTEM_WALLET=0x… POOL_OWNER_ADDRESS=0x… POOL_OWNER_CONFIRM=0x… \
      npx hardhat run deploy/pool.js --network bscTestnet

- `POOL_RELEASE_ADMIN` is the PM release EOA, `POOL_SYSTEM_WALLET` the conversion treasury, `POOL_OWNER_ADDRESS` the owner's wallet (decision 18). Ownership moves last, after every value is read back and asserted. One-step Ownable: the owner address is final.
- `requireOwner` (bscMainnet) refuses to deploy without an owner wallet, with one equal to the deployer, or without a matching `POOL_OWNER_CONFIRM`. bscTestnet keeps the deployer as owner when none is set.
- Ledger keys are `PoolFeeManager` / `PoolP2PEscrowV3`, so the pool never overwrites the main `P2PEscrowV3` entry.
- Never run `scripts/seed-feemanager-rates.js` against the pool FeeManager: it sets a non-zero `p2p_platform` rate.
- Never lower `paymentWindow` while slices are open (M4).
- Never `pause()` the pool escrow while paid slices exist. `adminRelease` is `whenNotPaused` (`P2PEscrowV3.sol:535`); the incident stop is `pool_order_max = 0`.
