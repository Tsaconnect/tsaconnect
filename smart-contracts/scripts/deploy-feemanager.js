// Deploys FeeManager — the on-chain SOURCE OF TRUTH (write side / CQRS) for every
// fee rate, one instance per chain. See:
//   docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §3
//   docs/superpowers/runbooks/2026-06-09-fee-v2-cutover-runbook.md §2.A / §3.A
//
// UUPS proxy. `initialize(initialOwner)` sets the owner, which is BOTH the rate-write
// authority (onlyOwner setRate/setRates) AND the UUPS upgrade authority. On mainnet
// the owner MUST be the Gnosis Safe (runbook P0-5); on testnet a test EOA is allowed.
//
// Run:
//   # dry-run (validates inputs + UUPS layout, no tx)
//   CHECK_ONLY=1 OWNER_ADDRESS=0x... npx hardhat run scripts/deploy-feemanager.js --network sonicTestnet
//   # real
//   OWNER_ADDRESS=0x... npx hardhat run scripts/deploy-feemanager.js --network sonicTestnet
//
// Required env:
//   DEPLOYER_PRIVATE_KEY  — the EOA that pays for + sends the deploy tx
// Optional env:
//   OWNER_ADDRESS         — FeeManager owner (rate writes + upgrades). Default: deployer.
//                           MAINNET: set this to the Gnosis Safe.
//   CHECK_ONLY=1          — validate inputs + UUPS upgrade-safety, do not deploy.

const { ethers, network, upgrades } = require("hardhat");

function isAddress(a) {
  try {
    return !!a && ethers.getAddress(a) === ethers.getAddress(a);
  } catch {
    return false;
  }
}

async function main() {
  const [deployer] = await ethers.getSigners();

  const owner = process.env.OWNER_ADDRESS || deployer.address;
  const checkOnly = process.env.CHECK_ONLY === "1";

  if (!isAddress(owner)) {
    throw new Error(`OWNER_ADDRESS must be a valid address (got '${owner}')`);
  }
  if (ethers.getAddress(owner) === ethers.ZeroAddress) {
    throw new Error("OWNER_ADDRESS must be non-zero.");
  }

  console.log("=== Deploy FeeManager (UUPS) ===");
  console.log("Network:  ", network.name, "(chain", (network.config.chainId ?? "?") + ")");
  console.log("Deployer: ", deployer.address);
  console.log("Owner:    ", owner, owner === deployer.address ? "(deployer — testnet/EOA)" : "");
  console.log("Mode:     ", checkOnly ? "CHECK_ONLY (dry-run)" : "DEPLOY");
  console.log("");

  const FeeManager = await ethers.getContractFactory("FeeManager");

  // Always validate UUPS upgrade-safety (initializer, no constructor state, layout).
  await upgrades.validateImplementation(FeeManager, { kind: "uups" });
  console.log("Implementation: UUPS-safe");

  if (checkOnly) {
    console.log("CHECK_ONLY=1 — inputs valid + implementation UUPS-safe; not deploying. Exiting.");
    return;
  }

  const fm = await upgrades.deployProxy(FeeManager, [owner], { kind: "uups" });
  await fm.waitForDeployment();
  const proxyAddr = await fm.getAddress();
  const implAddr = await upgrades.erc1967.getImplementationAddress(proxyAddr);

  console.log("");
  console.log("Deployed FeeManager:");
  console.log("  Proxy:          ", proxyAddr);
  console.log("  Implementation: ", implAddr);
  console.log("  owner():        ", await fm.owner());
  console.log("  MAX_BPS:        ", (await fm.MAX_BPS()).toString(), "(10% on-chain fat-finger ceiling)");

  console.log("");
  console.log("=== Next steps (runbook §2.B / §3.B) ===");
  console.log("  1. Seed CURRENT rates via setRates (owner-signed) — zero behavior change.");
  console.log("  2. Wire each escrow: setFeeManager(" + proxyAddr + ") + setTokenSymbol per token.");
  console.log("  3. Backend env: FEE_MANAGER_ADDRESS_SONIC=" + proxyAddr + " (FEE_MODEL_V2_ENABLED stays false).");
  console.log("");
  console.log("Record this as FM_" + network.name + ":");
  console.log("FEE_MANAGER_ADDRESS_SONIC=" + proxyAddr);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
