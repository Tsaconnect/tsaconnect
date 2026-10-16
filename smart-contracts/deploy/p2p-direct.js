// smart-contracts/deploy/p2p-direct.js
//
// Direct (user-to-user) P2P escrow on any EVM chain whose deploy/chains.js
// entry has a `direct` block (BSC; plan docs/plans/p2p-bsc C-1). A thin layer
// over deploy/deterministic-escrow.js (same CREATE2 address on every chain):
//
//   - its OWN FeeManager (deploy/feemanager.js, never a shared one), deployed
//     and owned by the same cold deploy key as the escrow (never the routine
//     .env key), live p2p_platform / p2p rates written with setRates before
//     any handover;
//   - the escrow through the deterministic path with admins = the dispute
//     arbiters ONLY (the explicit admin list replaces [operator] in
//     initialize(), so the arbiters are part of the address; the operator,
//     treasury, deployer and routine deploy keys are refused as admins);
//   - systemWallet = the treasury, paymentWindow 1800, no trusted forwarder
//     (Q12: a forwarder can act as any party), USDT + USDC only;
//   - every value read back and asserted, then the full read-back
//     (scripts/verify-direct-escrow.js: CREATE2 address, proxy code, event
//     scan proving the admin set is exactly the arbiters, ownership chain)
//     run while the deployer still owns both — any mismatch refuses the
//     handover — then ownership of BOTH contracts handed to the PM LAST and
//     the read-back run again. One-step Ownable: the owner address is final.
//   - DIRECT_OWNER_IS_DEPLOYER=1 (owner decision 2026-10-16): the cold
//     deployer keeps both contracts — no handover — until the owner moves
//     them with transferOwnership. DET_OWNER_CONFIRM must then be the
//     deployer. isAdmin(owner) is always true on P2PEscrowV3, so the deployer
//     is also an arbiter meanwhile. The deployer itself must not be a
//     forbidden admin, a routine deploy key or the routine signer; every other
//     hot key stays refused. A re-run of a finished owner-is-deployer deploy
//     only reads back (DIRECT_FEE_MANAGER_ADDRESS + DIRECT_DEPLOY_BLOCK or the
//     ledger) and sends nothing.
//   - A deployer-owned escrow that is already wired (feeManager() set) is
//     never resumed: that would deploy a fresh FeeManager and re-point the
//     live escrow. The run refuses with nothing sent; hand it over with
//     transferOwnership. Only a proxy left unwired resumes.
//
// Refuses before anything is sent: an empty arbiter list or one naming a
// forbidden address (DIRECT_FORBIDDEN_ADMINS, the canonical operator /
// treasury), an owner that is any hot or deploy key
// (forbidden admin, routine deploy key, the routine signer, the cold
// deployer), a non-direct-p2p:* DET_NAME, a configured forwarder, missing /
// out-of-range rates, plus every deterministic-escrow refusal (duplicate or
// hot-key admin, deploy key, manifest, DET_OWNER_CONFIRM).
//
//   read -r -s -p "deploy key: " K; echo
//   DET_DEPLOYER_PRIVATE_KEY="$K" DET_OWNER_CONFIRM=0x62bA… \
//   ARBITER_ADDRESSES=0x…,0x… \
//   [DIRECT_OWNER_IS_DEPLOYER=1 with DET_OWNER_CONFIRM=<the deployer>] \
//   DIRECT_RATES_JSON='{"p2p_platform":{"bps":100},"p2p":{"bps":50}}' \
//     npx hardhat run deploy/p2p-direct.js --network bscTestnet; unset K
const { getAddress, isAddress, ZeroAddress } = require("ethers");
const { resolveChain, readLedger, writeLedger, assertOwnerConfirm, handOverOwnership } = require("./lib");
const { CANONICAL_HOT_KEYS, DETERMINISTIC_ROLES } = require("./chains");
const feemanager = require("./feemanager");
const det = require("./deterministic-escrow");
const create2 = require("./create2");

