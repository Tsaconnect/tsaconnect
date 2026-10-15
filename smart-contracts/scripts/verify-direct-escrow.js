// Read-back verifier for the direct P2P escrow deployed by deploy/p2p-direct.js
// (plan docs/plans/p2p-bsc C-2). Read-only: it sends no transaction and exits
// non-zero on any mismatch.
//
// Checks, against the same chains.js `direct` block and deterministic roles
// (DET_OWNER, DET_TREASURY, DET_OPERATOR) the deploy used:
//   - the escrow is the predicted CREATE2 address for (deployer, arbiters,
//     DET_NAME) and both contracts are the OpenZeppelin v5 ERC1967Proxy;
//   - the FeeManager is not the main escrow's (ledger);
//   - owner() of both contracts — never a forbidden or routine deploy key —
//     and both ERC-1967 implementation slots (escrow: the ledger's, else the
//     deterministic implementation);
//   - every wired value (shared with the deploy's own asserts —
//     deploy/p2p-direct.js directMismatches): feeManager, feeBps, rates,
//     paymentWindow, systemWallet == treasury, trustedForwarder == 0,
//     paused == false, accepted tokens + symbols, isAdmin(arbiter) true,
//     isAdmin(forbidden) false (operator, treasury — the roles and the
//     canonical hot keys —, DIRECT_FORBIDDEN_ADMINS,
//     the deployer and every routine deploy key);
//   - the event history from the deploy block, in 2,000-block chunks (a
//     whole-range eth_getLogs silently returns [] on some RPCs): the admin set
//     replayed from AdminAdded/AdminRemoved equals the arbiters EXACTLY, the
//     accepted-token set replayed from TokenAcceptanceUpdated equals the direct
//     tokens exactly, every AdminRemoved follows an AdminAdded of that
//     address, and the OwnershipTransferred chain of both contracts starts at
//     the zero address, is continuous (each previousOwner is the newOwner
//     before it) and ends at owner(). Finding no deploy events
//     at all is itself a failure (wrong deploy block, or an RPC that drops logs).
//
// Addresses come from DIRECT_ESCROW_ADDRESS / DIRECT_FEE_MANAGER_ADDRESS /
// DIRECT_DEPLOY_BLOCK, else the deployments/<network>.json ledger written by
// the deploy; the deployer (for the CREATE2 check) from DET_DEPLOYER, else the
// ledger, and DET_NAME defaults to the ledger's name, else direct-p2p:v1. Expected values come from the deploy's env vars:
// ARBITER_ADDRESSES, DIRECT_FORBIDDEN_ADMINS, DIRECT_RATES_JSON and the DET_*
// roles (defaults in deploy/chains.js DETERMINISTIC_ROLES).
//
//   npx hardhat run scripts/verify-direct-escrow.js --network bscMainnet
//
// (scripts/verify-deployed-escrow.js is the ProductEscrow print-out.)
const { getAddress, isAddress, ZeroAddress } = require("ethers");
const { resolveChain, readLedger } = require("../deploy/lib");
const { DETERMINISTIC_ROLES } = require("../deploy/chains");
const { directMismatches, planFrom, foreignFeeManagers, DIRECT_NAME, LEDGER_FM, LEDGER_ESC } = require("../deploy/p2p-direct");
const det = require("../deploy/deterministic-escrow");

const LOG_CHUNK = 2000;
const IMPL_SLOT = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc";

// scanLogs walks [fromBlock, toBlock] in `chunk`-block windows and returns
// every log matching `topics` from `address`, in chain order.
async function scanLogs(provider, { address, topics, fromBlock, toBlock, chunk = LOG_CHUNK }) {
  const logs = [];
  for (let from = fromBlock; from <= toBlock; from += chunk) {
    const to = Math.min(from + chunk - 1, toBlock);
    logs.push(...(await provider.getLogs({ address, topics, fromBlock: from, toBlock: to })));
  }
  return logs.sort((a, b) => a.blockNumber - b.blockNumber || a.index - b.index);
}

async function implementationOf(provider, proxy) {
  const raw = await provider.getStorage(proxy, IMPL_SLOT);
  return getAddress("0x" + raw.slice(-40));
}

const sameSet = (a, b) => a.size === b.size && [...a].every((x) => b.has(x));

