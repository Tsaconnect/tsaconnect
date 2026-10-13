// smart-contracts/deploy/upgrade.js
//
//   UPGRADE_CONTRACT=FeeManager npx hardhat run deploy/upgrade.js --network sonicMainnet
//   UPGRADE_CONTRACT=P2PEscrowV3 UPGRADE_DET_NAME=direct-p2p:v1 npx hardhat run deploy/upgrade.js --network bscMainnet
//
// The ledger key P2PEscrowV3 is the MAIN escrow. A deterministic escrow
// (deploy/deterministic-escrow.js) is targeted only by UPGRADE_DET_NAME, which
// resolves its proxy from the committed deploy/deterministic.json for this
// network class and chain id. Whoever signs must own the proxy (the PM after
// handover).
const { readLedger, writeLedger } = require("./lib");

const LEDGER_KEY = {
  P2PEscrowV3: "P2PEscrowV3",
  ProductEscrowV2: "ProductEscrowV2",
  ServiceContactV2: "ServiceContactV2",
  FeeManager: "FeeManager",
  // Without an entry here `upgrade` throws on an unknown contract, so a UUPS
  // contract absent from this map cannot actually be upgraded.
  McgpEntitlementSwapV2: "McgpEntitlementSwapV2",
};

// Options: contract (UPGRADE_CONTRACT), proxy (explicit address, ledger left
// alone), detName (deterministic proxy by DET_NAME, ledger left alone),
// manifestPath (tests), factory (tests: a ContractFactory to upgrade to).
async function upgrade(hre, { contract, proxy, detName, manifestPath, factory } = {}) {
  const { ethers, upgrades, network } = hre;
  if (!LEDGER_KEY[contract]) throw new Error(`deploy/upgrade: unknown contract '${contract}'`);
  let target = proxy;
  if (detName) {
    const det = require("./deterministic-escrow");
    if (contract !== det.CONTRACT) throw new Error(`deploy/upgrade: UPGRADE_DET_NAME only applies to ${det.CONTRACT}`);
    if (proxy) throw new Error("deploy/upgrade: give a proxy address or UPGRADE_DET_NAME, not both");
    target = await det.recordedProxy(hre, detName, manifestPath);
  }
  const fromLedger = !target;
  target = target || readLedger(network.name)[LEDGER_KEY[contract]]?.proxy;
  if (!target) throw new Error(`deploy/upgrade: no proxy for ${contract} on ${network.name}`);

  const Factory = factory || (await ethers.getContractFactory(contract));
  // unsafeAllow: ["constructor"] is required for ProductEscrowV2 and ServiceContactV2
  // (they inherit non-upgradeable ReentrancyGuard whose constructor OZ flags as unsafe).
  // It is harmless for FeeManager and P2PEscrowV3, so we apply it unconditionally.
  const upgraded = await upgrades.upgradeProxy(target, Factory, {
    kind: "uups",
    unsafeAllow: ["constructor"],
  });
  await upgraded.waitForDeployment();
  const addr = await upgraded.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(addr);
  if (fromLedger) writeLedger(network.name, LEDGER_KEY[contract], { proxy: addr, impl });
  console.log(`${contract}${detName ? ` (${detName})` : ""} upgraded proxy=${addr} impl=${impl}`);
  return { proxy: addr, impl };
}

if (require.main === module) {
  const contract = process.env.UPGRADE_CONTRACT;
  const detName = process.env.UPGRADE_DET_NAME || undefined;
  upgrade(require("hardhat"), { contract, detName }).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { upgrade };
