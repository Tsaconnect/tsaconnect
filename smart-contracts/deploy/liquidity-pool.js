// smart-contracts/deploy/liquidity-pool.js
//
// TsaLiquidityPool (spec 2026-10-14) on any chain whose deploy/chains.js entry
// has a `liquidityPool` block: the UUPS proxy, every initialize() value read
// back and asserted, then ownership handed to the owner's wallet — the
// deployer never keeps ownership past a successful run. No timelock while
// the product is in development (owner 2026-10-14); the market-ready
// contracts will not be upgradeable at all.
//
//   LPOOL_OWNER_CONFIRM=0x…  (type the owner again)
//   [LPOOL_COLLATERAL_PROVIDER, LPOOL_GUARDIAN, LPOOL_OWNER default to
//    the owner's wallet (owner, 2026-10-14: the owner does all three);
//    LPOOL_OPERATOR, LPOOL_RECIPIENT to the consolidated operator/treasury.
//    More operators: the collateral provider calls addOperator directly.]
//     npx hardhat run deploy/liquidity-pool.js --network bscMainnet
const { getAddress } = require("ethers");
const { resolveChain, assertOwnerConfirm, handOverOwnership, writeLedger } = require("./lib");

async function assertPool(pool, want) {
  const fail = (m) => { throw new Error(`liquidity-pool: assertion failed — ${m}`); };
  const same = (a, b) => getAddress(a) === getAddress(b);
  if (!same(await pool.token(), want.token)) fail("token");
  if (!same(await pool.collateralProvider(), want.collateralProvider)) fail("collateralProvider");
  if (!(await pool.operators(want.operator))) fail(`operator ${want.operator} not set`);
  if (!same(await pool.guardian(), want.guardian)) fail("guardian");
  if (!(await pool.recipient(want.recipient))) fail(`recipient ${want.recipient} not approved`);
  for (const k of ["minDeposit", "maxPerLp", "totalCap", "maxFillPerTx", "fillWindow", "fillWindowCap", "maxSpreadBps", "withdrawLock"]) {
    if ((await pool[k]()) !== BigInt(want[k])) fail(`${k} is not ${want[k]}`);
  }
  if ((await pool.totalPrincipal()) !== 0n || (await pool.collateral()) !== 0n) fail("fresh pool is not empty");
}

async function deploy(hre, { chain } = {}) {
  const { ethers, upgrades } = hre;
  const cfg = chain || resolveChain(hre);
  const lp = cfg.liquidityPool;
  if (!lp) throw new Error(`liquidity-pool: network ${hre.network.name} has no liquidityPool block in deploy/chains.js`);
  for (const k of ["collateralProvider", "operator", "guardian", "recipient", "owner"]) {
    if (!lp[k]) throw new Error(`liquidity-pool: ${k} is required`);
  }
  const tokenAddr = cfg.tokens[lp.token];
  if (!tokenAddr) throw new Error(`liquidity-pool: token ${lp.token} has no address on this network`);

  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();
  assertOwnerConfirm({
    label: "liquidity-pool", owner: lp.owner, confirm: lp.ownerConfirm, deployerAddr,
    ownerVar: "LPOOL_OWNER", confirmVar: "LPOOL_OWNER_CONFIRM",
  });

  const decimals = Number(await (await ethers.getContractAt("MockERC20", tokenAddr)).decimals());
  const units = (n) => ethers.parseUnits(String(n), decimals);
  const params = {
    token: tokenAddr,
    owner: deployerAddr, // handed to the owner below, after the assertions
    collateralProvider: lp.collateralProvider,
    operator: lp.operator,
    guardian: lp.guardian,
    recipient: lp.recipient,
    minDeposit: units(lp.minDeposit),
    maxPerLp: units(lp.maxPerLp),
    totalCap: units(lp.totalCap),
    maxFillPerTx: units(lp.maxFillPerTx),
    fillWindow: lp.fillWindowSec,
    fillWindowCap: units(lp.fillWindowCap),
    maxSpreadBps: lp.maxSpreadBps,
    withdrawLock: lp.withdrawLockSec,
  };

  const Pool = await ethers.getContractFactory("TsaLiquidityPool");
  const pool = await upgrades.deployProxy(Pool, [params], { kind: "uups" });
  await pool.waitForDeployment();
  const proxy = await pool.getAddress();
  console.log(`liquidity-pool: proxy ${proxy}`);
  try {
    await assertPool(pool, params);
  } catch (e) {
    e.deployed = { pool: proxy };
    console.error(`liquidity-pool: NOT handed over — still owned by the deployer ${deployerAddr}. pool=${proxy}`);
    throw e;
  }

  await handOverOwnership("liquidity-pool", [pool], lp.owner);
  if (!chain) writeLedger(hre.network.name, "TsaLiquidityPool", { proxy, owner: lp.owner });
  console.log(`liquidity-pool: ready pool=${proxy} owner=${lp.owner}`);
  return { pool: proxy };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy, assertPool };
