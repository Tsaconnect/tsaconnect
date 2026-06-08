// UUPS upgrade: ServiceContact proxy → ServiceContactV2 implementation.
//
// Atomically runs the one-shot `initializeV2()` reinitializer via upgradeProxy's
// `call` option, so the freshly-added `legacySplitEnabled` slot is set TRUE at the
// upgrade — the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte today" guarantee.
// Without this call upgradeProxy does NOT re-run initialize and the slot reads
// default zero=false, which would SILENTLY bypass the provider/caller/upline 4-way
// split on the first payContactFee after upgrade (audit finding #1).
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY    - the EOA that owns the proxy (UUPS _authorizeUpgrade onlyOwner)
//   SERVICE_CONTACT_PROXY   - the live ServiceContact proxy address to upgrade in place
//
// Optional env:
//   CHECK_ONLY=1            - validate layout + print plan, exit before any chain tx
//
// Run:
//   SERVICE_CONTACT_PROXY=0x... npx hardhat run scripts/upgrade-service-contact-v2.js --network sonicMainnet

const { ethers, network, upgrades } = require("hardhat");

function readProxy() {
  const v = process.env.SERVICE_CONTACT_PROXY;
  if (!v) throw new Error("Set SERVICE_CONTACT_PROXY to the live ServiceContact proxy address");
  if (!ethers.isAddress(v)) throw new Error(`Invalid SERVICE_CONTACT_PROXY: ${v}`);
  return ethers.getAddress(v);
}

async function main() {
  const proxy = readProxy();
  const [deployer] = await ethers.getSigners();

  console.log("=== Upgrade ServiceContact → ServiceContactV2 ===");
  console.log("Network: ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer:", deployer.address);
  console.log("Proxy:   ", proxy);

  const V2 = await ethers.getContractFactory("ServiceContactV2");

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
  console.log("Done. NOTE: feeAmount stays > 0 (I2) — disable the split via setLegacySplitEnabled(false), never by zeroing the fee.");
}

main().catch((e) => { console.error(e); process.exit(1); });
