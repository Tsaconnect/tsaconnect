// smart-contracts/deploy/liquidity-pool.js
//
// TsaLiquidityPool (spec 2026-10-14) on any chain whose deploy/chains.js entry
// has a `liquidityPool` block: the UUPS proxy, every initialize() value read
// back and asserted, then a TimelockController deployed and made the owner
// (spec O1) — the deployer never keeps ownership past a successful run.
//
//   LPOOL_TIMELOCK_ADMIN_CONFIRM=0x…  (type the timelock admin again: it is final)
//   [LPOOL_COLLATERAL_PROVIDER, LPOOL_GUARDIAN, LPOOL_TIMELOCK_ADMIN default to
//    the owner's wallet (owner, 2026-10-14: the owner does all three);
//    LPOOL_OPERATOR, LPOOL_RECIPIENT to the consolidated operator/treasury.
//    More operators: the collateral provider calls addOperator directly.]
//     npx hardhat run deploy/liquidity-pool.js --network bscMainnet
const { getAddress } = require("ethers");
const { resolveChain, assertOwnerConfirm, handOverOwnership, writeLedger } = require("./lib");

const MIN_MAINNET_TIMELOCK_SEC = 72 * 3600;

async function assertPool(pool, want) {
  const fail = (m) => { throw new Error(`liquidity-pool: assertion failed — ${m}`); };
  const same = (a, b) => getAddress(a) === getAddress(b);
  if (!same(await pool.token(), want.token)) fail("token");
  if (!same(await pool.collateralProvider(), want.collateralProvider)) fail("collateralProvider");
  if (!(await pool.operators(want.operator))) fail(`operator ${want.operator} not set`);
  if (!same(await pool.guardian(), want.guardian)) fail("guardian");
  if (!(await pool.recipient(want.recipient))) fail(`recipient ${want.recipient} not approved`);
  for (const k of ["minDeposit", "maxPerLp", "totalCap", "maxFillPerTx", "fillWindow", "fillWindowCap", "maxSpreadBps"]) {
    if ((await pool[k]()) !== BigInt(want[k])) fail(`${k} is not ${want[k]}`);
  }
  if ((await pool.totalPrincipal()) !== 0n || (await pool.collateral()) !== 0n) fail("fresh pool is not empty");
}

async function deploy(hre, { chain } = {}) {
  const { ethers, upgrades } = hre;
  const cfg = chain || resolveChain(hre);
  const lp = cfg.liquidityPool;
  if (!lp) throw new Error(`liquidity-pool: network ${hre.network.name} has no liquidityPool block in deploy/chains.js`);
  for (const k of ["collateralProvider", "operator", "guardian", "recipient", "timelockAdmin"]) {
    if (!lp[k]) throw new Error(`liquidity-pool: ${k} is required`);
  }
  // The timelock is what lets LPs exit before an upgrade lands: never shorter
  // than 72 h on a mainnet (review 2026-10-14).
  if (/mainnet/i.test(hre.network.name) && lp.timelockDelaySec < MIN_MAINNET_TIMELOCK_SEC) {
    throw new Error(`liquidity-pool: timelock delay ${lp.timelockDelaySec}s is under the 72h mainnet minimum`);
  }
  const tokenAddr = cfg.tokens[lp.token];
  if (!tokenAddr) throw new Error(`liquidity-pool: token ${lp.token} has no address on this network`);

  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();
  assertOwnerConfirm({
    label: "liquidity-pool", owner: lp.timelockAdmin, confirm: lp.timelockAdminConfirm, deployerAddr,
    ownerVar: "LPOOL_TIMELOCK_ADMIN", confirmVar: "LPOOL_TIMELOCK_ADMIN_CONFIRM",
  });

  const decimals = Number(await (await ethers.getContractAt("MockERC20", tokenAddr)).decimals());
  const units = (n) => ethers.parseUnits(String(n), decimals);
  const params = {
    token: tokenAddr,
    owner: deployerAddr, // handed to the timelock below, after the assertions
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

  const Timelock = await ethers.getContractFactory("TimelockController");
  const timelock = await Timelock.deploy(lp.timelockDelaySec, [lp.timelockAdmin], [lp.timelockAdmin], ethers.ZeroAddress);
  await timelock.waitForDeployment();
  const timelockAddr = await timelock.getAddress();
  if ((await timelock.getMinDelay()) !== BigInt(lp.timelockDelaySec)) throw new Error("liquidity-pool: timelock delay read-back mismatch");

  await handOverOwnership("liquidity-pool", [pool], timelockAddr);
  if (!chain) writeLedger(hre.network.name, "TsaLiquidityPool", { proxy, timelock: timelockAddr });
  console.log(`liquidity-pool: ready pool=${proxy} timelock=${timelockAddr} delay=${lp.timelockDelaySec}s admin=${lp.timelockAdmin}`);
  return { pool: proxy, timelock: timelockAddr };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy, assertPool };
