// smart-contracts/deploy/upgrade.js
const { readLedger, writeLedger } = require("./lib");

const LEDGER_KEY = {
  P2PEscrowV3: "P2PEscrowV3",
  ProductEscrowV2: "ProductEscrowV2",
  ServiceContactV2: "ServiceContactV2",
  FeeManager: "FeeManager",
};

async function upgrade(hre, { contract, proxy } = {}) {
  const { ethers, upgrades, network } = hre;
  if (!LEDGER_KEY[contract]) throw new Error(`deploy/upgrade: unknown contract '${contract}'`);
  const target = proxy || readLedger(network.name)[LEDGER_KEY[contract]]?.proxy;
  if (!target) throw new Error(`deploy/upgrade: no proxy for ${contract} on ${network.name}`);

  const Factory = await ethers.getContractFactory(contract);
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
  if (!proxy) writeLedger(network.name, LEDGER_KEY[contract], { proxy: addr, impl });
  console.log(`${contract} upgraded proxy=${addr} impl=${impl}`);
  return { proxy: addr, impl };
}

if (require.main === module) {
  const contract = process.env.UPGRADE_CONTRACT;
  upgrade(require("hardhat"), { contract }).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { upgrade };
