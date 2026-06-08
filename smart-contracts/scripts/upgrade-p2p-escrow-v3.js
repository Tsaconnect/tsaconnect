// UUPS upgrade: P2PEscrowV2 proxy → P2PEscrowV3 implementation.
//
// Atomically runs the one-shot `initializeV3()` reinitializer via upgradeProxy's
// `call` option, so the freshly-added `legacySplitEnabled` slot is set TRUE at the
// upgrade — the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte today" guarantee.
// Without this call upgradeProxy does NOT re-run initialize and the slot reads
// default zero=false (audit finding #1).
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY   - the EOA that owns the proxy (UUPS _authorizeUpgrade onlyOwner)
//   P2P_ESCROW_PROXY       - the live P2PEscrowV2 proxy address to upgrade in place
//
// Optional env:
//   CHECK_ONLY=1           - validate layout + print plan, exit before any chain tx
//
// Run:
//   P2P_ESCROW_PROXY=0x... npx hardhat run scripts/upgrade-p2p-escrow-v3.js --network sonicMainnet

const { ethers, network, upgrades } = require("hardhat");

function readProxy() {
  const v = process.env.P2P_ESCROW_PROXY;
  if (!v) throw new Error("Set P2P_ESCROW_PROXY to the live P2PEscrowV2 proxy address");
  if (!ethers.isAddress(v)) throw new Error(`Invalid P2P_ESCROW_PROXY: ${v}`);
  return ethers.getAddress(v);
}

async function main() {
  const proxy = readProxy();
  const [deployer] = await ethers.getSigners();

  console.log("=== Upgrade P2PEscrowV2 → P2PEscrowV3 ===");
  console.log("Network: ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer:", deployer.address);
  console.log("Proxy:   ", proxy);

  const V3 = await ethers.getContractFactory("P2PEscrowV3");

  // Always validate storage-layout compatibility before touching the chain.
  await upgrades.validateUpgrade(proxy, V3, {
    unsafeAllow: ["constructor", "missing-initializer"],
  });
  console.log("Storage layout: COMPATIBLE");

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1 — layout validated; exiting before any chain tx.");
    return;
  }

  const upgraded = await upgrades.upgradeProxy(proxy, V3, {
    unsafeAllow: ["constructor", "missing-initializer"],
    call: { fn: "initializeV3" }, // atomic reinitializer: legacySplitEnabled = true
  });
  await upgraded.waitForDeployment();

  const implAddr = await upgrades.erc1967.getImplementationAddress(proxy);
  console.log("New implementation:", implAddr);
  console.log("legacySplitEnabled:", await upgraded.legacySplitEnabled(), "(expected true)");
  console.log("Done. Seed FeeManager rates + setTokenSymbol BEFORE flipping legacySplitEnabled(false) at cutover.");
}

main().catch((e) => { console.error(e); process.exit(1); });
