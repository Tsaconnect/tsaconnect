// smart-contracts/deploy/mcgp-distributor.js
const { resolveChain, requireAddr, writeLedger } = require("./lib");

async function deploy(hre, { chain } = {}) {
  const { ethers, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());
  const mcgp = requireAddr(cfg, "MCGP", cfg.tokens.MCGP);

  const Factory = await ethers.getContractFactory("MCGPRewardDistributor");
  const d = await Factory.deploy(mcgp, owner);
  await d.waitForDeployment();
  const address = await d.getAddress();
  if (!chain) writeLedger(network.name, "MCGPRewardDistributor", { address });
  console.log(`MCGPRewardDistributor address=${address} mcgp=${mcgp}`);
  return { address };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
