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

## Deterministic escrow (same address on every EVM chain)

`deploy/deterministic-escrow.js` deploys a `P2PEscrowV3` at ONE address on every
EVM chain via CREATE2 through the standard deterministic deployment proxy
`0x4e59b44847b379578588920ca78fbf26c0b4956c` (Arachnid). It is for NEW escrows
(first: direct P2P on BSC); existing deployments are never redeployed.

    # dry run: prints the would-be addresses, sends nothing (no key needed)
    CHECK_ONLY=1 DET_DEPLOYER=0x… npx hardhat run deploy/deterministic-escrow.js --network bscTestnet
    # deploy + wire + hand over to the PM
    DET_OWNER_CONFIRM=0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96 \
      npx hardhat run deploy/deterministic-escrow.js --network bscMainnet

Any network in `hardhat.config.js` works if the factory has code there; the
script refuses clearly if it does not (fund `0x3fab…5362` with 0.01 native and
broadcast its presigned tx first). On the local hardhat network it installs the
real factory from that same presigned tx.

**What sets the address**

| | salt label (salt = keccak256 of it) | init code |
|---|---|---|
| implementation | `tsa:P2PEscrowV3:impl:v1` | `P2PEscrowV3` creation code |
| proxy | `tsa:P2PEscrowV3:proxy:<DET_NAME>` (default `direct-p2p:v1`) | `ERC1967Proxy(impl, initialize(DEPLOYER, [OPERATOR]))` |

`ERC1967Proxy` is the precompiled artifact bundled with `@openzeppelin/upgrades-core`
(pinned by `package-lock.json`). So the proxy address depends on: the compiled
`P2PEscrowV3` bytecode, the **deployer address**, the **operator address** and
`DET_NAME`. Owner, treasury, tokens, FeeManager, window and fee are per-chain
setup and do not move the address.

**Rule: the deployer key must be the same address on every chain.** The
deployer is the temporary owner in the init calldata.

**Env**

| var | default | meaning |
|---|---|---|
| `DET_OWNER` | `0x62bA…1b96` (PM) | final owner; ownership moves here last |
| `DET_OWNER_CONFIRM` | — | must equal `DET_OWNER` (one-step Ownable is irreversible); required to deploy |
| `DET_OPERATOR` | `0x38cE…c2c9` | sole initial admin (release admin) — part of the address |
| `DET_TREASURY` | `0xA412…e205` | `systemWallet` |
| `DET_NAME` | `direct-p2p:v1` | proxy salt suffix — a new escrow purpose gets a new name |
| `DET_TOKENS` | every non-null token in `chains.js` | comma list of symbols to accept |
| `DET_FEE_MANAGER` | unset | per-chain FeeManager proxy to wire |
| `DET_PAYMENT_WINDOW`, `DET_FEE_BPS` | contract defaults (30 min, 50) | optional overrides |
| `DET_DEPLOYER` | the key's address | CHECK_ONLY without a key; must match the key otherwise |

Role defaults live in `DETERMINISTIC_ROLES` (`chains.js`).

**Manifest** — `deploy/deterministic.json` (committed) records, per proxy salt,
both salts, init-code hashes, addresses, init args and the chain ids it landed
on. A run whose deployer, operator or compiled bytecode would put the proxy
anywhere else is refused before anything is sent. Commit the manifest after the
first live deploy; deploy later chains from the commit that recorded it (a
changed `P2PEscrowV3.sol` changes the implementation and therefore the proxy).

**Run order and re-runs** — factory check → impl CREATE2 (reused if present) →
proxy CREATE2 (reused if present) → proxy code + impl slot verified → manifest
written → `upgrades.forceImport` (so `deploy/upgrade.js` / `upgradeProxy` work
later) → `wireEscrow` (shared with `p2p-escrow.js`) + window/fee → every value
read back → `transferOwnership(PM)` and owner read back. Re-running where the
proxy is still deployer-owned resumes the setup; where it is PM-owned it only
verifies (code, impl, admins, system wallet, tokens) and sends nothing.

**Front-running** — the factory is permissionless, so anyone can submit our
CREATE2 first. The init calldata is fixed (owner = our deployer), so a
front-runner only pays our gas; the script treats code at the predicted address
as success. Because owner = deployer is a hot key, setup and handover always run
in the same invocation and the owner is verified at the end.

FeeManager is not deterministic: it is per-chain wiring, not part of the escrow
address, and keeps using `deploy/feemanager.js`.
