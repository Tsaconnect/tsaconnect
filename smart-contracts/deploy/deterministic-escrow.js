// smart-contracts/deploy/deterministic-escrow.js
//
// One P2PEscrowV3 at the SAME address on every EVM chain (CREATE2 through the
// standard deterministic deployment proxy, deploy/create2.js). Works because the
// PM owner, the operator (release admin) and the treasury are one address on
// every chain since the 2026-10 signer consolidation. See deploy/README.md.
//
//   implementation = CREATE2(salt keccak("tsa:P2PEscrowV3:impl:v1"), P2PEscrowV3 creation code)
//   proxy          = CREATE2(salt keccak("tsa:P2PEscrowV3:proxy:<DET_NAME>"),
//                            ERC1967Proxy(impl, initialize(DEPLOYER, [OPERATOR])))
//
// The proxy is initialised owned by the DEPLOYER so this run can do the
// per-chain onlyOwner setup; ownership moves to the PM at the end of the SAME
// run, after every value is read back. Anyone can front-run either CREATE2 (the
// factory is permissionless), but the init calldata is fixed, so a front-runner
// only pays our gas and the deployer still owns the proxy.
//
//   DET_OWNER_CONFIRM=0x62bA… npx hardhat run deploy/deterministic-escrow.js --network bscTestnet
//   CHECK_ONLY=1 [DET_DEPLOYER=0x…] npx hardhat run deploy/deterministic-escrow.js --network bscTestnet
const { getAddress, isAddress, keccak256, concat, AbiCoder } = require("ethers");
const ERC1967Proxy = require("@openzeppelin/upgrades-core/artifacts/@openzeppelin/contracts/proxy/ERC1967/ERC1967Proxy.sol/ERC1967Proxy.json");
const { resolveChain, writeLedger, assertOwnerConfirm, handOverOwnership } = require("./lib");
const { DETERMINISTIC_ROLES } = require("./chains");
const { wireEscrow } = require("./p2p-escrow");
const create2 = require("./create2");

const CONTRACT = "P2PEscrowV3";
const IMPL_SALT_LABEL = `tsa:${CONTRACT}:impl:v1`;
const DEFAULT_NAME = "direct-p2p:v1";
const proxySaltLabel = (name) => `tsa:${CONTRACT}:proxy:${name}`;

const same = (a, b) => getAddress(a) === getAddress(b);

function envOptions(env = process.env) {
  const num = (v) => (v === undefined || v === "" ? undefined : Number(v));
  return {
    name: env.DET_NAME || DEFAULT_NAME,
    ownerConfirm: env.DET_OWNER_CONFIRM || null,
    tokens: env.DET_TOKENS ? env.DET_TOKENS.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
    feeManager: env.DET_FEE_MANAGER || undefined,
    paymentWindowSec: num(env.DET_PAYMENT_WINDOW),
    feeBps: num(env.DET_FEE_BPS),
    checkOnly: env.CHECK_ONLY === "1",
    deployer: env.DET_DEPLOYER || undefined,
  };
}

// plan computes both addresses from the init code alone — no RPC — so the
// check-only path and the manifest guard see exactly what a deploy would send.
async function plan(hre, { deployer, operator, name }) {
  const Impl = await hre.ethers.getContractFactory(CONTRACT);
  const implSalt = create2.saltOf(IMPL_SALT_LABEL);
  const implInitCode = Impl.bytecode;
  const impl = create2.predict(implSalt, implInitCode);
  const initData = Impl.interface.encodeFunctionData("initialize", [getAddress(deployer), [getAddress(operator)]]);
  const proxySalt = create2.saltOf(proxySaltLabel(name));
  const proxyInitCode = concat([
    ERC1967Proxy.bytecode,
    AbiCoder.defaultAbiCoder().encode(["address", "bytes"], [impl, initData]),
  ]);
  return {
    contract: CONTRACT,
    implSaltLabel: IMPL_SALT_LABEL, implSalt, implInitCodeHash: keccak256(implInitCode), impl,
    proxySaltLabel: proxySaltLabel(name), proxySalt, proxyInitCodeHash: keccak256(proxyInitCode), proxy: create2.predict(proxySalt, proxyInitCode),
    initArgs: { owner: getAddress(deployer), admins: [getAddress(operator)] },
    _implInitCode: implInitCode, _proxyInitCode: proxyInitCode, _Impl: Impl,
  };
}

