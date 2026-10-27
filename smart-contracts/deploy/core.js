// smart-contracts/deploy/core.js
//
// Deploy the audit-ready core set on one network: FeeConfig, OrderEscrow (where the chain has
// `core.order`) and P2PEscrow. Spec: docs/specs/2026-10-27-contracts-audit-ready-redesign.md.
//
//   CHECK_ONLY=1 npx hardhat run deploy/core.js --network sonicMainnet     # plan only, sends nothing
//   CORE_ADMIN_CONFIRM=0x62bA… npx hardhat run deploy/core.js --network sonicMainnet
//
// How it stays safe:
//   - Every role and the whole fee/limit configuration are set in the proxies' initializers, so the
//     admin (default: the PM) owns each contract from its first block. The deployer only pays gas
//     and never holds a role; the script asserts that after deploying.
//   - Proxies are created with their init calldata in the ERC1967Proxy constructor (no window in
//     which anyone else could initialize them), and the implementations disable initializers.
//   - The admin may not be the deployer or a hot key; resolvers and pausers may not be hot keys.
//     On mainnets the admin must be typed twice (CORE_ADMIN_CONFIRM).
//   - Everything is read back and asserted, then recorded in deployments/<network>.json, then
//     verified on the explorer.
//
// Config lives in deploy/chains.js (`core` block per network).

const hre = require("hardhat");
const { ethers, upgrades } = hre;
const { getAddress, isAddress, ZeroAddress, encodeBytes32String } = require("ethers");
const { resolveChain, validateAddresses, writeLedger, reuseFromLedger } = require("./lib");
const { chains, CANONICAL_HOT_KEYS } = require("./chains");

// A rehearsal against a local fork (anvil / hardhat node on `localhost`) borrows a real chain's
// config: CORE_CHAIN=sonicMainnet npx hardhat run deploy/core.js --network localhost
function chainConfig() {
  if (hre.network.name === "localhost" && process.env.CORE_CHAIN) {
    const cfg = chains[process.env.CORE_CHAIN];
    if (!cfg) fail(`unknown CORE_CHAIN ${process.env.CORE_CHAIN}`);
    validateAddresses(cfg, process.env.CORE_CHAIN);
    return cfg;
  }
  return resolveChain(hre);
}

const ORDER = encodeBytes32String("order");
const P2P_PLATFORM = encodeBytes32String("p2p_platform");
const P2P_GAS = encodeBytes32String("p2p_gas");
const P2P_FQN = "contracts/core/P2PEscrow.sol:P2PEscrow";

const MAINNETS = new Set(["sonicMainnet", "bscMainnet"]);
const LEDGER = { feeConfig: "CoreFeeConfig", order: "CoreOrderEscrow", p2p: "CoreP2PEscrow" };

function fail(msg) {
  throw new Error(`deploy/core: ${msg}`);
}

function checkedAddress(label, value) {
  if (!value || !isAddress(value)) fail(`${label} is not a valid address: ${value}`);
  return getAddress(value);
}

/** Turn the chain's `core` block into FeeConfig's InitialConfig, resolving symbols to addresses. */
function buildInitialConfig(cfg) {
  const core = cfg.core;
  const token = (sym) => {
    const addr = cfg.tokens[sym];
    if (!addr) fail(`token ${sym} has no address on this network`);
    return getAddress(addr);
  };
  const features = [];
  const tokens = [];
  const rates = [];
  const used = new Set();
  for (const [sym, r] of Object.entries(core.order || {})) {
    features.push(ORDER); tokens.push(token(sym)); rates.push({ bps: r.bps, flatFee: BigInt(r.flat), active: true });
    used.add(sym);
  }
  for (const [sym, r] of Object.entries(core.p2p || {})) {
    features.push(P2P_PLATFORM); tokens.push(token(sym)); rates.push({ bps: r.platformBps, flatFee: 0n, active: true });
    features.push(P2P_GAS); tokens.push(token(sym)); rates.push({ bps: r.gasBps, flatFee: 0n, active: true });
    used.add(sym);
  }
  const limitTokens = [];
  const limits = [];
  for (const sym of used) {
    const l = core.limits[sym];
    if (!l) fail(`no limits for ${sym}: every token with a rate needs value caps (D7)`);
    limitTokens.push(token(sym));
    limits.push({ maxPerOrder: BigInt(l.maxPerOrder), maxOwed: BigInt(l.maxOwed) });
  }
  return { features, tokens, rates, limitTokens, limits };
}