const TAG = "p2p-direct";
const FEATURES = ["p2p_platform", "p2p"];
// P2PEscrowV3.initialize sets feeBps = 50 and this module leaves it alone; it
// only applies if a FeeManager row is ever switched inactive (legacy fallback).
const INIT_FEE_BPS = 50n;
// P2PEscrowV3 caps the snapshot per leg (MAX_PLATFORM_FEE_BPS / MAX_GAS_FEE_BPS)
// and in total (MAX_FEE_BPS). A rate above these would make every open revert.
const MAX_LEG_BPS = 300;
const MAX_TOTAL_BPS = 500;
const LEDGER_FM = "DirectFeeManager";
const LEDGER_ESC = "DirectP2PEscrowV3";
// The direct escrow's proxy salt; deploy/deterministic-escrow.js refuses every
// direct-p2p:* name without an admin list.
const DIRECT_NAME = "direct-p2p:v1";
const MAX_UINT64 = (1n << 64n) - 1n; // FeeManager.Rate.flatUSD

const fail = (m) => { throw new Error(`${TAG}: ${m}`); };
const same = (a, b) => getAddress(a) === getAddress(b);

// parseRates accepts DIRECT_RATES_JSON (string) or an already-parsed object of
// the shape { p2p_platform: {bps, flatUSD?, mcgpExempt?}, p2p: {...} } and
// returns it normalised. Every row is written active: a rate row that is not
// active falls back to the legacy feeBps on-chain, which is not "the live rate".
function parseRates(raw) {
  if (raw == null || raw === "") fail("DIRECT_RATES_JSON is required (the live p2p_platform / p2p rates)");
  let obj = raw;
  if (typeof raw === "string") {
    try { obj = JSON.parse(raw); } catch (e) { fail(`DIRECT_RATES_JSON is not valid JSON (${e.message})`); }
  }
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) fail("DIRECT_RATES_JSON must be an object keyed by feature");
  const extra = Object.keys(obj).filter((k) => !FEATURES.includes(k));
  if (extra.length) fail(`DIRECT_RATES_JSON has unknown feature(s): ${extra.join(", ")}`);
  const out = {};
  for (const f of FEATURES) {
    const r = obj[f];
    if (!r || typeof r !== "object") fail(`DIRECT_RATES_JSON is missing the ${f} rate`);
    const unknown = Object.keys(r).filter((k) => !["bps", "flatUSD", "mcgpExempt", "active"].includes(k));
    if (unknown.length) fail(`DIRECT_RATES_JSON ${f} has unknown field(s): ${unknown.join(", ")}`);
    if (!Number.isInteger(r.bps) || r.bps < 0 || r.bps > MAX_LEG_BPS) fail(`${f}.bps must be an integer in 0..${MAX_LEG_BPS}`);
    const flatUSD = r.flatUSD === undefined ? 0 : r.flatUSD;
    // Safe integers only: JSON numbers past 2^53 are already rounded, and the
    // FeeManager slot is uint64 — an overflow would revert setRates mid-run.
    if (!Number.isSafeInteger(flatUSD) || flatUSD < 0 || BigInt(flatUSD) > MAX_UINT64) {
      fail(`${f}.flatUSD must be a non-negative safe integer that fits uint64 (1e6 micro-USD)`);
    }
    if (r.mcgpExempt !== undefined && typeof r.mcgpExempt !== "boolean") fail(`${f}.mcgpExempt must be a boolean`);
    if (r.active !== undefined && r.active !== true) fail(`${f}.active must be true (an inactive row falls back to the legacy feeBps)`);
    out[f] = { bps: r.bps, flatUSD, mcgpExempt: r.mcgpExempt === true, active: true };
  }
  if (out.p2p_platform.bps + out.p2p.bps > MAX_TOTAL_BPS) fail(`p2p_platform.bps + p2p.bps exceeds ${MAX_TOTAL_BPS}`);
  return out;
}

const uniqueAddrs = (list) => [...new Set(list.filter(Boolean).map(getAddress))];

// forbiddenLabels: every always-forbidden admin besides the DET_OPERATOR /
// DET_TREASURY roles (deterministic-escrow.js names those itself), labelled for
// the refusal message. The canonical hot keys are fixed, not env-derived.
function forbiddenLabels(cfg) {
  const out = (cfg.direct.forbiddenAdmins || []).map((a) => [a, "in DIRECT_FORBIDDEN_ADMINS"]);
  out.push([CANONICAL_HOT_KEYS.operator, "the canonical operator (hot key)"]);
  out.push([CANONICAL_HOT_KEYS.treasury, "the canonical treasury (hot key)"]);
  return out;
}

