// UUPS upgrade: ProductEscrow proxy → ProductEscrowV2 implementation.
//
// Atomically runs the one-shot `initializeV2()` reinitializer via upgradeProxy's
// `call` option, so the freshly-added `legacySplitEnabled` slot is set TRUE at the
// upgrade — the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte today" guarantee.
// Without this call upgradeProxy does NOT re-run initialize and the slot reads
// default zero=false, which would SILENTLY bypass the buyer-cashback/upline/system
// split on the first confirmReceipt after upgrade (audit finding #1).
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY   - the EOA that owns the proxy (UUPS _authorizeUpgrade onlyOwner)
//   PRODUCT_ESCROW_PROXY   - the live ProductEscrow proxy address to upgrade in place
//
// Optional env:
//   CHECK_ONLY=1           - validate layout + print plan, exit before any chain tx
//
// Run:
//   PRODUCT_ESCROW_PROXY=0x... npx hardhat run scripts/upgrade-product-escrow-v2.js --network sonicMainnet

const { ethers, network, upgrades } = require("hardhat");

function readProxy() {
  const v = process.env.PRODUCT_ESCROW_PROXY;
  if (!v) throw new Error("Set PRODUCT_ESCROW_PROXY to the live ProductEscrow proxy address");
  if (!ethers.isAddress(v)) throw new Error(`Invalid PRODUCT_ESCROW_PROXY: ${v}`);
  return ethers.getAddress(v);
}

async function main() {
  const proxy = readProxy();
  const [deployer] = await ethers.getSigners();

  console.log("=== Upgrade ProductEscrow → ProductEscrowV2 ===");
  console.log("Network: ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer:", deployer.address);
  console.log("Proxy:   ", proxy);

  const V2 = await ethers.getContractFactory("ProductEscrowV2");

  await upgrades.validateUpgrade(proxy, V2, {
    unsafeAllow: ["constructor", "missing-initializer"],
  });
  console.log("Storage layout: COMPATIBLE");

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1 — layout validated; exiting before any chain tx.");
    return;
  }

  const upgraded = await upgrades.upgradeProxy(proxy, V2, {
    unsafeAllow: ["constructor", "missing-initializer"],
    call: { fn: "initializeV2" }, // atomic reinitializer: legacySplitEnabled = true
  });
  await upgraded.waitForDeployment();

  const implAddr = await upgrades.erc1967.getImplementationAddress(proxy);
  console.log("New implementation:", implAddr);
  console.log("legacySplitEnabled:", await upgraded.legacySplitEnabled(), "(expected true)");
  console.log("Done. Wire FeeManager (setFeeManager/setTokenSymbol) + seed rates BEFORE flipping legacySplitEnabled(false).");
}

main().catch((e) => { console.error(e); process.exit(1); });