// verifyDirectEscrow returns { mismatches, report }. expected = { plan
// (deploy/p2p-direct.js planFrom), owner (of the escrow), fmOwner? (default
// owner), afterHandover? (default true; false = the deploy's own pre-handover
// run, deployer-owned: admin checks read the explicit admins() mapping),
// create2? { deployer, name } (without it the CREATE2 check fails),
// foreignFeeManagers?, allTokens, permit2, impls? }.
async function verifyDirectEscrow(hre, { escrow, feeManager, fromBlock, expected, chunk = LOG_CHUNK }) {
  const { ethers } = hre;
  const provider = ethers.provider;
  const esc = await ethers.getContractAt("P2PEscrowV3", escrow);
  const fm = await ethers.getContractAt("FeeManager", feeManager);
  const { plan, owner } = expected;
  const fmWant = expected.fmOwner || owner;
  const afterHandover = expected.afterHandover !== false;
  const mismatches = [];
  const report = [];
  const bad = (m) => mismatches.push(m);
  const same = (a, b) => getAddress(a) === getAddress(b);

  // Where it is: the predicted CREATE2 address, and both are the v5 ERC1967Proxy.
  const impls = { ...(expected.impls || {}) };
  if (expected.create2 && expected.create2.deployer) {
    const p = await det.plan(hre, { deployer: expected.create2.deployer, admins: plan.arbiters, name: expected.create2.name });
    report.push(`predicted escrow ${p.proxy} [${p.proxySaltLabel}] impl ${p.impl} (deployer ${getAddress(expected.create2.deployer)})`);
    if (!same(p.proxy, escrow)) bad(`escrow ${getAddress(escrow)} is not the predicted CREATE2 address ${p.proxy} for deployer ${getAddress(expected.create2.deployer)}, the arbiters and ${p.proxySaltLabel}`);
    if (!impls.escrow) impls.escrow = p.impl;
  } else {
    bad("cannot check the escrow is the predicted CREATE2 address — set DET_DEPLOYER (the deploy key's address)");
  }
  for (const [name, proxy] of [["escrow", escrow], ["FeeManager", feeManager]]) {
    if ((await provider.getCode(proxy)) !== det.ERC1967Proxy.deployedBytecode) bad(`${name} ${getAddress(proxy)} is not an OpenZeppelin v5 ERC1967Proxy`);
  }
  for (const f of expected.foreignFeeManagers || []) {
    if (same(f, feeManager)) bad(`FeeManager ${getAddress(feeManager)} is the main escrow's FeeManager — the direct escrow needs its own`);
  }

  // Ownership and implementations.
  const escOwner = await esc.owner(), fmOwner = await fm.owner();
  report.push(`owner(): escrow=${escOwner} FeeManager=${fmOwner}`);
  if (!same(escOwner, owner)) bad(`escrow.owner() is ${escOwner}, want ${owner}`);
  if (!same(fmOwner, fmWant)) bad(`FeeManager.owner() is ${fmOwner}, want ${fmWant}`);
  if (afterHandover && plan.forbidden.some((f) => same(f, owner))) {
    bad(`the owner ${getAddress(owner)} is a forbidden / routine deploy key — it can never own the direct escrow`);
  }
  for (const [name, proxy] of [["escrow", escrow], ["FeeManager", feeManager]]) {
    const impl = await implementationOf(provider, proxy);
    report.push(`${name} implementation=${impl}`);
    if (impl === ZeroAddress) bad(`${name} implementation slot is empty`);
    else if ((await provider.getCode(impl)) === "0x") bad(`${name} implementation ${impl} has no code`);
    const want = impls[name];
    if (want && getAddress(want) !== impl) bad(`${name} implementation is ${impl}, expected ${want}`);
  }

  // Every wired value, as the deploy asserted it.
  for (const m of await directMismatches(hre, {
    fm, esc, plan, allTokens: expected.allTokens, permit2: expected.permit2, afterHandover,
  })) bad(m);
  report.push(`feeManager=${await esc.feeManager()} feeBps=${await esc.feeBps()} paymentWindow=${await esc.paymentWindow()}`);
  report.push(`systemWallet=${await esc.systemWallet()} trustedForwarder=${await esc.trustedForwarder()} paused=${await esc.paused()}`);
  for (const [sym, addr] of Object.entries(plan.tokens)) {
    const key = await esc.tokenSymbol(addr);
    const rows = [];
    for (const f of ["p2p_platform", "p2p"]) {
      const [bps, flat, exempt, active] = await fm.rateFor(f, key);
      rows.push(`${f}=(${bps},${flat},${exempt},${active})`);
    }
    report.push(`${sym} ${addr} accepted=${await esc.acceptedTokens(addr)} symbol="${key}" ${rows.join(" ")}`);
  }

  // Event history, chunked from the deploy block.
  const toBlock = await provider.getBlockNumber();
  const ev = (c, name) => c.interface.getEvent(name).topicHash;
  const escLogs = await scanLogs(provider, {
    address: escrow, fromBlock, toBlock, chunk,
    topics: [[ev(esc, "AdminAdded"), ev(esc, "AdminRemoved"), ev(esc, "OwnershipTransferred"), ev(esc, "TokenAcceptanceUpdated")]],
  });
  const fmLogs = await scanLogs(provider, {
    address: feeManager, fromBlock, toBlock, chunk, topics: [[ev(fm, "OwnershipTransferred")]],
  });
  report.push(`scanned blocks ${fromBlock}..${toBlock} in ${chunk}-block chunks: escrow ${escLogs.length} logs, FeeManager ${fmLogs.length} logs`);

  const admins = new Set();
  const accepted = new Set();
  const ownerChain = { escrow: [], FeeManager: [] };
  for (const log of escLogs) {
    const p = esc.interface.parseLog(log);
    if (p.name === "AdminAdded") admins.add(getAddress(p.args.account));
    else if (p.name === "AdminRemoved") {
      const a = getAddress(p.args.account);
      if (!admins.has(a)) bad(`escrow: AdminRemoved(${a}) at block ${log.blockNumber} with no AdminAdded before it`);
      admins.delete(a);
    }
    else if (p.name === "TokenAcceptanceUpdated") {
      if (p.args.accepted) accepted.add(getAddress(p.args.token)); else accepted.delete(getAddress(p.args.token));
    } else ownerChain.escrow.push([p.args.previousOwner, p.args.newOwner]);
  }
  for (const log of fmLogs) {
    const p = fm.interface.parseLog(log);
    ownerChain.FeeManager.push([p.args.previousOwner, p.args.newOwner]);
  }

  // Sanity: the deploy itself must be in range, or every set below is empty
  // for the wrong reason.
  for (const [name, chain] of Object.entries(ownerChain)) {
    if (chain.length === 0 || chain[0][0] !== ZeroAddress) {
      bad(`${name}: no deploy-time OwnershipTransferred(0x0 → …) found from block ${fromBlock} — wrong deploy block, or the RPC returned empty logs`);
      continue;
    }
    for (let i = 1; i < chain.length; i++) {
      if (!same(chain[i][0], chain[i - 1][1])) {
        bad(`${name}: OwnershipTransferred history breaks — transfer ${i} is from ${chain[i][0]}, but the owner before it was ${chain[i - 1][1]}`);
      }
    }
    const last = chain[chain.length - 1][1];
    const current = name === "escrow" ? escOwner : fmOwner;
    if (getAddress(last) !== getAddress(current)) bad(`${name}: OwnershipTransferred history ends at ${last} but owner() is ${current}`);
    report.push(`${name} ownership: ${chain.map(([, n]) => n).join(" → ")}`);
  }
  if (!escLogs.some((l) => l.topics[0] === ev(esc, "AdminAdded"))) {
    bad(`escrow: no AdminAdded event found from block ${fromBlock} — wrong deploy block, or the RPC returned empty logs`);
  }

  const wantAdmins = new Set(plan.arbiters.map(getAddress));
  report.push(`admin set from events: ${[...admins].join(",") || "(none)"}`);
  if (!sameSet(admins, wantAdmins)) {
    const extra = [...admins].filter((a) => !wantAdmins.has(a));
    const missing = [...wantAdmins].filter((a) => !admins.has(a));
    bad(`admin set from events is not exactly the arbiters (extra: ${extra.join(",") || "none"}; missing: ${missing.join(",") || "none"})`);
  }
  const wantTokens = new Set(Object.values(plan.tokens).map(getAddress));
  if (!sameSet(accepted, wantTokens)) {
    bad(`accepted tokens from events are ${[...accepted].join(",") || "(none)"}, want exactly ${[...wantTokens].join(",")}`);
  }
  return { mismatches, report };
}

