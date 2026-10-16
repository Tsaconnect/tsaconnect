# Deploy tooling

Per-chain config lives in `deploy/chains.js` (addresses only; keys in `.env`).
Deploy any contract to any network:

    npx hardhat run deploy/feemanager.js --network bscTestnet
    npx hardhat run deploy/all.js --network sonicMainnet      # full stack, in order

Networks: sonicMainnet, sonicTestnet, bscMainnet, bscTestnet.
MCGPRewardDistributor requires an MCGP token; it is skipped/fails-fast on BSC
until `chains.js` has a BSC MCGP address. Upgrades:

    UPGRADE_CONTRACT=FeeManager npx hardhat run deploy/upgrade.js --network sonicMainnet

The ledger key `P2PEscrowV3` is the main escrow. A deterministic escrow (below)
is upgraded only through `UPGRADE_DET_NAME`, which resolves the proxy from the
committed `deploy/deterministic.json` for this network class and chain id:

    UPGRADE_CONTRACT=P2PEscrowV3 UPGRADE_DET_NAME=direct-p2p:v1 \
      npx hardhat run deploy/upgrade.js --network bscMainnet

The signer (`DEPLOYER_PRIVATE_KEY`) must own the proxy — the PM after handover.

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

    # deploy + wire + hand over to the PM — the cold deploy key is typed in, hidden, for this run only
    read -r -s -p "Deploy key (hidden): " K; echo
    DET_DEPLOYER_PRIVATE_KEY="$K" DET_OWNER_CONFIRM=0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96 \
      npx hardhat run deploy/deterministic-escrow.js --network bscMainnet
    unset K

    # verify a chain that is already handed over (no key, sends nothing)
    npx hardhat run deploy/deterministic-escrow.js --network bscMainnet

Any network in `hardhat.config.js` works if the factory has code there; the
script refuses clearly if it does not (fund `0x3fab…5362` with 0.01 native and
broadcast its presigned tx first). On the local hardhat network it installs the
real factory from that same presigned tx.

**What sets the address**

| | salt label (salt = keccak256 of it) | init code |
|---|---|---|
| implementation | `tsa:P2PEscrowV3:impl:v1` | `P2PEscrowV3` creation code |
| proxy | `tsa:P2PEscrowV3:proxy:<DET_NAME>` (default `operator-p2p:v1`) | `ERC1967Proxy(impl, initialize(DEPLOYER, [OPERATOR]))`, or `initialize(DEPLOYER, DET_ADMINS)` when an admin list is given |

`ERC1967Proxy` is the OpenZeppelin v5 artifact bundled with
`@openzeppelin/upgrades-core` (`artifacts/@openzeppelin/contracts-v5/…`, pinned
by `package-lock.json`) — the same proxy `hardhat-upgrades` deploys for every
other proxy here. So the proxy address depends on: the compiled `P2PEscrowV3`
bytecode, the **deployer address**, the **operator address** (or the explicit
admin list, in order) and `DET_NAME`.
Owner, treasury, tokens, FeeManager, window and fee are per-chain setup and do
not move the address.

**Rule: the chain must support the compiled opcodes.** Everything compiles with
`evmVersion: "cancun"` (`hardhat.config.js`), and the OZ v5 code uses `mcopy` /
transient storage. The address is the same everywhere, but the contract only
WORKS on chains with Cancun opcodes (BSC, Sonic, Ethereum, Base, Arbitrum…).
Before a new chain, check it activated Cancun (its docs / hardfork table) and
probe it: `cast call --rpc-url <rpc> --create 0x5f5c5000` runs `PUSH0 TLOAD POP
STOP` and prints `0x` on a Cancun chain; an `invalid opcode` error means do not
deploy there. Never change `evmVersion` to fit
a chain: that changes the bytecode and therefore both addresses.

**Rule: the deployer key must be the same address on every chain** (of a
network class, below). The deployer is the temporary owner in the init calldata.

**Rule: the deployer is a cold key kept for deployments only.** Anyone can
replay the CREATE2 calldata on a chain we have not used yet, and the address
it lands at is owned by the deployer, so whoever holds the deployer key can
claim "our" address on any new chain. The script therefore:
- never uses the hardhat-config signer; the key comes only from
  `DET_DEPLOYER_PRIVATE_KEY` in the process env of that one command (typed with
  `read -s` as above, never stored),