function checkRoles(core, deployer) {
  const hot = new Set(Object.values(CANONICAL_HOT_KEYS).map(getAddress));
  const admin = checkedAddress("CORE_ADMIN", core.admin);
  if (admin === getAddress(deployer)) fail("the admin is the deployer: a deploy key must never hold the admin role");
  if (hot.has(admin)) fail(`the admin ${admin} is a hot key`);
  const resolvers = core.resolvers.map((a) => checkedAddress("CORE_RESOLVERS", a));
  const pausers = core.pausers.map((a) => checkedAddress("CORE_PAUSERS", a));
  for (const a of [...resolvers, ...pausers]) {
    if (hot.has(a)) fail(`${a} is a hot key and may not be a resolver or pauser`);
    if (a === getAddress(deployer)) fail(`${a} is the deployer and may not hold a role`);
  }
  if (MAINNETS.has(hre.network.name)) {
    if (!core.adminConfirm || getAddress(core.adminConfirm) !== admin) {
      fail(`set CORE_ADMIN_CONFIRM to the admin (${admin}) to deploy on ${hre.network.name}`);
    }
  }
  return { admin, resolvers, pausers };
}

async function deployProxy(name, fqn, args, constructorArgs) {
  const factory = await ethers.getContractFactory(fqn);
  const proxy = await upgrades.deployProxy(factory, args, { kind: "uups", constructorArgs });
  await proxy.waitForDeployment();
  const address = await proxy.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(address);
  console.log(`  ${name}: proxy ${address}  impl ${impl}`);
  return { contract: proxy, address, impl };
}

async function assertRoles(label, c, roles, admin, deployer) {
  const ADMIN = await c.DEFAULT_ADMIN_ROLE();
  if (getAddress(await c.defaultAdmin()) !== admin) fail(`${label}: defaultAdmin is not ${admin}`);
  for (const r of roles) {
    const role = await c[r]();
    if (!(await c.hasRole(role, admin))) fail(`${label}: admin lacks ${r}`);
    if (await c.hasRole(role, deployer)) fail(`${label}: deployer holds ${r}`);
  }
  if (await c.hasRole(ADMIN, deployer)) fail(`${label}: deployer holds DEFAULT_ADMIN_ROLE`);
}

async function verify(address, label) {
  if (hre.network.name === "hardhat" || hre.network.name === "localhost") return;
  try {
    await hre.run("verify:verify", { address });
    console.log(`  verified ${label}`);
  } catch (e) {
    const msg = String(e.message || e);
    if (/already verified/i.test(msg)) console.log(`  ${label}: already verified`);
    else console.warn(`  ${label}: verification failed (${msg.split("\n")[0]}) — rerun: npx hardhat verify --network ${hre.network.name} ${address}`);
  }
}

