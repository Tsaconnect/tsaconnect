// smart-contracts/deploy/feemanager.js
const { resolveChain, writeLedger } = require("./lib");

// Options (all optional; none given = the behaviour this module always had):
//   deferOwnership — initialise owned by the deployer so a caller can wire it
//                    first and transfer last;
//   writeLedgerKey — the ledger key written on the CLI path (default "FeeManager");
//   signer         — deploy (and, with deferOwnership, own) with this signer
//                    instead of the routine hardhat-config one
//                    (deploy/p2p-direct.js: the cold deploy key);
//   writeLedger    — false leaves the ledger to the caller, who records the
//                    proxy only once its whole run has succeeded.
async function deploy(hre, { chain, deferOwnership = false, writeLedgerKey, signer, writeLedger: ledger = true } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const deployer = signer || (await ethers.getSigners())[0];
  const deployerAddr = await deployer.getAddress();
  const owner = deferOwnership ? deployerAddr : cfg.owner || deployerAddr;

  const FeeManager = await ethers.getContractFactory("FeeManager", deployer);
  const fm = await upgrades.deployProxy(FeeManager, [owner], { kind: "uups" });
  await fm.waitForDeployment();

  const proxy = await fm.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain && ledger) writeLedger(network.name, writeLedgerKey || "FeeManager", { proxy, impl });
  console.log(`FeeManager proxy=${proxy} impl=${impl} owner=${owner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
