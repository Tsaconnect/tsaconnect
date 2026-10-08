// smart-contracts/deploy/p2p-escrow.js
const { resolveChain, writeLedger, readLedger, transferOwnershipIfNeeded } = require("./lib");

// Options (all optional; none given = the behaviour this module always had):
//   feeManager, deferOwnership, admins, tokens, systemWallet, writeLedgerKey —
//   see deploy/pool.js, the one caller that sets them.
async function deploy(hre, { chain, feeManager, deferOwnership = false, admins, tokens, systemWallet, writeLedgerKey } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();
  const finalOwner = cfg.owner || deployerAddr;
  const adminList = admins && admins.length ? admins : cfg.admins && cfg.admins.length ? cfg.admins : [finalOwner];
  const tokenMap = tokens || cfg.tokens;

  // Deploy owned by the deployer so it can run the onlyOwner wiring below;
  // ownership is handed to finalOwner at the end. (Initializing straight to a
  // separate owner would make every setter below revert — review fix.)
  const Factory = await ethers.getContractFactory("P2PEscrowV3");
  const esc = await upgrades.deployProxy(Factory, [deployerAddr, adminList], { kind: "uups" });
  await esc.waitForDeployment();
  const proxy = await esc.getAddress();

  // systemWallet is mandatory on-chain (P2PEscrowV3 reverts SystemWalletNotSet);
  // fall back to finalOwner so a deploy never ships with it unset.
  const sysWallet = systemWallet || cfg.systemWallet || finalOwner;
  await (await esc.setSystemWallet(sysWallet)).wait();
  for (const [sym, addr] of Object.entries(tokenMap)) {
    if (!addr) continue; // e.g. MCGP null on BSC
    await (await esc.setTokenAcceptance(addr, true)).wait();
    await (await esc.setTokenSymbol(addr, sym)).wait();
  }
  if (tokenMap.MCGP) await (await esc.setMcgpToken(tokenMap.MCGP)).wait();
  if (cfg.permit2) await (await esc.setPermit2(cfg.permit2)).wait();
  if (cfg.forwarder) await (await esc.setTrustedForwarder(cfg.forwarder)).wait();

  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  if (fm) await (await esc.setFeeManager(fm)).wait();

  if (!deferOwnership) await transferOwnershipIfNeeded(esc, deployerAddr, finalOwner);

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, writeLedgerKey || "P2PEscrowV3", { proxy, impl });
  console.log(`P2PEscrowV3 proxy=${proxy} impl=${impl} owner=${deferOwnership ? deployerAddr : finalOwner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