async function main() {
  const cfg = chainConfig();
  if (!cfg.core) fail(`no core block for ${hre.network.name} in deploy/chains.js`);
  const core = cfg.core;
  const [signer] = await ethers.getSigners();
  const deployer = getAddress(signer.address);
  const net = await ethers.provider.getNetwork();
  const { admin, resolvers, pausers } = checkRoles(core, deployer);
  const feeRecipient = checkedAddress("CORE_FEE_RECIPIENT", core.feeRecipient);
  const forwarder = core.forwarder ? checkedAddress("CORE_FORWARDER", core.forwarder) : ZeroAddress;
  const initial = buildInitialConfig(cfg);

  console.log(`core set on ${hre.network.name} (chain ${net.chainId}), deployer ${deployer}`);
  console.log(`  admin ${admin}, fee recipient ${feeRecipient}, forwarder ${forwarder}`);
  console.log(`  resolvers [${resolvers.join(", ")}], pausers [${pausers.join(", ")}]`);
  initial.features.forEach((f, i) =>
    console.log(`  rate ${ethers.decodeBytes32String(f)} ${initial.tokens[i]}: ${initial.rates[i].bps} bps + ${initial.rates[i].flatFee} flat`));
  initial.limitTokens.forEach((t, i) =>
    console.log(`  limits ${t}: ${initial.limits[i].maxPerOrder} per order, ${initial.limits[i].maxOwed} owed`));

  if (forwarder !== ZeroAddress && (await ethers.provider.getCode(forwarder)) === "0x") fail(`forwarder ${forwarder} has no code`);
  for (const t of new Set(initial.tokens)) if ((await ethers.provider.getCode(t)) === "0x") fail(`token ${t} has no code`);

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1: nothing sent.");
    return;
  }

  const out = {};
  const prior = reuseFromLedger(hre, LEDGER.feeConfig, null);
  if (prior) fail(`${LEDGER.feeConfig} is already recorded (${prior.proxy}); set FORCE_REDEPLOY=1 to deploy a new set`);

  out.feeConfig = await deployProxy("FeeConfig", "FeeConfig", [admin, feeRecipient, initial], []);
  const fc = out.feeConfig.address;
  if (core.order) {
    out.order = await deployProxy("OrderEscrow", "OrderEscrow", [admin, resolvers, pausers], [forwarder, fc]);
  }
  out.p2p = await deployProxy("P2PEscrow", P2P_FQN, [admin, resolvers, pausers], [forwarder, fc]);

  // Read everything back before recording it.
  await assertRoles("FeeConfig", out.feeConfig.contract, ["CONFIG_ROLE", "UPGRADER_ROLE"], admin, deployer);
  if (getAddress(await out.feeConfig.contract.feeRecipient()) !== feeRecipient) fail("FeeConfig fee recipient mismatch");
  for (let i = 0; i < initial.features.length; i++) {
    const r = await out.feeConfig.contract.rateFor(initial.features[i], initial.tokens[i]);
    if (Number(r.bps) !== initial.rates[i].bps || r.flatFee !== initial.rates[i].flatFee) fail(`FeeConfig rate ${i} mismatch`);
  }
  for (const [key, label] of [["order", "OrderEscrow"], ["p2p", "P2PEscrow"]]) {
    if (!out[key]) continue;
    const c = out[key].contract;
    await assertRoles(label, c, ["UPGRADER_ROLE", "PAUSER_ROLE", "RESCUER_ROLE", "RESOLVER_ROLE"], admin, deployer);
    if (getAddress(await c.feeConfig()) !== fc) fail(`${label}: feeConfig mismatch`);
    if (getAddress(await c.trustedForwarder()) !== forwarder) fail(`${label}: forwarder mismatch`);
    for (const r of resolvers) if (!(await c.hasRole(await c.RESOLVER_ROLE(), r))) fail(`${label}: resolver ${r} missing`);
    for (const p of pausers) if (!(await c.hasRole(await c.PAUSER_ROLE(), p))) fail(`${label}: pauser ${p} missing`);
  }
  console.log("  read-back OK: admin holds every role, the deployer holds none");

  writeLedger(hre.network.name, LEDGER.feeConfig, { proxy: fc, impl: out.feeConfig.impl });
  if (out.order) writeLedger(hre.network.name, LEDGER.order, { proxy: out.order.address, impl: out.order.impl });
  writeLedger(hre.network.name, LEDGER.p2p, { proxy: out.p2p.address, impl: out.p2p.impl });

  await verify(fc, "FeeConfig");
  if (out.order) await verify(out.order.address, "OrderEscrow");
  await verify(out.p2p.address, "P2PEscrow");
  return out;
}

if (require.main === module) {
  main().then(() => process.exit(0)).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { main, buildInitialConfig, checkRoles };
