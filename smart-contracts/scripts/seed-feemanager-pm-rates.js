// DRAFT: applies the PM TARGET fee rates to a FeeManager (runbook §2.H.3 / §3.H.3).
// This is the rate flip that happens AFTER the flag is ON. Running it earlier
// changes charged fees — so by default this script is a DRY-RUN (diff only) and
// REFUSES to write unless APPLY=1 AND the unresolved targets are pinned via env.
//
// On-chain rateFor() is an EXACT (feature, token) lookup — no wildcard fallback.
// Therefore:
//   - off-chain/backend-read features (otc, swap, spot, allswap, withdrawal,
//     product*, service_contact, instant_*) get a single "*" row (the backend's
//     FeeRate() falls back specific→"*").
//   - ESCROW-read features (p2p, p2p_platform) are expanded PER SYMBOL
//     (USDT/USDC/MCGP) because P2PEscrowV3._snapshotFeeBps queries
//     rateFor(feature, tokenSymbol[token]). MCGP rows are exempt (invariant #6).
//
// Unresolved targets (pinned to CURRENT as placeholders → diff shows "no change",
// flagging that they still need a decision):
//   - P0-3: p2p (gas) + p2p_platform bps   → env P2P_GAS_BPS / P2P_PLATFORM_BPS
//   - P0-4: product gas (0.5% vs $0.10 flat) → env PRODUCT_GAS_BPS (+ PRODUCT_GAS_FLAT_USD)
//
// Run (dry-run / diff — address resolved from deployments ledger automatically):
//   npx hardhat run scripts/seed-feemanager-pm-rates.js --network sonicTestnet
// Override address:
//   FEE_MANAGER_ADDRESS=0x... npx hardhat run scripts/seed-feemanager-pm-rates.js --network sonicTestnet
// Apply (only after P0-3/P0-4 resolved + flag ON):
//   APPLY=1 P2P_GAS_BPS=.. P2P_PLATFORM_BPS=.. PRODUCT_GAS_BPS=.. FEE_MANAGER_ADDRESS=0x... \
//     npx hardhat run scripts/seed-feemanager-pm-rates.js --network sonicTestnet

const hre = require("hardhat");
const { ethers, network } = hre;
const { readLedger } = require("../deploy/lib");

const ESCROW_SYMBOLS = ["USDT", "USDC"]; // non-MCGP escrow tokens get the PM bps
const MCGP_SYMBOL = "MCGP";              // exempt everywhere (invariant #6)
const flat = (usd) => BigInt(Math.round(usd * 1e6));
const envInt = (k) => (process.env[k] !== undefined ? parseInt(process.env[k], 10) : undefined);

// PM target table — mirrors runbook §67-78. `tbd` marks unresolved decisions.
function pmTable() {
  const p2pGas = envInt("P2P_GAS_BPS");
  const p2pPlat = envInt("P2P_PLATFORM_BPS");
  const prodGasBps = envInt("PRODUCT_GAS_BPS");
  const prodGasFlat = process.env.PRODUCT_GAS_FLAT_USD !== undefined ? parseFloat(process.env.PRODUCT_GAS_FLAT_USD) : undefined;
  return [
    { feature: "product_merchant", scope: "wild", bps: 200, flatUSD: 0, note: "unchanged (2%)" },
    { feature: "product", scope: "wild", bps: prodGasBps ?? 0, flatUSD: prodGasFlat ?? 0.10, note: "P0-4: 0.5% vs $0.10 flat", tbd: "P0-4" },
    { feature: "service_contact", scope: "wild", bps: 0, flatUSD: 0.10, note: "unchanged ($0.10)" },
    { feature: "withdrawal", scope: "wild", bps: 50, flatUSD: 0, note: "unchanged (0.5%)" },
    { feature: "otc", scope: "wild", bps: 200, flatUSD: 0, note: "0.5% → 2%" },
    { feature: "swap", scope: "wild", bps: 300, flatUSD: 0, note: "0.1% → 3%" },
    { feature: "spot", scope: "wild", bps: 300, flatUSD: 0, note: "→ 3%" },
    { feature: "allswap", scope: "wild", bps: 300, flatUSD: 0, note: "NEW (3%)" },
    { feature: "instant_pay", scope: "wild", bps: 50, flatUSD: 0, note: "unchanged" },
    { feature: "instant_transfer", scope: "wild", bps: 200, flatUSD: 0, note: "unchanged" },
    { feature: "p2p", scope: "symbol", bps: p2pGas ?? 50, flatUSD: 0, note: "buyer gas (100% system, no TP)", tbd: p2pGas === undefined ? "P0-3" : undefined },
    { feature: "p2p_platform", scope: "symbol", bps: p2pPlat ?? 100, flatUSD: 0, note: "platform/TP split", tbd: p2pPlat === undefined ? "P0-3" : undefined },
  ];
}