// expectedFromChain builds the expected values from a chains.js entry and the
// deterministic roles; extraForbidden (the deployer, routine deploy keys) must
// never be admin or owner. `create2` { deployer, name } and `ledger` (for the
// main FeeManager address) are optional.
function expectedFromChain(cfg, roles, extraForbidden = [], { create2, ledger } = {}) {
  if (!cfg.direct) throw new Error("verify-direct: this network has no direct block in deploy/chains.js");
  for (const k of ["owner", "operator", "treasury"]) {
    if (!roles[k] || !isAddress(roles[k])) throw new Error(`verify-direct: role ${k} is not a valid address: ${roles[k]}`);
  }
  const plan = planFrom(cfg, roles, extraForbidden);
  return {
    plan, owner: getAddress(roles.owner), allTokens: cfg.tokens, permit2: cfg.permit2,
    create2, foreignFeeManagers: foreignFeeManagers(cfg, ledger),
  };
}

// cliInputs resolves what the CLI verifies from env + ledger (split out so the
// test can check it): addresses, start block, and the expected values with the
// deployer and every routine deploy key forbidden.
function cliInputs(hre, { cfg, ledger, env = process.env, roles = DETERMINISTIC_ROLES }) {
  const ledgerEsc = ledger[LEDGER_ESC], ledgerFm = ledger[LEDGER_FM];
  const escrow = env.DIRECT_ESCROW_ADDRESS || ledgerEsc?.proxy;
  const feeManager = env.DIRECT_FEE_MANAGER_ADDRESS || ledgerFm?.proxy;
  const fromRaw = env.DIRECT_DEPLOY_BLOCK || ledgerEsc?.deployBlock;
  if (!escrow || !isAddress(escrow)) throw new Error("verify-direct: set DIRECT_ESCROW_ADDRESS (or deploy with the ledger)");
  if (!feeManager || !isAddress(feeManager)) throw new Error("verify-direct: set DIRECT_FEE_MANAGER_ADDRESS (or deploy with the ledger)");
  const fromBlock = Number(fromRaw);
  if (!Number.isInteger(fromBlock) || fromBlock < 0) throw new Error("verify-direct: set DIRECT_DEPLOY_BLOCK (the block the deploy started at)");
  const deployer = env.DET_DEPLOYER || ledgerEsc?.deployer;
  if (deployer && !isAddress(deployer)) throw new Error(`verify-direct: DET_DEPLOYER is not a valid address: ${deployer}`);
  const name = env.DET_NAME || ledgerEsc?.name || DIRECT_NAME;
  // Routine deploy keys (.env DEPLOYER_PRIVATE_KEY, hardhat-config accounts —
  // addresses only) are forbidden as admin and as owner, like the deployer.
  const extra = [...(deployer ? [deployer] : []), ...det.routineDeployerAddresses(hre)];
  const expected = expectedFromChain(cfg, roles, extra, { create2: deployer ? { deployer, name } : undefined, ledger });
  const impls = {};
  if (!env.DIRECT_ESCROW_ADDRESS && ledgerEsc?.impl) impls.escrow = ledgerEsc.impl;
  if (!env.DIRECT_FEE_MANAGER_ADDRESS && ledgerFm?.impl) impls.FeeManager = ledgerFm.impl;
  expected.impls = impls;
  return { escrow, feeManager, fromBlock, expected };
}

async function main() {
  const hre = require("hardhat");
  const net = hre.network.name;
  const { escrow, feeManager, fromBlock, expected } = cliInputs(hre, { cfg: resolveChain(hre), ledger: readLedger(net) });
  console.log(`verify-direct: ${net} escrow=${escrow} FeeManager=${feeManager} fromBlock=${fromBlock}`);
  const { mismatches, report } = await verifyDirectEscrow(hre, { escrow, feeManager, fromBlock, expected });
  for (const line of report) console.log(`  ${line}`);
  if (mismatches.length) {
    console.error(`verify-direct: ${mismatches.length} MISMATCH(ES):`);
    for (const m of mismatches) console.error(`  ✗ ${m}`);
    process.exit(1);
  }
  console.log("verify-direct: OK — every value and the admin set match");
}

if (require.main === module) {
  main().catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { verifyDirectEscrow, expectedFromChain, cliInputs, scanLogs, implementationOf, LOG_CHUNK };
