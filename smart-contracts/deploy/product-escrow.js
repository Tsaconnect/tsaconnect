// smart-contracts/deploy/product-escrow.js
const { resolveChain, writeLedger, readLedger, transferOwnershipIfNeeded } = require("./lib");

async function deploy(hre, { chain, feeManager } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();
  const finalOwner = cfg.owner || deployerAddr;
  const admins = cfg.admins && cfg.admins.length ? cfg.admins : [finalOwner];

  // Deploy owned by the deployer so it can run the onlyOwner wiring; ownership
  // is transferred to finalOwner at the end.
  const Factory = await ethers.getContractFactory("ProductEscrowV2");
  const esc = await upgrades.deployProxy(Factory, [deployerAddr, admins], { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] });
  await esc.waitForDeployment();
  const proxy = await esc.getAddress();

  if (cfg.tokens.MCGP) await (await esc.setMcgpToken(cfg.tokens.MCGP)).wait();
  for (const [sym, addr] of Object.entries(cfg.tokens)) {
    if (!addr) continue;
    await (await esc.setTokenAcceptance(addr, true)).wait();
    await (await esc.setTokenSymbol(addr, sym)).wait();
  }
  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  if (fm) await (await esc.setFeeManager(fm)).wait();

  await transferOwnershipIfNeeded(esc, deployerAddr, finalOwner);

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "ProductEscrowV2", { proxy, impl });
  console.log(`ProductEscrowV2 proxy=${proxy} impl=${impl} owner=${finalOwner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
