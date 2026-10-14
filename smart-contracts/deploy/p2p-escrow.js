// smart-contracts/deploy/p2p-escrow.js
const { getAddress } = require("ethers");
const { resolveChain, writeLedger, readLedger, handOverOwnership } = require("./lib");

const sameAddr = (a, b) => getAddress(a) === getAddress(b);

// wireEscrow runs the onlyOwner per-chain setup on a P2PEscrowV3 the caller
// owns: system wallet, accepted tokens + their FeeManager symbols, MCGP, Permit2,
// forwarder and FeeManager. Each value is read first and its setter is sent
// only when it differs, so re-running on a partly-wired proxy resends nothing
// already done (deploy/deterministic-escrow.js resumes this way). On a fresh
// proxy every value is unset, so the txs are exactly the setter list.
async function wireEscrow(esc, { cfg, tokens, systemWallet, feeManager }) {
  const send = async (tx) => (await tx).wait();
  if (!sameAddr(await esc.systemWallet(), systemWallet)) await send(esc.setSystemWallet(systemWallet));
  for (const [sym, addr] of Object.entries(tokens)) {
    if (!addr) continue; // e.g. MCGP null on BSC
    if (!(await esc.acceptedTokens(addr))) await send(esc.setTokenAcceptance(addr, true));
    if ((await esc.tokenSymbol(addr)) !== sym) await send(esc.setTokenSymbol(addr, sym));
  }
  if (tokens.MCGP && !sameAddr(await esc.mcgpToken(), tokens.MCGP)) await send(esc.setMcgpToken(tokens.MCGP));
  if (cfg.permit2 && !sameAddr(await esc.permit2(), cfg.permit2)) await send(esc.setPermit2(cfg.permit2));
  if (cfg.forwarder && !sameAddr(await esc.trustedForwarder(), cfg.forwarder)) await send(esc.setTrustedForwarder(cfg.forwarder));
  if (feeManager && !sameAddr(await esc.feeManager(), feeManager)) await send(esc.setFeeManager(feeManager));
}

// Options (all optional; none given = the behaviour this module always had):
//   feeManager, deferOwnership, admins, tokens, systemWallet, writeLedgerKey —
//   a caller that wires more before the handover sets them.
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
  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  await wireEscrow(esc, { cfg, tokens: tokenMap, systemWallet: sysWallet, feeManager: fm });

  if (!deferOwnership) await handOverOwnership("P2PEscrowV3", [esc], finalOwner);

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, writeLedgerKey || "P2PEscrowV3", { proxy, impl });
  console.log(`P2PEscrowV3 proxy=${proxy} impl=${impl} owner=${deferOwnership ? deployerAddr : finalOwner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy, wireEscrow };