const RECORDED = ["implSalt", "implInitCodeHash", "impl", "proxySalt", "proxyInitCodeHash", "proxy"];

// assertManifest refuses a run whose addresses would differ from the ones a
// previous chain landed at for the same salt — a different deployer, operator
// or compiled bytecode would otherwise silently put this chain somewhere else.
function assertManifest(entry, p) {
  if (!entry) return;
  const diffs = RECORDED.filter((k) => entry[k] !== p[k]).map((k) => `${k}: recorded ${entry[k]}, now ${p[k]}`);
  if (!same(entry.initArgs.owner, p.initArgs.owner)) diffs.push(`initialize owner (deployer): recorded ${entry.initArgs.owner}, now ${p.initArgs.owner}`);
  if (entry.initArgs.admins.join() !== p.initArgs.admins.join()) diffs.push(`initialize admins: recorded ${entry.initArgs.admins}, now ${p.initArgs.admins}`);
  if (diffs.length) {
    throw new Error(
      `deterministic: refusing — ${p.proxySaltLabel} would not land at the recorded ${entry.proxy}:\n  ` + diffs.join("\n  ") +
      "\nUse the recorded deployer key and operator, and build from the commit that recorded the entry (same bytecode)."
    );
  }
}

function recordManifest(file, p, network, chainId) {
  const manifest = create2.readManifest(file);
  const prev = manifest[p.proxySaltLabel] || {};
  const entry = {};
  for (const k of ["contract", "implSaltLabel", ...RECORDED.slice(0, 3), "proxySaltLabel", ...RECORDED.slice(3), "initArgs"]) entry[k] = p[k];
  entry.networks = { ...(prev.networks || {}), [String(chainId)]: { network, recordedAt: new Date().toISOString() } };
  manifest[p.proxySaltLabel] = entry;
  create2.writeManifest(manifest, file);
}

function pickTokens(cfgTokens, symbols) {
  if (!symbols) return Object.fromEntries(Object.entries(cfgTokens).filter(([, a]) => a));
  const out = {};
  for (const sym of symbols) {
    if (!cfgTokens[sym]) throw new Error(`deterministic: token ${sym} has no address on this network`);
    out[sym] = cfgTokens[sym];
  }
  return out;
}

// assertEscrow reads back every per-chain value; used after setup and as the
// whole of a re-run on a chain that is already handed over.
async function assertEscrow(esc, { operator, treasury, tokens, cfg, feeManager, paymentWindowSec, feeBps }) {
  const bad = [];
  if (!(await esc.isAdmin(operator))) bad.push(`operator ${operator} is not an admin`);
  if (!same(await esc.systemWallet(), treasury)) bad.push(`systemWallet is not the treasury ${treasury}`);
  for (const [sym, addr] of Object.entries(tokens)) {
    if (!(await esc.acceptedTokens(addr))) bad.push(`${sym} ${addr} is not accepted`);
    else if ((await esc.tokenSymbol(addr)) !== sym) bad.push(`${sym} symbol is not set`);
  }
  if (tokens.MCGP && !same(await esc.mcgpToken(), tokens.MCGP)) bad.push("mcgpToken is not MCGP");
  if (cfg.permit2 && !same(await esc.permit2(), cfg.permit2)) bad.push("permit2 is not set");
  if (cfg.forwarder && !same(await esc.trustedForwarder(), cfg.forwarder)) bad.push("trustedForwarder is not set");
  if (feeManager && !same(await esc.feeManager(), feeManager)) bad.push(`feeManager is not ${feeManager}`);
  if (paymentWindowSec !== undefined && (await esc.paymentWindow()) !== BigInt(paymentWindowSec)) bad.push(`paymentWindow is not ${paymentWindowSec}`);
  if (feeBps !== undefined && (await esc.feeBps()) !== BigInt(feeBps)) bad.push(`feeBps is not ${feeBps}`);
  if (bad.length) throw new Error(`deterministic: escrow assertions failed — ${bad.join("; ")}`);
}