// foreignFeeManagers: FeeManagers the direct escrow must never use — from
// the ledger, the main escrow's (contract-wide rates are not shared).
function foreignFeeManagers(_cfg, ledger = {}) {
  return uniqueAddrs([ledger.FeeManager && ledger.FeeManager.proxy]);
}

// planFrom builds the expected end state from a chains.js entry and the
// deterministic roles; shared by the deploy and scripts/verify-direct-escrow.js.
// `forbidden` = DIRECT_FORBIDDEN_ADMINS + operator + treasury (the roles AND the
// canonical hot keys) + `extraForbidden` (deployers,
// routine deploy keys): none of them may ever be an admin.
function planFrom(cfg, roles, extraForbidden = []) {
  const direct = cfg.direct;
  if (!direct) fail("this network has no direct block in deploy/chains.js");
  const arbiters = direct.arbiters || [];
  if (arbiters.length === 0) fail("ARBITER_ADDRESSES is empty — the direct escrow needs at least one dispute arbiter");
  const labelled = forbiddenLabels(cfg);
  for (const a of [...arbiters, ...labelled.map(([f]) => f)]) {
    if (!isAddress(a)) fail(`not a valid address: ${a}`);
  }
  return {
    arbiters: arbiters.map(getAddress),
    forbidden: uniqueAddrs([...labelled.map(([f]) => f), roles.operator, roles.treasury, ...extraForbidden]),
    rates: parseRates(direct.rates),
    tokens: det.pickTokens(cfg.tokens, direct.tokens),
    tokenSymbols: direct.tokens,
    paymentWindowSec: direct.paymentWindowSec,
    systemWallet: getAddress(roles.treasury),
  };
}

// checkConfig is every direct-specific refusal; the deterministic module adds
// its own (duplicate / hot-key admins, deploy key, owner confirm) after it.
function checkConfig(cfg, roles) {
  if (cfg.forwarder) fail("chains.js sets a forwarder — the direct escrow keeps trustedForwarder at zero (Q12)");
  const plan = planFrom(cfg, roles);
  for (const a of plan.arbiters) {
    for (const [f, label] of forbiddenLabels(cfg)) if (same(f, a)) fail(`arbiter ${a} is ${label}`);
  }
  // isAdmin(owner) is always true on P2PEscrowV3, so a forbidden owner is a forbidden admin.
  if (plan.forbidden.some((f) => same(f, roles.owner))) {
    fail(`the owner ${roles.owner} is a forbidden admin — isAdmin(owner) is always true on P2PEscrowV3`);
  }
  return plan;
}

// directMismatches reads every wired value back and returns one line per
// mismatch (empty = all good). Before the handover the deployer still owns the
// escrow and isAdmin(deployer) is true, so admin checks read the explicit
// `admins` mapping; afterHandover switches them to isAdmin(), which also
// counts the owner. Shared with scripts/verify-direct-escrow.js.
async function directMismatches(hre, { fm, esc, plan, allTokens, permit2, afterHandover = false }) {
  const { ethers } = hre;
  const out = [];
  const bad = (m) => out.push(m);
  const { arbiters, forbidden, rates, tokens } = plan;
  const adminOf = (a) => (afterHandover ? esc.isAdmin(a) : esc.admins(a));
  if (!same(await esc.feeManager(), await fm.getAddress())) bad("escrow.feeManager() is not the direct FeeManager");
  if ((await esc.feeBps()) !== INIT_FEE_BPS) bad(`escrow.feeBps() is not ${INIT_FEE_BPS}`);
  if ((await esc.paymentWindow()) !== BigInt(plan.paymentWindowSec)) bad(`paymentWindow() is not ${plan.paymentWindowSec}`);
  if ((await esc.trustedForwarder()) !== ZeroAddress) bad("trustedForwarder() is not zero");
  if (!same(await esc.permit2(), permit2 || ZeroAddress)) bad(`permit2() is not ${permit2 || ZeroAddress}`);
  if ((await esc.mcgpToken()) !== ZeroAddress) bad("mcgpToken() is set");
  if (await esc.paused()) bad("escrow is paused");
  if (!same(await esc.systemWallet(), plan.systemWallet)) bad(`systemWallet is not the treasury ${plan.systemWallet}`);
  for (const a of arbiters) {
    if (!(await adminOf(a))) bad(`arbiter ${a} is not an escrow admin`);
  }
  for (const f of forbidden) {
    if (await adminOf(f)) bad(`forbidden address ${f} is an escrow admin`);
  }
  for (const [sym, addr] of Object.entries(tokens)) {
    if (!(await esc.acceptedTokens(addr))) bad(`${sym} is not accepted`);
    const key = await esc.tokenSymbol(addr);
    if (key !== sym) bad(`tokenSymbol(${sym}) is "${key}"`);
    for (const f of FEATURES) {
      const [bps, flat, exempt, active] = await fm.rateFor(f, key);
      const want = rates[f];
      if (bps !== BigInt(want.bps) || flat !== BigInt(want.flatUSD) || exempt !== want.mcgpExempt || active !== true) {
        bad(`${f}/${key} is (${bps}, ${flat}, ${exempt}, ${active}), want (${want.bps}, ${want.flatUSD}, ${want.mcgpExempt}, true)`);
      }
    }
    const erc = await ethers.getContractAt("MockERC20", addr); // any ERC-20 ABI with decimals()
    if (Number(await erc.decimals()) < 6) bad(`${sym} has fewer than 6 decimals`);
  }
  for (const [sym, addr] of Object.entries(allTokens)) {
    if (!addr || tokens[sym]) continue;
    if (await esc.acceptedTokens(addr)) bad(`non-direct token ${sym} is accepted`);
  }
  return out;
}

