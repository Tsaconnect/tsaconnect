// smart-contracts/scripts/upgrade-liquidity-pool.js
//
// Upgrades a deployed TsaLiquidityPool proxy IN PLACE (UUPS) to the current
// contracts/TsaLiquidityPool.sol — the no-collateral model, spec 2026-10-14.
// Storage is append-only; the OZ plugin validates the new layout against the
// proxy's current implementation (from .openzeppelin/<network>.json) before
// anything is sent. Signed by DEPLOYER_PRIVATE_KEY, which must be the pool's
// owner (upgradeToAndCall is owner-only).
//
//   PROXY=0x… [CHECK_ONLY=1] npx hardhat run scripts/upgrade-liquidity-pool.js --network bscTestnet
//   PROXY=0xA786… UPGRADE_CONFIRM=0xA786… npx hardhat run scripts/upgrade-liquidity-pool.js --network bscMainnet
//
// On a mainnet chain the run is refused unless UPGRADE_CONFIRM equals PROXY.
// CHECK_ONLY=1 validates the layout and the signer, then stops.
// Fork rehearsal (local only): FORK_RPC=<bsc rpc> PROXY=0xA786… npx hardhat run
// scripts/upgrade-liquidity-pool.js — on the forked in-process network the
// owner is impersonated, so no key is needed.
const hre = require("hardhat");
const { ethers, upgrades, network } = hre;

const MAINNETS = new Set([56n, 146n]);

async function snapshot(pool) {
  const out = {};
  for (const k of ["owner", "token", "collateralProvider", "guardian", "totalPrincipal", "outstanding", "collateral", "accEarningsPerShare", "maxFillPerTx", "fillWindowCap", "maxSpreadBps", "withdrawLock"]) {
    out[k] = String(await pool[k]());
  }
  return out;
}

async function main() {
  const proxy = process.env.PROXY;
  if (!proxy || !ethers.isAddress(proxy)) throw new Error("set PROXY to the pool's proxy address");
  const proxyAddr = ethers.getAddress(proxy);
  const { chainId } = await ethers.provider.getNetwork();
  const forked = network.name === "hardhat" && !!process.env.FORK_RPC;
  // Calls at the fork block itself are "historical" and need the remote
  // chain's hardfork table; one local block moves execution onto local state.
  if (forked) await network.provider.send("evm_mine");

  if (MAINNETS.has(chainId)) {
    const confirm = process.env.UPGRADE_CONFIRM;
    if (!confirm || !ethers.isAddress(confirm) || ethers.getAddress(confirm) !== proxyAddr) {
      throw new Error(`chain ${chainId} is a mainnet: set UPGRADE_CONFIRM to the proxy address (${proxyAddr}) to proceed`);
    }
  }
  if ((await ethers.provider.getCode(proxyAddr)) === "0x") throw new Error(`no contract at ${proxyAddr} on chain ${chainId}`);

  const current = await ethers.getContractAt("TsaLiquidityPool", proxyAddr);
  // The no-collateral upgrade removes withdrawCollateral/settleFromCollateral:
  // posted collateral would be stranded and old outstanding would silently
  // become LP-backed debt. Only an empty pool's books are upgraded unless
  // the operator says otherwise (review 2026-10-15).
  const col = await current.collateral(), out = await current.outstanding();
  if ((col > 0n || out > 0n) && process.env.UPGRADE_ALLOW_COLLATERAL_STATE !== "1") {
    throw new Error(`collateral ${col} / outstanding ${out} not zero: withdraw collateral and refill first, or set UPGRADE_ALLOW_COLLATERAL_STATE=1`);
  }
  const owner = ethers.getAddress(await current.owner());
  let signer;
  if (forked) {
    await network.provider.request({ method: "hardhat_impersonateAccount", params: [owner] });
    await network.provider.send("hardhat_setBalance", [owner, "0x8AC7230489E80000"]); // 10 native for gas
    signer = await ethers.getSigner(owner);
  } else {
    [signer] = await ethers.getSigners();
    if (!signer) throw new Error("no signer: set DEPLOYER_PRIVATE_KEY to the pool owner's key");
  }
  const signerAddr = ethers.getAddress(await signer.getAddress());
  if (signerAddr !== owner) throw new Error(`signer ${signerAddr} is not the pool owner ${owner}`);

  const New = await ethers.getContractFactory("TsaLiquidityPool", signer);
  const oldImpl = await upgrades.erc1967.getImplementationAddress(proxyAddr);
  console.log(`liquidity-pool upgrade: chain ${chainId} (${network.name}) proxy ${proxyAddr}`);
  console.log(`  owner/signer   ${owner}`);
  console.log(`  old impl       ${oldImpl}`);

  // Throws on any incompatible storage change against the proxy's current
  // implementation as recorded in the network's manifest.
  await upgrades.validateUpgrade(proxyAddr, New, { kind: "uups" });
  console.log("  storage layout compatible (validateUpgrade passed)");
  if (process.env.CHECK_ONLY === "1") {
    console.log("  CHECK_ONLY=1: nothing sent");
    return;
  }

  const before = await snapshot(current);
  const upgraded = await upgrades.upgradeProxy(proxyAddr, New, { kind: "uups" });
  await upgraded.waitForDeployment();
  const newImpl = await upgrades.erc1967.getImplementationAddress(proxyAddr);
  if (newImpl.toLowerCase() === oldImpl.toLowerCase()) throw new Error("implementation did not change");

  const after = await snapshot(upgraded);
  for (const k of Object.keys(before)) {
    if (before[k] !== after[k]) throw new Error(`state changed across the upgrade: ${k} ${before[k]} -> ${after[k]}`);
  }
  console.log(`  new impl       ${newImpl}`);
  console.log(`  liquid ${await upgraded.liquid()}  totalPendingWithdrawals ${await upgraded.totalPendingWithdrawals()}  outstanding ${after.outstanding}  totalPrincipal ${after.totalPrincipal}`);
  console.log("liquidity-pool upgrade: done, state unchanged");
}

main().catch((e) => { console.error(e); process.exit(1); });
