const { expect } = require("chai");
const hre = require("hardhat");
const { ethers } = hre;
const { deploy } = require("../../deploy/liquidity-pool");

async function fixture(over = {}) {
  const [deployer, provider, operator, guardian, treasury, admin] = await ethers.getSigners();
  const MockERC20 = await ethers.getContractFactory("MockERC20");
  const usdt = await MockERC20.deploy("Tether USD", "USDT", 18);
  const chain = {
    tokens: { USDT: await usdt.getAddress() },
    liquidityPool: {
      token: "USDT",
      collateralProvider: provider.address,
      operator: operator.address,
      guardian: guardian.address,
      recipient: treasury.address,
      timelockAdmin: admin.address,
      timelockAdminConfirm: admin.address,
      timelockDelaySec: 72 * 3600,
      minDeposit: 5, maxPerLp: 5000, totalCap: 50000,
      maxFillPerTx: 2000, fillWindowSec: 86400, fillWindowCap: 20000,
      ...over,
    },
  };
  return { chain, deployer, admin, treasury };
}

describe("deploy/liquidity-pool", () => {
  it("wires every role and limit, then hands ownership to the timelock", async () => {
    const { chain, admin, treasury } = await fixture();
    const out = await deploy(hre, { chain });
    const pool = await ethers.getContractAt("TsaLiquidityPool", out.pool);
    const tl = await ethers.getContractAt("TimelockController", out.timelock);
    expect(await pool.owner()).to.equal(out.timelock);
    expect(await pool.recipient(treasury.address)).to.equal(true);
    expect(await pool.maxPerLp()).to.equal(ethers.parseUnits("5000", 18));
    expect(await tl.getMinDelay()).to.equal(72n * 3600n);
    expect(await tl.hasRole(await tl.PROPOSER_ROLE(), admin.address)).to.equal(true);
    expect(await tl.hasRole(await tl.DEFAULT_ADMIN_ROLE(), admin.address)).to.equal(false);
  });

  it("refuses a timelock admin that is the deployer or not typed twice", async () => {
    const { chain, deployer, admin } = await fixture();
    chain.liquidityPool.timelockAdmin = deployer.address;
    chain.liquidityPool.timelockAdminConfirm = deployer.address;
    await expect(deploy(hre, { chain })).to.be.rejectedWith(/is the deployer/);
    chain.liquidityPool.timelockAdmin = admin.address;
    chain.liquidityPool.timelockAdminConfirm = null;
    await expect(deploy(hre, { chain })).to.be.rejectedWith(/same address/);
  });

  it("refuses to run without the PM's collateral wallet", async () => {
    const { chain } = await fixture({ collateralProvider: null });
    await expect(deploy(hre, { chain })).to.be.rejectedWith(/collateralProvider is required/);
  });
});
