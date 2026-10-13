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
// The deploy key is supplied for this run only (DET_DEPLOYER_PRIVATE_KEY in the
// process env, typed with hidden input — see README); it is never the routine
// .env DEPLOYER_PRIVATE_KEY nor any hardhat-config account.
//
//   read -r -s -p "deploy key: " K; echo
//   DET_DEPLOYER_PRIVATE_KEY="$K" DET_OWNER_CONFIRM=0x62bA… npx hardhat run deploy/deterministic-escrow.js --network bscTestnet; unset K
//   CHECK_ONLY=1 [DET_DEPLOYER=0x…] npx hardhat run deploy/deterministic-escrow.js --network bscTestnet
const fs = require("fs");
const path = require("path");
const { getAddress, isAddress, keccak256, concat, AbiCoder, Wallet, Contract } = require("ethers");
// The same OpenZeppelin v5 ERC1967Proxy that hardhat-upgrades deploys for every
// other proxy in this repo (its dist/utils/factories.js loads this path).
const ERC1967Proxy = require("@openzeppelin/upgrades-core/artifacts/@openzeppelin/contracts-v5/proxy/ERC1967/ERC1967Proxy.sol/ERC1967Proxy.json");
const { resolveChain, writeLedger, assertOwnerConfirm, handOverOwnership } = require("./lib");
const { DETERMINISTIC_ROLES } = require("./chains");
const { wireEscrow } = require("./p2p-escrow");
const create2 = require("./create2");

const CONTRACT = "P2PEscrowV3";
const IMPL_SALT_LABEL = `tsa:${CONTRACT}:impl:v1`;
const DEFAULT_NAME = "direct-p2p:v1";
const proxySaltLabel = (name) => `tsa:${CONTRACT}:proxy:${name}`;
const ENV_FILE = path.join(__dirname, "..", ".env");

// Mirrors P2PEscrowV3's public constants (the test asserts they match), so a bad
// DET_* value is refused before anything is sent rather than reverting mid-setup.
const ESCROW_LIMITS = { MAX_FEE_BPS: 500, MIN_PAYMENT_WINDOW: 5 * 60, MAX_PAYMENT_WINDOW: 24 * 60 * 60 };

const same = (a, b) => getAddress(a) === getAddress(b);

function envOptions(env = process.env) {
  return {
    name: env.DET_NAME || DEFAULT_NAME,
    ownerConfirm: env.DET_OWNER_CONFIRM || null,
    tokens: env.DET_TOKENS ? env.DET_TOKENS.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
    feeManager: env.DET_FEE_MANAGER || undefined,
    paymentWindowSec: env.DET_PAYMENT_WINDOW || undefined,
    feeBps: env.DET_FEE_BPS || undefined,
    checkOnly: env.CHECK_ONLY === "1",
    deployer: env.DET_DEPLOYER || undefined,
    deployerKey: takeDeployKey(env),
  };
}

// takeDeployKey reads the per-run deploy key and removes it from the
// environment at once, so no child process, crash report or env dump made
// later in the run can carry it.
function takeDeployKey(env) {
  const key = env.DET_DEPLOYER_PRIVATE_KEY || undefined;
  delete env.DET_DEPLOYER_PRIVATE_KEY;
  return key;
}

// networkClass keeps testnet rehearsals (often with a throwaway deploy key) from
// pinning the mainnet address: the manifest is keyed by class, then salt label.
function networkClass(hre) {
  const name = hre.network.name;
  if (create2.isLocalNetwork(hre)) return "local";
  if (/mainnet$/i.test(name)) return "mainnet";
  if (/testnet$/i.test(name)) return "testnet";
  throw new Error(`deterministic: cannot tell whether network '${name}' is a mainnet or a testnet — name it *Mainnet or *Testnet`);
}

// addrOfKey never lets a key reach an error message: a malformed key is named
// by where it came from, never by value.
const addrOfKey = (k, source) => {
  try {
    return new Wallet(k.startsWith("0x") ? k : "0x" + k).address;
  } catch {
    throw new Error(`deterministic: ${source} is not a valid private key`);
  }
};