- refuses it when it equals the routine `.env` `DEPLOYER_PRIVATE_KEY` or any
  hardhat-config account, and refuses a `.env` that contains
  `DET_DEPLOYER_PRIVATE_KEY`,
- refuses the operator or treasury (hot server keys) as the deployer.

**Rule: trust an address per chain, never by equality.** The same address on
another chain is not ours until it is recorded for that chain (here in
`deploy/deterministic.json`, and in the API's preset row for that chain).

**Env**

| var | default | meaning |
|---|---|---|
| `DET_DEPLOYER_PRIVATE_KEY` | — | the cold deploy key, this run only; required to send anything |
| `DET_DEPLOYER` | the key's address, else the manifest's recorded deployer | address-only CHECK_ONLY / verify; must match the key if both given |
| `DET_OWNER` | `0x62bA…1b96` (PM) | final owner; ownership moves here last |
| `DET_OWNER_CONFIRM` | — | must equal `DET_OWNER` (one-step Ownable is irreversible); required to deploy |
| `DET_OPERATOR` | `0x38cE…c2c9` | sole initial admin (release admin) — part of the address |
| `DET_ADMINS` | unset (= `[DET_OPERATOR]`) | comma list replacing `[DET_OPERATOR]` as the initial admins — part of the address. Set-but-empty (`DET_ADMINS=`) is refused, never read as unset. Refused when empty, duplicated, or naming the operator, the treasury, the deployer or a routine deploy key; the read-back then also asserts none of those is an admin |
| `DET_TREASURY` | `0xA412…e205` | `systemWallet` |
| `DET_NAME` | `operator-p2p:v1` | proxy salt suffix — a new escrow purpose gets a new name. A `direct-p2p:*` name is refused without `DET_ADMINS` (it is the direct escrow's salt; use `deploy/p2p-direct.js`) |
| `DET_TOKENS` | every non-null token in `chains.js` | comma list of symbols; each must have code |
| `DET_FEE_MANAGER` | unset | per-chain FeeManager proxy to wire; must have code |
| `DET_PAYMENT_WINDOW` | contract default (30 min) | integer seconds in [300, 86400] (the contract's bounds) |
| `DET_FEE_BPS` | contract default (50) | integer in [0, 500] |

Role defaults live in `DETERMINISTIC_ROLES` (`chains.js`). Every value is
validated before any CREATE2 is sent or the manifest is written.

**Manifest** — `deploy/deterministic.json` (committed) is keyed by network
class (`mainnet` / `testnet`, from the network name), then proxy salt label;
each entry records both salts, init-code hashes, addresses, init args and the
chain ids it landed on. A run whose deployer, operator or compiled bytecode
would put the proxy anywhere else than its class's entry is refused before
anything is sent. The class split is deliberate: a testnet rehearsal with a
throwaway deploy key records under `testnet` and never pins the mainnet address
(nor is it refused by it), so testnet and mainnet addresses differ unless the
same deploy key is used for both. Commit the manifest after the first live
deploy; deploy later chains from the commit that recorded it (a changed
`P2PEscrowV3.sol` changes the implementation and therefore the proxy).

**Run order and re-runs** — validate every input → factory check → if the proxy
is already PM-owned: verify code, impl slot, owner, admins, system wallet,
tokens and stop (no key needed, nothing sent; CHECK_ONLY does the same) →
impl CREATE2 (reused if present) → proxy CREATE2 (reused if present) → proxy
code + impl slot verified → manifest written → `upgrades.forceImport` (so
`deploy/upgrade.js` with `UPGRADE_DET_NAME` works later) → `wireEscrow` (shared
with `p2p-escrow.js`; skips values already set) + window/fee → every value read
back → `transferOwnership(PM)` and owner read back. Re-running where the proxy
is still deployer-owned resumes the setup without resending what is done.

**Front-running** — the factory is permissionless, so anyone can submit our
CREATE2 first. The init calldata is fixed (owner = our deployer), so a
front-runner only pays our gas; the script treats code at the predicted address
as success. Until the handover the proxy is owned by the cold deploy key, so
setup and handover always run in the same invocation and the owner is verified
at the end — the cold key never holds an escrow beyond that one run.

FeeManager is not deterministic: it is per-chain wiring, not part of the escrow
address, and keeps using `deploy/feemanager.js`.

## Direct P2P escrow (BSC)

`deploy/p2p-direct.js` deploys the direct (user-to-user) P2P escrow as a thin
layer over the deterministic path above: the escrow lands at the same CREATE2
address on every chain, owned by the PM, with `systemWallet` = the treasury
(`DET_OWNER` / `DET_TREASURY`), and its **admins are the dispute arbiters only**
(`ARBITER_ADDRESSES` is passed as the deterministic admin list — never the
operator `0x38cE…`, the treasury or any hot key). It also deploys its **own**
`FeeManager` (`deploy/feemanager.js`, never a shared one) with the same cold deploy
key — no routine key ever owns a direct contract — and writes the live rates
before anything is handed over. Its salt is `DET_NAME` (default `direct-p2p:v1`;
any other name must also start with `direct-p2p:`).

**The owner is always an admin.** `P2PEscrowV3.isAdmin(owner)` is true whatever
the admin list says, so the PM owner can `adminRelease` / `adminResolve` (and
add or remove admins) on its own. "Arbiters only" means the explicit admin set;
the owner is the one more address with admin power. That is why the owner must
never be a hot or deploy key: the run refuses an owner that is a forbidden admin
(the operator / treasury roles, the canonical `0x38cE…c2c9` / `0xA412…e205`,
`DIRECT_FORBIDDEN_ADMINS`), a routine deploy key (`.env`
`DEPLOYER_PRIVATE_KEY`, any hardhat-config account), the routine hardhat signer
or the cold deployer — before anything is sent, and again right before the
handover.

    read -r -s -p "Deploy key (hidden): " K; echo
    DET_DEPLOYER_PRIVATE_KEY="$K" DET_OWNER_CONFIRM=0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96 \
    ARBITER_ADDRESSES=0x…,0x… \
    DIRECT_RATES_JSON='{"p2p_platform":{"bps":100},"p2p":{"bps":50}}' \
      npx hardhat run deploy/p2p-direct.js --network bscMainnet
    unset K

| Env var | Meaning |
|---|---|
| `ARBITER_ADDRESSES` | Comma list of dispute arbiters: the escrow's **only** admins. In `initialize()`, so the list (and its order) is part of the address — use the same list on every chain. |
| `DIRECT_RATES_JSON` | `{"p2p_platform":{"bps":…},"p2p":{"bps":…}}`; optional `flatUSD` (1e6 micro-USD) and `mcgpExempt`. Written active for USDT and USDC. Each leg ≤ 300 bps, total ≤ 500 bps (the escrow's snapshot caps). |
| `DIRECT_FORBIDDEN_ADMINS` | Comma list that must never be an admin or the owner: **put the relayer (`RELAYER_PRIVATE_KEY`'s address) here**, plus any role key that ever differs from the operator. Always forbidden besides it: the `DET_OPERATOR` / `DET_TREASURY` roles, the canonical operator `0x38cE…c2c9` and treasury `0xA412…e205` (hard-coded, whatever the env says), the deployers and the routine deploy keys. |
| `DIRECT_DEPLOY_BLOCK` | Only to resume a run whose escrow proxy already exists, or to re-run against one already handed over without the ledger: the block it was created in (the read-back's event scan starts there). |
| `DIRECT_FEE_MANAGER_ADDRESS` | Only to re-run against an escrow already handed over without the ledger: its FeeManager, for the read-back. |
| `DIRECT_OWNER_IS_DEPLOYER` | `1` = the cold deployer keeps both contracts — no handover (owner decision 2026-10-16) — until the owner moves them with `transferOwnership`. `DET_OWNER_CONFIRM` must then be the deployer's address. `isAdmin(owner)` is always true on P2PEscrowV3, so the deployer is an arbiter meanwhile. The deploy key itself must not be a forbidden admin (`DIRECT_FORBIDDEN_ADMINS`, the canonical operator/treasury, the role addresses), a routine deploy key or the routine hardhat signer; every other hot key stays refused. Re-running a finished deploy with the flag only reads back (set `DIRECT_FEE_MANAGER_ADDRESS` + `DIRECT_DEPLOY_BLOCK` or keep the ledger) and sends nothing. Pass it to `verify-direct-escrow.js` too, with `DET_DEPLOYER`. Set `expectedOwner` in the API's BSC row to the deployer. |
| `DET_*` | As for the deterministic escrow: `DET_DEPLOYER_PRIVATE_KEY`, `DET_OWNER_CONFIRM`, `DET_OWNER`, `DET_TREASURY`, `DET_OPERATOR`, `DET_NAME`, `CHECK_ONLY`. `DET_ADMINS`, `DET_TOKENS`, `DET_FEE_MANAGER`, `DET_PAYMENT_WINDOW` and `DET_FEE_BPS` are set by the module, not read. |

Order: (1) every refusal with nothing sent — the deterministic check-only pass
(admin list, deploy key, manifest, tokens), the owner against every hot or
deploy key, then `DET_OWNER_CONFIRM`; a chain whose escrow is already PM-owned
runs the full read-back (below) and stops — a mismatch, or no FeeManager /
deploy block to read back from (ledger or `DIRECT_FEE_MANAGER_ADDRESS` +
`DIRECT_DEPLOY_BLOCK`), fails the run; a deployer-owned escrow that is already
wired (`feeManager()` set — a finished owner-is-deployer run, or one interrupted
after the FeeManager handover) is refused with nothing sent rather than resumed,
because resuming would deploy a new FeeManager and re-point the live escrow at it:
hand it over with `transferOwnership` instead (only an unwired proxy resumes); (2) `FeeManager` deployed and owned by
the cold deploy key (it is not deterministic) and `setRates`; (3) the escrow
through `deterministic-escrow.js` with `admins = arbiters`, wired to that
FeeManager, `paymentWindow` 1800 (the initializer's value, asserted), no
forwarder, USDT + USDC only, read back — still deployer-owned (`deferOwnership`);
(4) everything read back again (arbiters are admins; every forbidden address
is not; rates; window; forwarder zero; no MCGP), then the full read-back below
with the deployer as the expected owner of both — the event scan proves the
admin set is exactly the arbiters **before** anything is handed over, and any
mismatch refuses the handover; (5) the owner re-checked against freshly read
hot keys, then ownership of **both** contracts to the PM, last; (6) the ledger
written, and the full read-back again with the PM as owner. `feeBps` keeps the
initializer's 50 (only used if a rate row is switched inactive).

Never run `deterministic-escrow.js` directly for direct P2P: without an admin
list it would make the operator the admin, so it refuses any `direct-p2p:*`
name without `DET_ADMINS` (and its default name is `operator-p2p:v1`). Ledger
keys are `DirectFeeManager` / `DirectP2PEscrowV3` (the latter also records
`name` and `deployer`), each with the `deployBlock` the read-back scan starts
from; both are written only after the handover, so a failed run never leaves a
ledger entry pointing at a deployer-owned FeeManager (re-run with
`DIRECT_DEPLOY_BLOCK`, as the log says). Never run
`scripts/seed-feemanager-rates.js` against the direct FeeManager: it writes `*`
wildcard rows the escrow never reads.

A mismatch in the post-handover read-back fails the run, but ownership has
already moved, so fix it from the owner.

### Verifying the direct escrow

`scripts/verify-direct-escrow.js` is read-only and exits non-zero on any
mismatch. With the same env vars as the deploy (no key, no `DET_OWNER_CONFIRM`;
the deployer comes from `DET_DEPLOYER=0x…` or the ledger, and without it the
CREATE2 check fails) it checks the escrow is the predicted CREATE2 address for
(deployer, arbiters, `DET_NAME`) and both contracts are the OpenZeppelin v5
`ERC1967Proxy`; the FeeManager is not the main escrow's (ledger `FeeManager`);
both `owner()`s are `DET_OWNER`, which is no forbidden or routine deploy key;
both implementation slots (escrow: the ledger's, else the deterministic
implementation); every wired value (the same checks the deploy asserts —
`systemWallet` is `DET_TREASURY`; the forbidden set, the deployer and every
routine deploy key are not admins); and scans `AdminAdded` / `AdminRemoved` /
`OwnershipTransferred` / `TokenAcceptanceUpdated` from the deploy block in
2,000-block chunks: the admin set must equal the arbiters exactly, every
`AdminRemoved` must follow an `AdminAdded` of that address, the accepted tokens
must be USDT + USDC exactly, and each contract's ownership chain must start at
the zero address, be continuous (each transfer is from the previous new owner)
and end at `owner()`. Finding no deploy-time events is itself a failure (wrong
start block, or an RPC that silently returns empty logs).

    npx hardhat run scripts/verify-direct-escrow.js --network bscMainnet

Addresses and the start block come from the ledger, or `DIRECT_ESCROW_ADDRESS`,
`DIRECT_FEE_MANAGER_ADDRESS`, `DIRECT_DEPLOY_BLOCK`. The RPC must serve
2,000-block `eth_getLogs` (the public BSC dataseeds do not).