async function assertDirect(hre, args) {
  const m = await directMismatches(hre, args);
  if (m.length) fail(`assertion failed — ${m.join("; ")}`);
}

// envOptions: the deterministic DET_* options (deploy key, DET_OWNER_CONFIRM,
// CHECK_ONLY, …) with DET_NAME defaulting to the direct salt, plus
// DIRECT_DEPLOY_BLOCK / DIRECT_FEE_MANAGER_ADDRESS for a resumed run or a
// re-run against an escrow already handed over.
function envOptions(env = process.env) {
  const o = det.envOptions(env);
  return {
    name: env.DET_NAME || DIRECT_NAME, ownerConfirm: o.ownerConfirm, checkOnly: o.checkOnly, deployer: o.deployer, deployerKey: o.deployerKey,
    deployBlock: env.DIRECT_DEPLOY_BLOCK ? Number(env.DIRECT_DEPLOY_BLOCK) : undefined,
    feeManager: env.DIRECT_FEE_MANAGER_ADDRESS || undefined,
    ownerIsDeployer: env.DIRECT_OWNER_IS_DEPLOYER === "1",
  };
}

// hotKeys lists every address that must never own the direct escrow, each
// with the reason: the cold deployer, every routine deploy key (.env
// DEPLOYER_PRIVATE_KEY, hardhat-config accounts, `routineAddresses`), the
// routine hardhat signer, and every forbidden admin (isAdmin(owner) is always
// true on P2PEscrowV3). Read fresh on every call.
async function hotKeys(hre, { plan, deployer, routineAddresses = [], ownerIsDeployer = false }) {
  const [routineSigner] = await hre.ethers.getSigners();
  return [
    // Owner-is-deployer drops ONLY this entry: the deployer is still checked
    // against every routine key and forbidden admin below.
    ...(ownerIsDeployer ? [] : [[deployer, "the deployer (the cold deploy key)"]]),
    ...det.routineDeployerAddresses(hre).map((a) => [a, "a routine deploy key (.env DEPLOYER_PRIVATE_KEY / hardhat-config account)"]),
    ...routineAddresses.map((a) => [a, "a routine deploy key"]),
    [routineSigner && routineSigner.address, "the routine hardhat signer"],
    ...plan.forbidden.map((a) => [a, "a forbidden admin (isAdmin(owner) is always true on P2PEscrowV3)"]),
  ].filter(([a]) => a);
}

// ownerDeployer is the deployer address an owner-is-deployer run will own the
// contracts with, resolved BEFORE the check-only pass so a re-run of a finished
// deploy takes the read-back-only path. The deploy key is checked against the
// routine keys exactly as deterministic-escrow.js does.
function ownerDeployer(hre, opts, routineAddresses) {
  const fromKey = opts.deployerKey ? det.deployerSigner(hre, opts.deployerKey, routineAddresses).address : null;
  if (fromKey && opts.deployer && !same(fromKey, opts.deployer)) fail(`DET_DEPLOYER ${opts.deployer} is not the deploy key's address ${fromKey}`);
  const addr = fromKey || opts.deployer;
  if (!addr || !isAddress(addr)) fail("DIRECT_OWNER_IS_DEPLOYER needs the deploy key (DET_DEPLOYER_PRIVATE_KEY) or DET_DEPLOYER");
  return getAddress(addr);
}