// routineDeployerAddresses: every key this repo uses for routine deploys — the
// active network's hardhat-config accounts and the DEPLOYER_PRIVATE_KEY in .env.
// Only addresses leave this function; keys are never logged.
function routineDeployerAddresses(hre, envFile = ENV_FILE) {
  const out = [];
  const accounts = hre.network.config && hre.network.config.accounts;
  if (Array.isArray(accounts)) for (const a of accounts) out.push(addrOfKey(typeof a === "string" ? a : a.privateKey, `a hardhat-config account on ${hre.network.name}`));
  if (process.env.DEPLOYER_PRIVATE_KEY) out.push(addrOfKey(process.env.DEPLOYER_PRIVATE_KEY, "DEPLOYER_PRIVATE_KEY"));
  if (fs.existsSync(envFile)) {
    const parsed = require("dotenv").parse(fs.readFileSync(envFile));
    if (parsed.DET_DEPLOYER_PRIVATE_KEY) {
      throw new Error("deterministic: DET_DEPLOYER_PRIVATE_KEY is stored in .env — remove it; the deploy key is typed in for one run only");
    }
    if (parsed.DEPLOYER_PRIVATE_KEY) out.push(addrOfKey(parsed.DEPLOYER_PRIVATE_KEY, ".env DEPLOYER_PRIVATE_KEY"));
  }
  return out;
}

// deployerSigner turns the per-run deploy key into a signer on the active
// provider, refusing any routine or hot key. Error messages carry addresses only.
function deployerSigner(hre, key, extraRoutine = []) {
  let wallet;
  try {
    wallet = new Wallet(key.startsWith("0x") ? key : "0x" + key, hre.ethers.provider);
  } catch {
    throw new Error("deterministic: DET_DEPLOYER_PRIVATE_KEY is not a valid private key");
  }
  for (const r of [...routineDeployerAddresses(hre), ...extraRoutine]) {
    if (same(r, wallet.address)) {
      throw new Error(`deterministic: the deploy key ${wallet.address} is a routine deploy key (.env DEPLOYER_PRIVATE_KEY / hardhat-config account) — use the dedicated cold deploy key`);
    }
  }
  return wallet;
}

