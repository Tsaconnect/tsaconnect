// smart-contracts/deploy/service-contact.js
const { resolveChain, writeLedger, readLedger, handOverOwnership } = require("./lib");

async function deploy(hre, { chain, feeManager, feeAmount = 0n } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();
  const finalOwner = cfg.owner || deployerAddr;

  // Deploy owned by the deployer so it can run the onlyOwner wiring; ownership
  // is transferred to finalOwner at the end.
  const Factory = await ethers.getContractFactory("ServiceContactV2");
  const sc = await upgrades.deployProxy(Factory, [deployerAddr], { kind: "uups", unsafeAllow: ["constructor"] });
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

  await handOverOwnership("ServiceContactV2", [sc], finalOwner);

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "ServiceContactV2", { proxy, impl });
  console.log(`ServiceContactV2 proxy=${proxy} impl=${impl} owner=${finalOwner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
