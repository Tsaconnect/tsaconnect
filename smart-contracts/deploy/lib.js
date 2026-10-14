// smart-contracts/deploy/lib.js
const fs = require("fs");
const path = require("path");
const { isAddress, getAddress } = require("ethers");
const { chains } = require("./chains");

const LEDGER_DIR = path.join(__dirname, "..", "deployments");

function resolveChain(hre) {
  const name = hre.network.name;
  const cfg = chains[name];
  if (!cfg) throw new Error(`deploy: no chain config for network '${name}'`);
  validateAddresses(cfg, name);
  return cfg;
}

// validateAddresses throws on any malformed non-null address in a chain config,
// so a typo'd env override (or a bad hardcoded default) fails fast before a
// deploy targets a wrong address. null is allowed — it means intentionally
// absent (e.g. MCGP on BSC, owner → deployer fallback).
function validateAddresses(cfg, network) {
  const check = (label, v) => {
    if (v == null) return;
    if (!isAddress(v)) throw new Error(`deploy: invalid address for ${label} on ${network}: ${v}`);
  };
  for (const [sym, addr] of Object.entries(cfg.tokens)) check(`tokens.${sym}`, addr);
  check("permit2", cfg.permit2);
  check("forwarder", cfg.forwarder);
  check("owner", cfg.owner);
  check("systemWallet", cfg.systemWallet);
  (cfg.admins || []).forEach((a, i) => check(`admins[${i}]`, a));
  if (cfg.liquidityPool) {
    for (const k of ["collateralProvider", "operator", "guardian", "recipient", "owner", "ownerConfirm"]) {
      check(`liquidityPool.${k}`, cfg.liquidityPool[k]);
    }
  }
}

function requireAddr(cfg, label, value) {
  if (!value) throw new Error(`deploy: ${label} not configured for this network`);
  if (!isAddress(value)) throw new Error(`deploy: ${label} is not a valid address: ${value}`);
  return value;
}

function ledgerPath(network) {
  return path.join(LEDGER_DIR, `${network}.json`);
}

function readLedger(network) {
  const p = ledgerPath(network);
  if (!fs.existsSync(p)) return {};
  try {
    return JSON.parse(fs.readFileSync(p, "utf8"));
  } catch (e) {
    throw new Error(`deploy: ledger ${p} is corrupt (${e.message}) — fix or delete it`);
  }
}

function writeLedger(network, key, addresses) {
  if (!fs.existsSync(LEDGER_DIR)) fs.mkdirSync(LEDGER_DIR, { recursive: true });
  const ledger = readLedger(network);
  ledger[key] = { ...addresses, updatedAt: new Date().toISOString() };
  fs.writeFileSync(ledgerPath(network), JSON.stringify(ledger, null, 2) + "\n");
}

// reuseFromLedger returns a prior deployment of `key` on the active network so a
// re-run (e.g. after a mid-sequence failure) reuses it instead of orphaning a
// fresh proxy. CLI path only: the test path (chain provided) and FORCE_REDEPLOY=1
// always redeploy.
function reuseFromLedger(hre, key, chain) {
  if (chain || process.env.FORCE_REDEPLOY === "1") return null;
  return readLedger(hre.network.name)[key] || null;
}

// assertOwnerConfirm is the pre-deploy guard for an irreversible one-step
// Ownable handover (review m1): the hot deployer key must never be the final
// owner, and the owner address must be typed twice. Throws before anything is
// sent, so a refused run deploys nothing.
function assertOwnerConfirm({ label, owner, confirm, deployerAddr, ownerVar, confirmVar }) {
  if (getAddress(owner) === getAddress(deployerAddr)) {
    throw new Error(`${label}: ${ownerVar} is the deployer — a hot key must never own this contract on this network`);
  }
  if (!confirm || getAddress(confirm) !== getAddress(owner)) {
    throw new Error(`${label}: set ${confirmVar} to the same address as ${ownerVar} (one-step Ownable is irreversible)`);
  }
}

// handOverOwnership is the one ownership helper every deploy script uses: it
// moves each contract from the deployer (which must own it to run the
// onlyOwner setup) to finalOwner as the LAST step of a run. Contracts already
// owned by finalOwner are skipped, so finalOwner == deployer sends nothing.
// Every owner() is read back afterwards (even when a transfer throws) so a
// split handover is never silent; a failing read-back is logged and never
// masks the transfer error. Throws unless every contract ends at finalOwner.
async function handOverOwnership(label, contracts, finalOwner) {
  const want = getAddress(finalOwner);
  let transferError = null;
  try {
    for (const c of contracts) {
      if (getAddress(await c.owner()) !== want) await (await c.transferOwnership(want)).wait();
    }
  } catch (e) {
    transferError = e;
  }
  let owners = null;
  try {
    owners = [];
    for (const c of contracts) owners.push(getAddress(await c.owner()));
    console.log(`${label}: owner() read back — ${owners.join(", ")}`);
    if (new Set(owners).size > 1) {
      console.error(`${label}: SPLIT OWNERSHIP — finish the transfer from the current owner before anything else`);
    }
  } catch (readError) {
    console.error(`${label}: owner() read-back failed (${readError.message}) — check every owner() by hand`);
    if (!transferError) throw readError;
  }
  if (transferError) throw transferError;
  if (owners.some((o) => o !== want)) throw new Error(`${label}: owner() is not ${want} on every contract`);
}

module.exports = {
  assertOwnerConfirm,
  handOverOwnership,
  resolveChain,
  validateAddresses,
  requireAddr,
  readLedger,
  writeLedger,
  reuseFromLedger,
};
