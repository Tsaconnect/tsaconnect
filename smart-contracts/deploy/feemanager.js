// smart-contracts/deploy/feemanager.js
const { resolveChain, writeLedger } = require("./lib");

async function deploy(hre, { chain } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());

  const FeeManager = await ethers.getContractFactory("FeeManager");
  const fm = await upgrades.deployProxy(FeeManager, [owner], { kind: "uups" });
  await fm.waitForDeployment();

  const proxy = await fm.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "FeeManager", { proxy, impl });
  console.log(`FeeManager proxy=${proxy} impl=${impl} owner=${owner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