// Options (tests pass them; the CLI reads DET_* env — see envOptions):
//   chain, roles {owner, operator, treasury}, name, ownerConfirm, tokens (symbols),
//   feeManager, paymentWindowSec, feeBps, checkOnly, deployer (check-only),
//   manifestPath (default deploy/deterministic.json; never written on hardhat
//   unless given).
async function deploy(hre, opts = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = opts.chain || resolveChain(hre);
  const roles = { ...DETERMINISTIC_ROLES, ...(opts.roles || {}) };
  for (const [k, v] of Object.entries(roles)) {
    if (!v || !isAddress(v)) throw new Error(`deterministic: role ${k} is not a valid address: ${v}`);
  }
  const name = opts.name || DEFAULT_NAME;
  const [signer] = await ethers.getSigners();
  const deployer = signer ? await signer.getAddress() : opts.deployer;
  if (!deployer) throw new Error("deterministic: no deployer key configured (CHECK_ONLY accepts DET_DEPLOYER=0x… instead)");
  if (signer && opts.deployer && !same(opts.deployer, deployer)) {
    throw new Error(`deterministic: DET_DEPLOYER ${opts.deployer} is not the configured key ${deployer}`);
  }
  // The deployer decides who can claim this address on a chain we have not used
  // yet (anyone can replay the CREATE2 calldata), so it must be a cold key kept
  // for deployments only, never a hot server key.
  for (const [role, addr] of [["operator", roles.operator], ["treasury", roles.treasury]]) {
    if (same(deployer, addr)) {
      throw new Error(`deterministic: the deployer ${deployer} is the ${role} — use a dedicated cold deploy key, never a hot server key`);
    }
  }
  const chainId = (await ethers.provider.getNetwork()).chainId;
  const manifestFile = opts.manifestPath || (create2.isLocalNetwork(hre) ? null : create2.MANIFEST_PATH);

  const p = await plan(hre, { deployer, operator: roles.operator, name });
  assertManifest(manifestFile ? create2.readManifest(manifestFile)[p.proxySaltLabel] : null, p);
  const tokens = pickTokens(cfg.tokens, opts.tokens);
  const params = {
    operator: roles.operator, treasury: roles.treasury, tokens, cfg,
    feeManager: opts.feeManager, paymentWindowSec: opts.paymentWindowSec, feeBps: opts.feeBps,
  };

  const provider = ethers.provider;
  const exists = { factory: await create2.hasCode(provider, create2.FACTORY), impl: await create2.hasCode(provider, p.impl), proxy: await create2.hasCode(provider, p.proxy) };
  const esc = await ethers.getContractAt(CONTRACT, p.proxy);
  const currentOwner = exists.proxy ? getAddress(await esc.owner()) : null;
  console.log(
    `deterministic: ${network.name} (chainId ${chainId}) deployer=${deployer}\n` +
    `  factory ${create2.FACTORY} ${exists.factory ? "present" : "MISSING"}\n` +
    `  impl    ${p.impl} [${IMPL_SALT_LABEL}] ${exists.impl ? "already deployed" : "not deployed"}\n` +
    `  proxy   ${p.proxy} [${p.proxySaltLabel}] ${exists.proxy ? `already deployed, owner ${currentOwner}` : "not deployed"}\n` +
    `  initialize(${deployer}, [${roles.operator}]) → owner after handover ${roles.owner}, systemWallet ${roles.treasury}, tokens ${Object.keys(tokens).join(",")}`
  );
  const result = { impl: p.impl, proxy: p.proxy, implSaltLabel: IMPL_SALT_LABEL, proxySaltLabel: p.proxySaltLabel };
  if (opts.checkOnly) return { ...result, checkOnly: true, exists };
  if (!signer) throw new Error("deterministic: a deploy needs DEPLOYER_PRIVATE_KEY");

  // Already handed over: verify and stop — the deployer can no longer change anything.
  if (currentOwner && same(currentOwner, roles.owner)) {
    await verifyProxyCode(hre, p);
    await assertEscrow(esc, params);
    console.log(`deterministic: ${p.proxy} already deployed, verified and owned by ${roles.owner}`);
    return { ...result, owner: roles.owner, reused: true };
  }
  if (currentOwner && !same(currentOwner, deployer)) {
    throw new Error(`deterministic: ${p.proxy} is owned by ${currentOwner} — neither the deployer nor the PM ${roles.owner}`);
  }
  // Refuse before sending anything: the handover at the end is irreversible.
  assertOwnerConfirm({
    label: "deterministic", owner: roles.owner, confirm: opts.ownerConfirm, deployerAddr: deployer,
    ownerVar: "DET_OWNER (the PM)", confirmVar: "DET_OWNER_CONFIRM",
  });

  await create2.ensureFactory(hre);
  const implOut = await create2.deployCreate2(hre, signer, { salt: p.implSalt, initCode: p._implInitCode, label: "implementation" });
  const proxyOut = await create2.deployCreate2(hre, signer, { salt: p.proxySalt, initCode: p._proxyInitCode, label: "proxy" });
  await verifyProxyCode(hre, p);
  if (manifestFile) recordManifest(manifestFile, p, network.name, chainId);
  // Registers the proxy + implementation with the OpenZeppelin upgrades
  // manifest (.openzeppelin/<network>.json) so upgrade.js / upgradeProxy work.
  await upgrades.forceImport(p.proxy, p._Impl, { kind: "uups" });

  try {
    await wireEscrow(esc, { cfg, tokens, systemWallet: roles.treasury, feeManager: opts.feeManager });
    if (opts.feeBps !== undefined) await (await esc.setFeeBps(opts.feeBps)).wait();
    if (opts.paymentWindowSec !== undefined) await (await esc.setPaymentWindow(opts.paymentWindowSec)).wait();
    await assertEscrow(esc, params);
  } catch (e) {
    console.error(`deterministic: NOT handed over — ${p.proxy} is still owned by the deployer ${deployer}; re-run to resume`);
    throw e;
  }
  console.log(`deterministic: transferring ${p.proxy} to ${roles.owner} (one-step Ownable — this address is final)`);
  await handOverOwnership("deterministic", [esc], roles.owner);
  if (!create2.isLocalNetwork(hre)) writeLedger(network.name, `${CONTRACT}:${name}`, { proxy: p.proxy, impl: p.impl });
  console.log(`deterministic: ready ${p.proxy} (impl ${p.impl}${implOut.reused ? ", reused" : ""}${proxyOut.reused ? "; proxy reused" : ""}) owner=${roles.owner}`);
  return { ...result, owner: roles.owner, reused: proxyOut.reused };
}

// verifyProxyCode checks the bytes at the proxy are the ERC1967Proxy runtime
// and that it was initialised against OUR implementation (or, after a later
// upgrade, at least something with code).
async function verifyProxyCode(hre, p) {
  const code = await hre.ethers.provider.getCode(p.proxy);
  if (code !== ERC1967Proxy.deployedBytecode) throw new Error(`deterministic: ${p.proxy} is not an ERC1967Proxy`);
  const impl = await hre.upgrades.erc1967.getImplementationAddress(p.proxy);
  if (!same(impl, p.impl)) {
    console.warn(`deterministic: ${p.proxy} now points at ${impl}, not the deterministic impl ${p.impl} (upgraded since?)`);
    if (!(await create2.hasCode(hre.ethers.provider, impl))) throw new Error(`deterministic: implementation ${impl} has no code`);
  }
}

if (require.main === module) {
  deploy(require("hardhat"), envOptions()).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy, plan, envOptions, IMPL_SALT_LABEL, DEFAULT_NAME, proxySaltLabel };