function assertOwnerNotHot(owner, hot) {
  for (const [addr, label] of hot) {
    if (same(addr, owner)) fail(`DET_OWNER (the PM) is ${label}: ${getAddress(owner)} — a hot or deploy key must never own the direct escrow`);
  }
}

// readBack runs scripts/verify-direct-escrow.js and prints its report.
async function readBack(hre, args) {
  const { verifyDirectEscrow } = require("../scripts/verify-direct-escrow"); // lazy: it requires this module
  const out = await verifyDirectEscrow(hre, args);
  for (const line of out.report) console.log(`${TAG}: read-back ${line}`);
  return out.mismatches;
}

// deploy options: chain (test path: a chains.js-shaped config, no ledger),
// roles, name (a direct-p2p:* salt, default direct-p2p:v1), ownerConfirm,
// deployerKey, deployer, checkOnly, routineAddresses, networkClass,
// manifestPath (all as deploy/deterministic-escrow.js), deployBlock (first
// block of the escrow's history on a resumed run or an already-handed-over
// re-run), feeManager (the direct FeeManager, for an already-handed-over
// re-run without a ledger), afterWire (test seam: runs after the wiring,
// before the asserts), beforeHandover (test seam: runs after the pre-handover
// read-back, before the owner re-check and the handover), ownerIsDeployer
// (the deployer keeps both contracts; see the header).
async function deploy(hre, opts = {}) {
  const { ethers, network } = hre;
  const { chain, afterWire, beforeHandover } = opts;
  const cfg = chain || resolveChain(hre);
  const roles = { ...DETERMINISTIC_ROLES, ...(opts.roles || {}) };
  const ownerIsDeployer = !!opts.ownerIsDeployer;
  const routineAddresses = opts.routineAddresses || [];
  // Set first: checkConfig then refuses a deployer that is a forbidden admin.
  if (ownerIsDeployer) roles.owner = ownerDeployer(hre, opts, routineAddresses);
  const plan = checkConfig(cfg, roles);
  const name = opts.name || DIRECT_NAME;
  if (!name.startsWith(det.DIRECT_NAME_PREFIX)) fail(`DET_NAME ${name} is not a ${det.DIRECT_NAME_PREFIX}* salt — the direct escrow never shares a salt with the operator-admin escrow`);
  const writeLedgers = !chain && !create2.isLocalNetwork(hre);
  const ledger = writeLedgers ? readLedger(network.name) : {};
  const foreignFMs = foreignFeeManagers(cfg, ledger);
  const detOpts = {
    chain: cfg, roles, admins: plan.arbiters, name, ownerConfirm: opts.ownerConfirm,
    tokens: plan.tokenSymbols, paymentWindowSec: plan.paymentWindowSec,
    deployerKey: opts.deployerKey, deployer: opts.deployer, routineAddresses,
    networkClass: opts.networkClass, manifestPath: opts.manifestPath, ownerIsDeployer,
  };

  // 1. Every refusal, sending nothing: the deterministic check-only pass
  //    (admin list, deploy key, manifest, tokens, window), then the owner
  //    against every hot or deploy key.
  const pre = await det.deploy(hre, { ...detOpts, checkOnly: true });
  if (ownerIsDeployer && !same(roles.owner, pre.deployer)) fail(`the checked deployer ${pre.deployer} is not the owner ${roles.owner}`);
  const hot = await hotKeys(hre, { plan, deployer: pre.deployer, routineAddresses, ownerIsDeployer });
  assertOwnerNotHot(roles.owner, hot);
  // Every deploy / routine key is now a forbidden admin too (the owner is
  // already proven not to be one of them).
  plan.forbidden = uniqueAddrs([...plan.forbidden, ...hot.map(([a]) => a)]);
  const expectedBase = { plan, allTokens: cfg.tokens, permit2: cfg.permit2, create2: { deployer: pre.deployer, name }, foreignFeeManagers: foreignFMs };
  const ledgerEsc = ledger[LEDGER_ESC] && same(ledger[LEDGER_ESC].proxy, pre.proxy) ? ledger[LEDGER_ESC] : null;

  // A chain already handed over: the full read-back, nothing sent.
  if (pre.verified) {
    const fmAddr = opts.feeManager || (ledgerEsc && ledger[LEDGER_FM] ? ledger[LEDGER_FM].proxy : undefined);
    const fromBlock = opts.deployBlock !== undefined ? opts.deployBlock : ledgerEsc ? ledgerEsc.deployBlock : undefined;
    if (!fmAddr || !isAddress(fmAddr) || !Number.isInteger(fromBlock)) {
      fail(`${pre.proxy} is already handed over — set DIRECT_FEE_MANAGER_ADDRESS and DIRECT_DEPLOY_BLOCK (or keep the ledger) so it can be read back`);
    }
    const impls = ledgerEsc ? { escrow: ledgerEsc.impl, FeeManager: ledger[LEDGER_FM] && ledger[LEDGER_FM].impl } : {};
    const mismatches = await readBack(hre, {
      escrow: pre.proxy, feeManager: fmAddr, fromBlock, expected: { ...expectedBase, owner: roles.owner, impls },
    });
    if (mismatches.length) fail(`${pre.proxy} is already handed over but its read-back FAILED: ${mismatches.join("; ")}`);
    console.log(`${TAG}: ${pre.proxy} is already handed over and reads back clean`);
    return { escrow: pre.proxy, feeManager: getAddress(fmAddr), owner: roles.owner, reused: true };
  }
  if (opts.checkOnly) return { escrow: pre.proxy, checkOnly: true, exists: pre.exists };
  // A deployer-owned escrow that is already wired is a finished (or
  // handed-over-FeeManager-only) run: resuming would deploy a fresh FeeManager
  // and re-point the live escrow at it. Only a proxy still unwired resumes.
  if (pre.exists.proxy) {
    const existing = await ethers.getContractAt("P2PEscrowV3", pre.proxy);
    if ((await existing.feeManager()) !== ZeroAddress) {
      fail(`${pre.proxy} is already deployed and wired by ${pre.deployer}; hand it over with transferOwnership, or verify it with scripts/verify-direct-escrow.js — nothing sent`);
    }
  }
  if (!opts.deployerKey) fail("a deploy needs DET_DEPLOYER_PRIVATE_KEY (the cold deploy key, typed in for this run)");
  assertOwnerConfirm({
    label: TAG, owner: roles.owner, confirm: opts.ownerConfirm, deployerAddr: pre.deployer,
    ownerVar: "DET_OWNER (the PM)", confirmVar: "DET_OWNER_CONFIRM", allowDeployerOwner: ownerIsDeployer,
  });
  // The cold deploy key deploys and owns the FeeManager too: no routine key
  // ever owns a direct contract, even before the handover.
  const cold = det.deployerSigner(hre, opts.deployerKey, routineAddresses);
  if (!same(cold.address, pre.deployer)) fail(`the deploy key ${cold.address} is not the checked deployer ${pre.deployer}`);

  // The read-back's event scan starts at the escrow's first block: this run's,
  // unless the proxy already exists from an earlier, interrupted run.
  let deployBlock = (await ethers.provider.getBlockNumber()) + 1;
  if (pre.exists.proxy) {
    const from = opts.deployBlock !== undefined ? opts.deployBlock : ledgerEsc ? ledgerEsc.deployBlock : undefined;
    if (!Number.isInteger(from)) fail(`${pre.proxy} already exists from an earlier run — set DIRECT_DEPLOY_BLOCK to the block it was created in`);
    deployBlock = from;
  }

  // 2. The direct FeeManager, cold-deployer-owned, with the live rates. Its
  //    ledger entry is written only once the whole run has succeeded.
  console.log(`${TAG}: deploying on ${network.name} — arbiters=${plan.arbiters.join(",")} owner=${roles.owner} systemWallet=${plan.systemWallet}`);
  const fmOut = await feemanager.deploy(hre, { ...(chain ? { chain } : {}), deferOwnership: true, signer: cold, writeLedger: false });
  const fm = await ethers.getContractAt("FeeManager", fmOut.proxy, cold);
  const deployed = { feeManager: fmOut.proxy, escrow: pre.proxy };
  const impls = { FeeManager: fmOut.impl };
  let escOut;
  try {
    const features = [], keys = [], rows = [];
    for (const sym of Object.keys(plan.tokens)) {
      for (const f of FEATURES) {
        const r = plan.rates[f];
        features.push(f); keys.push(sym); rows.push([r.bps, r.flatUSD, r.mcgpExempt, r.active]);
      }
    }
    await (await fm.setRates(features, keys, rows)).wait();

    // 3. The escrow, through the deterministic path, wired to that FeeManager
    //    and read back there — still deployer-owned.
    escOut = await det.deploy(hre, { ...detOpts, feeManager: fmOut.proxy, deferOwnership: true });
    if (!escOut.deferred) fail(`${escOut.proxy} was handed over by another run in the meantime — check it with scripts/verify-direct-escrow.js`);
    impls.escrow = escOut.impl;
    if (afterWire) await afterWire({ fm, esc: escOut.esc });
    await assertDirect(hre, { fm, esc: escOut.esc, plan, allTokens: cfg.tokens, permit2: cfg.permit2 });

    // 4. The full read-back while the deployer still owns both: CREATE2
    //    address, proxy code, implementations, and the event scan proving the
    //    admin set is exactly the arbiters. Any mismatch refuses the handover.
    const mismatches = await readBack(hre, {
      escrow: escOut.proxy, feeManager: fmOut.proxy, fromBlock: deployBlock,
      expected: { ...expectedBase, owner: pre.deployer, fmOwner: pre.deployer, afterHandover: false, impls },
    });
    if (mismatches.length) fail(`read-back before the handover FAILED: ${mismatches.join("; ")}`);
    if (beforeHandover) await beforeHandover({ fm, esc: escOut.esc, roles });
    // The owner again, against freshly read hot keys, right before the
    // irreversible transfer.
    assertOwnerNotHot(roles.owner, await hotKeys(hre, { plan, deployer: pre.deployer, routineAddresses, ownerIsDeployer }));
  } catch (e) {
    e.deployed = deployed;
    console.error(`${TAG}: NOT handed over — still owned by the deployer ${pre.deployer}. FeeManager=${fmOut.proxy} escrow=${pre.proxy}`);
    throw e;
  }

  // 5. Ownership of both, last (a no-op when the deployer is the owner).
  if (ownerIsDeployer) {
    console.log(`${TAG}: the deployer ${pre.deployer} keeps FeeManager + escrow (DIRECT_OWNER_IS_DEPLOYER) — hand over later with transferOwnership`);
  } else {
    console.log(`${TAG}: transferring FeeManager + escrow to ${roles.owner} (one-step Ownable — this address is final)`);
  }
  await handOverOwnership(TAG, [fm, escOut.esc], roles.owner);
  // Recorded once the contracts are the PM's, so a failed run never leaves a
  // ledger entry pointing at a deployer-owned FeeManager.
  if (writeLedgers) {
    writeLedger(network.name, LEDGER_FM, { proxy: fmOut.proxy, impl: fmOut.impl, deployBlock });
    writeLedger(network.name, LEDGER_ESC, { proxy: escOut.proxy, impl: escOut.impl, name, deployer: pre.deployer, deployBlock });
  }
  // 6. The full read-back again after the handover: owners, isAdmin() — so
  //    the deployers now count as forbidden — and the event scan.
  const mismatches = await readBack(hre, {
    escrow: escOut.proxy, feeManager: fmOut.proxy, fromBlock: deployBlock,
    expected: { ...expectedBase, owner: roles.owner, impls },
  });
  if (mismatches.length) {
    fail(`read-back after the handover FAILED (ownership already moved — fix from the owner): ${mismatches.join("; ")}`);
  }
  console.log(`${TAG}: ready FeeManager=${fmOut.proxy} escrow=${escOut.proxy} owner=${roles.owner} window=${plan.paymentWindowSec}s deployBlock=${deployBlock}`);
  return { feeManager: fmOut.proxy, escrow: escOut.proxy, owner: roles.owner, deployBlock, plan, impls };
}

if (require.main === module) {
  // Message only: a full error object can carry nested request/transaction data.
  deploy(require("hardhat"), envOptions()).catch((e) => { console.error(`${TAG}: ${e && e.message ? e.message : e}`); process.exit(1); });
}

module.exports = {
  deploy, envOptions, assertDirect, directMismatches, checkConfig, planFrom, parseRates, foreignFeeManagers,
  INIT_FEE_BPS, FEATURES, DIRECT_NAME, LEDGER_FM, LEDGER_ESC,
};
