// smart-contracts/deploy/lib.js
const fs = require("fs");
const path = require("path");
const { chains } = require("./chains");

const LEDGER_DIR = path.join(__dirname, "..", "deployments");

function resolveChain(hre) {
  const name = hre.network.name;
  const cfg = chains[name];
  if (!cfg) throw new Error(`deploy: no chain config for network '${name}'`);
  return cfg;
}

function requireAddr(cfg, label, value) {
  if (!value) throw new Error(`deploy: ${label} not configured for this network`);
  return value;
}

function ledgerPath(network) {
  return path.join(LEDGER_DIR, `${network}.json`);
}

function readLedger(network) {
  const p = ledgerPath(network);
  return fs.existsSync(p) ? JSON.parse(fs.readFileSync(p, "utf8")) : {};
}

function writeLedger(network, key, addresses) {
  if (!fs.existsSync(LEDGER_DIR)) fs.mkdirSync(LEDGER_DIR, { recursive: true });
  const ledger = readLedger(network);
  ledger[key] = { ...addresses, updatedAt: new Date().toISOString() };
  fs.writeFileSync(ledgerPath(network), JSON.stringify(ledger, null, 2) + "\n");
}

module.exports = { resolveChain, requireAddr, readLedger, writeLedger };
