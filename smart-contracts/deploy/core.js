// smart-contracts/deploy/core.js
//
// Deploy the audit-ready core set on one network: FeeConfig, OrderEscrow (where the chain has
// `core.order`) and P2PTradeEscrow. Spec: docs/specs/2026-10-27-contracts-audit-ready-redesign.md.
//
//   CHECK_ONLY=1 npx hardhat run deploy/core.js --network sonicMainnet     # plan only, sends nothing
//   CORE_ADMIN_CONFIRM=0x62bA… npx hardhat run deploy/core.js --network sonicMainnet
//
// Dev set (CORE_DEV=1): the deployer is the admin, and the default resolver and pauser, so the
// whole flow can be tested end to end without the PM's wallet. It is recorded under CoreDev*
// ledger names, never the production ones, and is thrown away: the production set is a fresh
// deploy with the PM as admin, not a handover of the dev contracts.
//   CORE_DEV=1 CORE_ADMIN_CONFIRM=<deployer> npx hardhat run deploy/core.js --network sonicMainnet
//
// How it stays safe:
//   - Every role and the whole fee/limit configuration are set in the proxies' initializers, so the
//     admin (default: the PM) owns each contract from its first block. Outside a dev set the
//     deployer only pays gas and never holds a role; the script asserts that after deploying.
//   - Proxies are created with their init calldata in the ERC1967Proxy constructor (no window in
//     which anyone else could initialize them), and the implementations disable initializers.
//   - The admin may not be a hot key, nor the deployer outside a dev set; resolvers and pausers may
//     not be hot keys. On mainnets the admin must be typed twice (CORE_ADMIN_CONFIRM).
//   - Each proxy is recorded in deployments/<network>.json as soon as it exists; everything is
//     then read back and asserted, marked `readBack`, and verified on the explorer.
//
// Config lives in deploy/chains.js (`core` block per network).

const hre = require("hardhat");
const { ethers, upgrades } = hre;
const { getAddress, isAddress, ZeroAddress, encodeBytes32String } = require("ethers");
const { resolveChain, validateAddresses, writeLedger, reuseFromLedger, assertOwnerConfirm } = require("./lib");
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
const P2P_FQN = "contracts/core/P2PTradeEscrow.sol:P2PTradeEscrow";

const MAINNETS = new Set(["sonicMainnet", "bscMainnet"]);

/** Ledger names: a dev set never shares a name with the production one. */
function ledgerNames(dev) {
  const p = dev ? "CoreDev" : "Core";
  return { feeConfig: `${p}FeeConfig`, order: `${p}OrderEscrow`, p2p: `${p}P2PTradeEscrow` };
}

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

/**
 * Resolve and check every role holder before anything is sent. Production: the deployer holds no
 * role. Dev (`dev`): the deployer is the admin, and the resolver and pauser unless others are
 * named. Both refuse hot keys, and on mainnets the admin must be typed twice (CORE_ADMIN_CONFIRM).
 */
