// smart-contracts/deploy/service-contact.js
const { resolveChain, writeLedger, readLedger } = require("./lib");

async function deploy(hre, { chain, feeManager, feeAmount = 0n } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());

  const Factory = await ethers.getContractFactory("ServiceContactV2");
  const sc = await upgrades.deployProxy(Factory, [owner], { kind: "uups", unsafeAllow: ["constructor"] });
  await sc.waitForDeployment();
  const proxy = await sc.getAddress();

  for (const [sym, addr] of Object.entries(cfg.tokens)) {
    if (!addr) continue;
    await (await sc.setTokenAcceptance(addr, true)).wait();
    await (await sc.setTokenSymbol(addr, sym)).wait();
    if (feeAmount > 0n) await (await sc.setFeeAmount(addr, feeAmount)).wait();
  }
  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  if (fm) await (await sc.setFeeManager(fm)).wait();

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "ServiceContactV2", { proxy, impl });
  console.log(`ServiceContactV2 proxy=${proxy} impl=${impl}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