// Expand the table to concrete (feature, token, Rate) rows.
function expand(table) {
  const rows = [];
  for (const e of table) {
    if (e.bps > 1000) throw new Error(`${e.feature}: bps ${e.bps} exceeds MAX_BPS (1000)`);
    const rate = { bps: e.bps, flatUSD: flat(e.flatUSD), mcgpExempt: true, active: true };
    if (e.scope === "wild") {
      rows.push({ feature: e.feature, token: "*", rate, tbd: e.tbd });
    } else {
      rows.push({ feature: e.feature, token: "*", rate, tbd: e.tbd }); // backend replica
      for (const sym of ESCROW_SYMBOLS) rows.push({ feature: e.feature, token: sym, rate, tbd: e.tbd });
      // MCGP: exempt / zero
      rows.push({ feature: e.feature, token: MCGP_SYMBOL, rate: { bps: 0, flatUSD: 0n, mcgpExempt: true, active: true }, tbd: e.tbd });
    }
  }
  return rows;
}

async function main() {
  const fmAddr = process.env.FEE_MANAGER_ADDRESS || readLedger(hre.network.name).FeeManager?.proxy;
  if (!fmAddr) throw new Error("seed: FeeManager address not found (deploy it or set FEE_MANAGER_ADDRESS)");
  if (!ethers.isAddress(fmAddr)) throw new Error(`seed: FeeManager address is invalid: ${fmAddr}`);
  const apply = process.env.APPLY === "1";
  const [signer] = await ethers.getSigners();
  const fm = await ethers.getContractAt("FeeManager", fmAddr);
  const owner = await fm.owner();

  const table = pmTable();
  const rows = expand(table);
  const tbds = [...new Set(rows.filter((r) => r.tbd).map((r) => r.tbd))];

  console.log("=== PM-rate seed (§2.H.3) —", apply ? "APPLY" : "DRY-RUN (diff only)", "===");
  console.log("Network:    ", network.name, "(chain", network.config.chainId + ")");
  console.log("FeeManager: ", fmAddr, "  owner:", owner);
  console.log("Signer:     ", signer.address, signer.address.toLowerCase() === owner.toLowerCase() ? "(owner ✓)" : "(NOT owner ✗)");
  console.log("Rows:       ", rows.length, "  Unresolved:", tbds.length ? tbds.join(", ") : "none");
  console.log("");
  console.log("Diff (current → proposed):");
  for (const r of rows) {
    const [cb, cf, , ca] = await fm.rateFor(r.feature, r.token);
    const cur = ca ? `${cb}bps/${cf}` : "(unset)";
    const next = `${r.rate.bps}bps/${r.rate.flatUSD}`;
    const changed = !(ca && Number(cb) === r.rate.bps && BigInt(cf) === r.rate.flatUSD);
    const key = `${r.feature}/${r.token}`.padEnd(28);
    console.log(`  ${key} ${cur.padStart(12)} → ${next.padEnd(12)} ${changed ? "CHANGE" : "same"}${r.tbd ? "  ⚠ " + r.tbd : ""}`);
  }
  console.log("");

  if (!apply) {
    console.log("DRY-RUN — no tx sent. To apply: set APPLY=1 and pin P2P_GAS_BPS / P2P_PLATFORM_BPS / PRODUCT_GAS_BPS.");
    console.log("NOTE: applying PM rates is a CUTOVER action — only after FEE_MODEL_V2_ENABLED=true (runbook §2.H.3).");
    return;
  }
  if (tbds.length) throw new Error(`Refusing to APPLY: unresolved targets ${tbds.join(", ")}. Pin them via env first.`);
  if (signer.address.toLowerCase() !== owner.toLowerCase()) throw new Error("Signer is not the FeeManager owner.");

  const features = rows.map((r) => r.feature);
  const tokens = rows.map((r) => r.token);
  const rates = rows.map((r) => r.rate);
  console.log("Sending setRates (batch of " + rows.length + ")...");
  const rcpt = await (await fm.setRates(features, tokens, rates)).wait();
  console.log("  tx:", rcpt.hash);

  let ok = 0;
  for (const r of rows) {
    const [bps, fUSD, , active] = await fm.rateFor(r.feature, r.token);
    if (Number(bps) === r.rate.bps && BigInt(fUSD) === r.rate.flatUSD && active) ok++;
  }
  if (ok !== rows.length) throw new Error(`Read-back FAILED: ${ok}/${rows.length}`);
  console.log(`All ${ok}/${rows.length} PM rows verified == intent.`);
}

main().catch((e) => { console.error(e); process.exit(1); });