function checkRoles(core, deployer, { dev = false } = {}) {
  deployer = getAddress(deployer);
  const hot = new Set([...Object.values(CANONICAL_HOT_KEYS), ...core.forbidden].map(getAddress));
  const admin = dev ? deployer : checkedAddress("CORE_ADMIN", core.admin);
  if (hot.has(admin)) fail(`the admin ${admin} is a hot key`);
  const named = (list, label) => list.map((a) => checkedAddress(label, a));
  const resolvers = dev && core.resolvers.length === 0 ? [deployer] : named(core.resolvers, "CORE_RESOLVERS");
  const pausers = dev && core.pausers.length === 0 ? [deployer] : named(core.pausers, "CORE_PAUSERS");
  for (const a of [...resolvers, ...pausers]) {
    if (hot.has(a)) fail(`${a} is a hot key and may not be a resolver or pauser`);
    if (!dev && a === deployer) fail(`${a} is the deployer and may not hold a role`);
  }
  const recipient = checkedAddress("CORE_FEE_RECIPIENT", core.feeRecipient);
  if (recipient === deployer || hot.has(recipient)) fail(`the fee recipient ${recipient} is the deployer or a forbidden key`);
  const confirm = { label: "deploy/core", owner: admin, confirm: core.adminConfirm, deployerAddr: deployer, ownerVar: "the admin", confirmVar: "CORE_ADMIN_CONFIRM", allowDeployerOwner: dev };
  if (MAINNETS.has(hre.network.name)) assertOwnerConfirm(confirm);
  else if (!dev && admin === deployer) fail("the admin is the deployer: a deploy key must never hold the admin role");
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

/** The admin holds every role; unless the deployer is the admin (dev), the deployer holds none. */
async function assertRoles(label, c, roles, admin, deployer) {
  const ADMIN = await c.DEFAULT_ADMIN_ROLE();
  if (getAddress(await c.defaultAdmin()) !== admin) fail(`${label}: defaultAdmin is not ${admin}`);
  const deployerMayHold = admin === deployer;
  for (const r of roles) {
    const role = await c[r]();
    if (!(await c.hasRole(role, admin))) fail(`${label}: admin lacks ${r}`);
    if (!deployerMayHold && (await c.hasRole(role, deployer))) fail(`${label}: deployer holds ${r}`);
  }
  if (!deployerMayHold && (await c.hasRole(ADMIN, deployer))) fail(`${label}: deployer holds DEFAULT_ADMIN_ROLE`);
}

/**
 * Verify the implementation with its constructor arguments first (the upgrades plugin's proxy
 * verify passes none, so an implementation with immutables fails there), then the proxy.
 */
async function verify(d, label, constructorArguments = []) {
  if (hre.network.name === "hardhat" || hre.network.name === "localhost") return;
  const attempt = async (address, args, what) => {
    try {
      await hre.run("verify:verify", { address, constructorArguments: args });
      console.log(`  verified ${label} ${what}`);
    } catch (e) {
      const msg = String(e.message || e);
      if (/already verified/i.test(msg)) console.log(`  ${label} ${what}: already verified`);
      else console.warn(`  ${label} ${what}: verification failed (${msg.split("\n")[0]}) — rerun: npx hardhat verify --network ${hre.network.name} ${address} ${args.join(" ")}`);
    }
  };
  await attempt(d.impl, constructorArguments, "implementation");
  await attempt(d.address, [], "proxy");
}

async function main() {
  const cfg = chainConfig();
  if (!cfg.core) fail(`no core block for ${hre.network.name} in deploy/chains.js`);
  const core = cfg.core;
  const [signer] = await ethers.getSigners();
  const deployer = getAddress(signer.address);
  const net = await ethers.provider.getNetwork();
  const dev = process.env.CORE_DEV === "1";
  const LEDGER = ledgerNames(dev);
  const { admin, resolvers, pausers } = checkRoles(core, deployer, { dev });
  const feeRecipient = checkedAddress("CORE_FEE_RECIPIENT", core.feeRecipient);
  const forwarder = core.forwarder ? checkedAddress("CORE_FORWARDER", core.forwarder) : ZeroAddress;
  const initial = buildInitialConfig(cfg);

  console.log(`core ${dev ? "DEV set (deployer is admin; not for production)" : "set"} on ${hre.network.name} (chain ${net.chainId}), deployer ${deployer}`);
  console.log(`  admin ${admin}, fee recipient ${feeRecipient}, forwarder ${forwarder}`);
  console.log(`  resolvers [${resolvers.join(", ")}], pausers [${pausers.join(", ")}]`);
  initial.features.forEach((f, i) =>
    console.log(`  rate ${ethers.decodeBytes32String(f)} ${initial.tokens[i]}: ${initial.rates[i].bps} bps + ${initial.rates[i].flatFee} flat`));
  initial.limitTokens.forEach((t, i) =>
    console.log(`  limits ${t}: ${initial.limits[i].maxPerOrder} per order, ${initial.limits[i].maxOwed} owed`));

  if (core.chainId && BigInt(core.chainId) !== net.chainId) fail(`the RPC is chain ${net.chainId}, the config expects ${core.chainId}`);
  if (forwarder !== ZeroAddress) {
    // The forwarder is immutable in the escrows and a wrong one could act as any user.
    if ((await ethers.provider.getCode(forwarder)) === "0x") fail(`forwarder ${forwarder} has no code`);
    const fwd = new ethers.Contract(forwarder, ["function eip712Domain() view returns (bytes1,string,string,uint256,address,bytes32,uint256[])"], ethers.provider);
    const [, name, , fwdChain, verifying] = await fwd.eip712Domain();
    if (!core.forwarderName) fail("set CORE_FORWARDER_NAME: the EIP-712 name the forwarder was deployed with");
    if (name !== core.forwarderName || fwdChain !== net.chainId || getAddress(verifying) !== forwarder) {
      fail(`${forwarder} reports "${name}" on chain ${fwdChain}, not the expected forwarder "${core.forwarderName}"`);
    }
  }
  const symbolOf = Object.fromEntries(Object.entries(cfg.tokens).filter(([, a]) => a).map(([sym, a]) => [getAddress(a), sym]));
  for (const t of new Set(initial.tokens)) {
    if ((await ethers.provider.getCode(t)) === "0x") fail(`token ${t} has no code`);
    const dec = Number(await new ethers.Contract(t, ["function decimals() view returns (uint8)"], ethers.provider).decimals());
    const want = core.decimals[symbolOf[t]];
    if (dec !== want) fail(`token ${symbolOf[t]} ${t} has ${dec} decimals, the limits assume ${want}`);
  }

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1: nothing sent.");
    return;
  }

  for (const name of Object.values(LEDGER)) {
    const prior = reuseFromLedger(hre, name, null);
    if (prior) fail(`${name} is already recorded (${prior.proxy}); set FORCE_REDEPLOY=1 to deploy a new set`);
  }

  // Each proxy is recorded the moment it exists, so a run that dies part-way leaves every live
  // contract in the ledger (and the guard above refuses a blind re-run) instead of orphaning it.
  // `readBack` turns true only once the whole set has been checked.
  const record = (key, d) => writeLedger(hre.network.name, LEDGER[key], { proxy: d.address, impl: d.impl, admin, dev, readBack: false });
  const out = {};
  out.feeConfig = await deployProxy("FeeConfig", "FeeConfig", [admin, feeRecipient, initial], []);
  record("feeConfig", out.feeConfig);
  const fc = out.feeConfig.address;
  if (core.order) {
    out.order = await deployProxy("OrderEscrow", "OrderEscrow", [admin, resolvers, pausers], [forwarder, fc]);
    record("order", out.order);
  }
  out.p2p = await deployProxy("P2PTradeEscrow", P2P_FQN, [admin, resolvers, pausers], [forwarder, fc]);
  record("p2p", out.p2p);

  // Read everything back before recording it.
  await assertRoles("FeeConfig", out.feeConfig.contract, ["CONFIG_ROLE", "UPGRADER_ROLE"], admin, deployer);
  if (getAddress(await out.feeConfig.contract.feeRecipient()) !== feeRecipient) fail("FeeConfig fee recipient mismatch");
  for (let i = 0; i < initial.features.length; i++) {
    const r = await out.feeConfig.contract.rateFor(initial.features[i], initial.tokens[i]);
    if (Number(r.bps) !== initial.rates[i].bps || r.flatFee !== initial.rates[i].flatFee) fail(`FeeConfig rate ${i} mismatch`);
  }
  for (let i = 0; i < initial.limitTokens.length; i++) {
    const l = await out.feeConfig.contract.limitsFor(initial.limitTokens[i]);
    if (l.maxPerOrder !== initial.limits[i].maxPerOrder || l.maxOwed !== initial.limits[i].maxOwed) fail(`FeeConfig limits ${i} mismatch`);
  }
  for (const [key, label] of [["order", "OrderEscrow"], ["p2p", "P2PTradeEscrow"]]) {
    if (!out[key]) continue;
    const c = out[key].contract;
    await assertRoles(label, c, ["UPGRADER_ROLE", "PAUSER_ROLE", "RESCUER_ROLE", "RESOLVER_ROLE"], admin, deployer);
    if (getAddress(await c.feeConfig()) !== fc) fail(`${label}: feeConfig mismatch`);
    if (getAddress(await c.trustedForwarder()) !== forwarder) fail(`${label}: forwarder mismatch`);
    for (const r of resolvers) if (!(await c.hasRole(await c.RESOLVER_ROLE(), r))) fail(`${label}: resolver ${r} missing`);
    for (const p of pausers) if (!(await c.hasRole(await c.PAUSER_ROLE(), p))) fail(`${label}: pauser ${p} missing`);
  }
  console.log(`  read-back OK: admin holds every role${dev ? "" : ", the deployer holds none"}`);

  for (const key of Object.keys(LEDGER)) {
    if (out[key]) writeLedger(hre.network.name, LEDGER[key], { proxy: out[key].address, impl: out[key].impl, admin, dev, readBack: true });
  }

  await verify(out.feeConfig, "FeeConfig");
  if (out.order) await verify(out.order, "OrderEscrow", [forwarder, fc]);
  await verify(out.p2p, "P2PTradeEscrow", [forwarder, fc]);
  return out;
}

if (require.main === module) {
  main().then(() => process.exit(0)).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { main, buildInitialConfig, checkRoles, ledgerNames };
