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

// transferOwnershipIfNeeded hands a freshly-wired contract from the deployer
// (which must own it to run the onlyOwner setup) to the configured owner. It is
// a no-op when they are the same address. Returns true if a transfer happened.
async function transferOwnershipIfNeeded(contract, deployerAddr, finalOwner) {
  if (getAddress(finalOwner) === getAddress(deployerAddr)) return false;
  await (await contract.transferOwnership(finalOwner)).wait();
  return true;
}

module.exports = {
  resolveChain,
  validateAddresses,
  requireAddr,
  readLedger,
  writeLedger,
  reuseFromLedger,
  transferOwnershipIfNeeded,
};