// The deployer decides who can claim this address on a chain we have not used
// yet (anyone can replay the CREATE2 calldata), so it must be a cold key kept
// for deployments only, never a hot server key.
function assertNotHotKey(deployer, roles) {
  for (const [role, addr] of [["operator", roles.operator], ["treasury", roles.treasury]]) {
    if (same(deployer, addr)) {
      throw new Error(`deterministic: the deployer ${deployer} is the ${role} — use a dedicated cold deploy key, never a hot server key`);
    }
  }
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
// previous chain of the same class landed at for the same salt — a different
// deployer, operator or compiled bytecode would otherwise silently put this
// chain somewhere else.
function assertManifest(entry, p) {
  if (!entry) return;
  const refuse = (diffs) => new Error(
    `deterministic: refusing — ${p.proxySaltLabel} would not land at the recorded ${entry.proxy}:\n  ` + diffs.join("\n  ") +
    "\nUse the recorded deployer key and operator, and build from the commit that recorded the entry (same bytecode)."
  );
  const ia = entry.initArgs;
  if (!ia || !ia.owner || !Array.isArray(ia.admins)) {
    throw refuse(["the manifest entry has no initArgs.owner / initArgs.admins — it is incomplete; restore deploy/deterministic.json from git"]);
  }
  const diffs = RECORDED.filter((k) => entry[k] !== p[k]).map((k) => `${k}: recorded ${entry[k]}, now ${p[k]}`);
  if (!same(ia.owner, p.initArgs.owner)) diffs.push(`initialize owner (deployer): recorded ${ia.owner}, now ${p.initArgs.owner}`);
  if (ia.admins.join() !== p.initArgs.admins.join()) diffs.push(`initialize admins: recorded ${ia.admins}, now ${p.initArgs.admins}`);
  if (diffs.length) throw refuse(diffs);
}

function manifestEntry(file, cls, label) {
  return file ? (create2.readManifest(file)[cls] || {})[label] || null : null;
}

function recordManifest(file, cls, p, network, chainId) {
  const manifest = create2.readManifest(file);
  const section = manifest[cls] || {};
  const prev = section[p.proxySaltLabel] || {};
  const entry = {};
  for (const k of ["contract", "implSaltLabel", ...RECORDED.slice(0, 3), "proxySaltLabel", ...RECORDED.slice(3), "initArgs"]) entry[k] = p[k];
  entry.networks = { ...(prev.networks || {}), [String(chainId)]: { network, recordedAt: new Date().toISOString() } };
  section[p.proxySaltLabel] = entry;
  manifest[cls] = section;
  create2.writeManifest(manifest, file);
}

// recordedProxy is how deploy/upgrade.js finds a deterministic proxy: the
// committed manifest entry for this network class + DET_NAME, only if this
// chain id is recorded on it (trust per chain, never by equality).
async function recordedProxy(hre, name, manifestPath = create2.MANIFEST_PATH) {
  const entry = manifestEntry(manifestPath, networkClass(hre), proxySaltLabel(name));
  const chainId = String((await hre.ethers.provider.getNetwork()).chainId);
  if (!entry || !entry.networks || !entry.networks[chainId]) {
    throw new Error(`deterministic: no recorded ${proxySaltLabel(name)} on chain ${chainId} (${hre.network.name}) in ${manifestPath}`);
  }
  return entry.proxy;
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

function intInRange(label, v, min, max) {
  if (v === undefined || v === null || v === "") return undefined;
  const n = Number(v);
  if (!Number.isInteger(n) || n < min || n > max) throw new Error(`deterministic: ${label} must be an integer in [${min}, ${max}], got ${v}`);
  return n;
}

// validateParams refuses every bad per-chain value before anything is sent or
// written: fee, window (the contract's bounds), FeeManager and token contracts.
async function validateParams(provider, cfg, opts) {
  const feeBps = intInRange("DET_FEE_BPS", opts.feeBps, 0, ESCROW_LIMITS.MAX_FEE_BPS);
  const paymentWindowSec = intInRange("DET_PAYMENT_WINDOW", opts.paymentWindowSec, ESCROW_LIMITS.MIN_PAYMENT_WINDOW, ESCROW_LIMITS.MAX_PAYMENT_WINDOW);
  let feeManager;
  if (opts.feeManager !== undefined && opts.feeManager !== null && opts.feeManager !== "") {
    if (!isAddress(opts.feeManager)) throw new Error(`deterministic: DET_FEE_MANAGER is not a valid address: ${opts.feeManager}`);
    feeManager = getAddress(opts.feeManager);
    if (!(await create2.hasCode(provider, feeManager))) throw new Error(`deterministic: DET_FEE_MANAGER ${feeManager} has no code on this network`);
  }
  const tokens = pickTokens(cfg.tokens, opts.tokens);
  if (!Object.keys(tokens).length) throw new Error("deterministic: no tokens to accept");
  for (const [sym, addr] of Object.entries(tokens)) {
    if (!isAddress(addr)) throw new Error(`deterministic: token ${sym} is not a valid address: ${addr}`);
    if (!(await create2.hasCode(provider, addr))) throw new Error(`deterministic: token ${sym} ${addr} has no code on this network`);
  }
  return { feeBps, paymentWindowSec, feeManager, tokens };
}

// assertEscrow reads back every per-chain value; used after setup and as the
// whole of a run on a chain that is already handed over.
async function assertEscrow(esc, { owner, operator, treasury, tokens, cfg, feeManager, paymentWindowSec, feeBps }) {
  const bad = [];
  if (owner && !same(await esc.owner(), owner)) bad.push(`owner is not ${owner}`);
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
//   feeManager, paymentWindowSec, feeBps, checkOnly, deployerKey (the per-run
//   deploy key), deployer (address: CHECK_ONLY / verify without a key),
//   routineAddresses (extra addresses to refuse as deployer), networkClass
//   (override), manifestPath (default deploy/deterministic.json; never written
//   on hardhat unless given).
async function deploy(hre, opts = {}) {
  const { ethers, upgrades, network } = hre;
  const provider = ethers.provider;
  const cfg = opts.chain || resolveChain(hre);
  const roles = { ...DETERMINISTIC_ROLES, ...(opts.roles || {}) };
  for (const [k, v] of Object.entries(roles)) {
    if (!v || !isAddress(v)) throw new Error(`deterministic: role ${k} is not a valid address: ${v}`);
  }
  const name = opts.name || DEFAULT_NAME;
  const cls = opts.networkClass || networkClass(hre);
  const manifestFile = opts.manifestPath || (create2.isLocalNetwork(hre) ? null : create2.MANIFEST_PATH);
  const recorded = manifestEntry(manifestFile, cls, proxySaltLabel(name));

  // Deliberately NOT ethers.getSigners(): that is the routine .env key.
  const signer = opts.deployerKey ? deployerSigner(hre, opts.deployerKey, opts.routineAddresses) : null;
  if (signer && opts.deployer && !same(opts.deployer, signer.address)) {
    throw new Error(`deterministic: DET_DEPLOYER ${opts.deployer} is not the deploy key's address ${signer.address}`);
  }
  const deployer = signer ? signer.address : opts.deployer || (recorded && recorded.initArgs && recorded.initArgs.owner);
  if (!deployer) {
    throw new Error("deterministic: no deployer — set DET_DEPLOYER_PRIVATE_KEY to deploy, or DET_DEPLOYER=0x… to check/verify without a key");
  }
  if (!isAddress(deployer)) throw new Error(`deterministic: DET_DEPLOYER is not a valid address: ${deployer}`);
  assertNotHotKey(deployer, roles);

  const chainId = (await provider.getNetwork()).chainId;
  const p = await plan(hre, { deployer, operator: roles.operator, name });
  assertManifest(recorded, p);
  const v = await validateParams(provider, cfg, opts);
  const params = { operator: roles.operator, treasury: roles.treasury, cfg, ...v };

  const exists = { factory: await create2.hasCode(provider, create2.FACTORY), impl: await create2.hasCode(provider, p.impl), proxy: await create2.hasCode(provider, p.proxy) };
  const esc = new Contract(p.proxy, p._Impl.interface, signer || provider);
  const currentOwner = exists.proxy ? getAddress(await esc.owner()) : null;
  console.log(
    `deterministic: ${network.name} (chainId ${chainId}, ${cls}) deployer=${deployer}${signer ? "" : " (address only, no key)"}\n` +
    `  factory ${create2.FACTORY} ${exists.factory ? "present" : "MISSING"}\n` +
    `  impl    ${p.impl} [${IMPL_SALT_LABEL}] ${exists.impl ? "already deployed" : "not deployed"}\n` +
    `  proxy   ${p.proxy} [${p.proxySaltLabel}] ${exists.proxy ? `already deployed, owner ${currentOwner}` : "not deployed"}\n` +
    `  initialize(${deployer}, [${roles.operator}]) → owner after handover ${roles.owner}, systemWallet ${roles.treasury}, tokens ${Object.keys(v.tokens).join(",")}`
  );
  const result = { impl: p.impl, proxy: p.proxy, implSaltLabel: IMPL_SALT_LABEL, proxySaltLabel: p.proxySaltLabel };

  // Already handed over: verify and stop — the deployer can no longer change
  // anything, so this needs no key and sends nothing (CHECK_ONLY included).
  if (currentOwner && same(currentOwner, roles.owner)) {
    await verifyProxyCode(hre, p);
    await assertEscrow(esc, { ...params, owner: roles.owner });
    console.log(`deterministic: ${p.proxy} already deployed, verified and owned by ${roles.owner}`);
    return { ...result, owner: roles.owner, reused: true, verified: true, exists };
  }
  if (opts.checkOnly) return { ...result, checkOnly: true, exists };
  if (!signer) throw new Error("deterministic: a deploy needs DET_DEPLOYER_PRIVATE_KEY (the cold deploy key, typed in for this run)");
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
  if (manifestFile) recordManifest(manifestFile, cls, p, network.name, chainId);
  // Registers the proxy + implementation with the OpenZeppelin upgrades
  // manifest (.openzeppelin/<network>.json) so upgrade.js / upgradeProxy work.
  await upgrades.forceImport(p.proxy, p._Impl, { kind: "uups" });

  try {
    await wireEscrow(esc, { cfg, tokens: v.tokens, systemWallet: roles.treasury, feeManager: v.feeManager });
    if (v.feeBps !== undefined && (await esc.feeBps()) !== BigInt(v.feeBps)) await (await esc.setFeeBps(v.feeBps)).wait();
    if (v.paymentWindowSec !== undefined && (await esc.paymentWindow()) !== BigInt(v.paymentWindowSec)) {
      await (await esc.setPaymentWindow(v.paymentWindowSec)).wait();
    }
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
  // Message only: a full error object can carry nested request/transaction data.
  deploy(require("hardhat"), envOptions()).catch((e) => { console.error(`deterministic: ${e && e.message ? e.message : e}`); process.exit(1); });
}

module.exports = {
  deploy, plan, envOptions, networkClass, recordedProxy, routineDeployerAddresses, assertManifest,
  ESCROW_LIMITS, CONTRACT, IMPL_SALT_LABEL, DEFAULT_NAME, proxySaltLabel,
};
