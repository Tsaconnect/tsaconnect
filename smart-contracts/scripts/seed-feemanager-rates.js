// Seeds a freshly-deployed FeeManager with the CURRENT production fee rates so
// that wiring the escrows to it changes NO fee math (runbook §2.B / §3.B,
// invariant #3 "seed current rates first").
//
// The rate table below is a byte-for-byte mirror of the backend's canonical
// DefaultFeeConfigs (tsa-api-go/internal/services/fee_config_service.go). Token
// is the wildcard "*" (models.FeeTokenAny). FlatUSD is in 1e6 micro-units on
// chain (FeeManager.Rate.flatUSD), e.g. $0.10 -> 100000.
//
// Owner-only (setRates). On mainnet this is a Safe-signed tx; on testnet the
// deployer EOA owner signs. After writing, every row is read back via rateFor
// and asserted equal — the same verify-then-trust the admin /fees/confirm uses.
//
// Run:
//   FEE_MANAGER_ADDRESS=0x... npx hardhat run scripts/seed-feemanager-rates.js --network sonicTestnet
//   CHECK_ONLY=1 FEE_MANAGER_ADDRESS=0x... npx hardhat run scripts/seed-feemanager-rates.js --network sonicTestnet

const { ethers, network } = require("hardhat");

const TOKEN_ANY = "*";
const flat = (usd) => BigInt(Math.round(usd * 1e6)); // human USD -> 1e6 micros

// (feature, bps, flatUSD, mcgpExempt) — mirrors DefaultFeeConfigs. active=true all.
const CURRENT_RATES = [
  ["product_merchant", 200, 0.0, true],
  ["product", 0, 0.1, true],
  ["service_contact", 0, 0.1, true],
  ["withdrawal", 50, 0.0, false],
  ["swap", 10, 0.0, false],
  ["service_other", 50, 0.0, false],
  ["otc", 50, 0.0, false],
  ["p2p", 50, 0.0, false],
  ["p2p_platform", 100, 0.0, false],
  ["spot", 10, 0.0, false],
  ["instant_pay", 50, 0.0, false],
  ["instant_transfer", 200, 0.0, false],
];

async function main() {
  const fmAddr = process.env.FEE_MANAGER_ADDRESS;
  if (!fmAddr || !ethers.isAddress(fmAddr)) {
    throw new Error("Set FEE_MANAGER_ADDRESS to the deployed FeeManager proxy.");
  }
  const checkOnly = process.env.CHECK_ONLY === "1";
  const [signer] = await ethers.getSigners();
  const fm = await ethers.getContractAt("FeeManager", fmAddr);

  const owner = await fm.owner();
  console.log("=== Seed FeeManager CURRENT rates ===");
  console.log("Network:    ", network.name, "(chain", network.config.chainId + ")");
  console.log("FeeManager: ", fmAddr);
  console.log("Owner:      ", owner);
  console.log("Signer:     ", signer.address, signer.address.toLowerCase() === owner.toLowerCase() ? "(owner ✓)" : "(NOT owner ✗)");
  console.log("Mode:       ", checkOnly ? "CHECK_ONLY (dry-run)" : "WRITE");
  console.log("");

  const features = [];
  const tokens = [];
  const rates = [];
  console.log("Planned rows:");
  for (const [feature, bps, flatUSD, mcgpExempt] of CURRENT_RATES) {
    const r = { bps, flatUSD: flat(flatUSD), mcgpExempt, active: true };
    features.push(feature);
    tokens.push(TOKEN_ANY);
    rates.push(r);
    console.log(`  ${feature.padEnd(18)} bps=${String(bps).padStart(3)} flatUSD=${r.flatUSD.toString().padStart(7)} mcgpExempt=${mcgpExempt}`);
  }

  if (checkOnly) {
    console.log("");
    console.log("CHECK_ONLY=1 — not writing. Exiting.");
    return;
  }

  console.log("");
  console.log("Sending setRates (batch)...");
  const tx = await fm.setRates(features, tokens, rates);
  const rcpt = await tx.wait();
  console.log("  tx:", rcpt.hash);

  console.log("");
  console.log("Read-back verify (rateFor):");
  let ok = 0;
  for (let i = 0; i < features.length; i++) {
    const [bps, flatUSD, mcgpExempt, active] = await fm.rateFor(features[i], tokens[i]);
    const want = rates[i];
    const match =
      Number(bps) === want.bps &&
      BigInt(flatUSD) === want.flatUSD &&
      mcgpExempt === want.mcgpExempt &&
      active === want.active;
    console.log(`  ${features[i].padEnd(18)} ${match ? "OK" : "MISMATCH"}  (bps=${bps} flat=${flatUSD} exempt=${mcgpExempt} active=${active})`);
    if (match) ok++;
  }
  console.log("");
  if (ok !== features.length) {
    throw new Error(`Read-back FAILED: ${ok}/${features.length} rows match.`);
  }
  console.log(`All ${ok}/${features.length} rows verified == intent. FeeManager seeded at CURRENT rates.`);
  console.log("Next: wire escrows (setFeeManager + setTokenSymbol). Do NOT apply PM rates until the flag is ON (runbook §2.H.3).");
}

main().catch((e) => { console.error(e); process.exit(1); });
