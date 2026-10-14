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
      owner: admin.address,
      ownerConfirm: admin.address,
      minDeposit: 5, maxPerLp: 5000, totalCap: 50000,
      maxFillPerTx: 2000, fillWindowSec: 86400, fillWindowCap: 20000, maxSpreadBps: 300, withdrawLockSec: 86400,
      ...over,
    },
  };
  return { chain, deployer, admin, treasury };
}

describe("deploy/liquidity-pool", () => {
  it("wires every role and limit, then hands ownership to the owner's wallet", async () => {
    const { chain, admin, treasury } = await fixture();
    const out = await deploy(hre, { chain });
    const pool = await ethers.getContractAt("TsaLiquidityPool", out.pool);
    expect(await pool.owner()).to.equal(admin.address);
    expect(await pool.recipient(treasury.address)).to.equal(true);
    expect(await pool.maxPerLp()).to.equal(ethers.parseUnits("5000", 18));
    expect(await pool.withdrawLock()).to.equal(86400n);
  });

  it("refuses an owner that is the deployer or not typed twice", async () => {
    const { chain, deployer, admin } = await fixture();
    chain.liquidityPool.owner = deployer.address;
    chain.liquidityPool.ownerConfirm = deployer.address;
    await expect(deploy(hre, { chain })).to.be.rejectedWith(/is the deployer/);
    chain.liquidityPool.owner = admin.address;
    chain.liquidityPool.ownerConfirm = null;
    await expect(deploy(hre, { chain })).to.be.rejectedWith(/same address/);
  });

  it("refuses to run without the PM's collateral wallet", async () => {
    const { chain } = await fixture({ collateralProvider: null });
    await expect(deploy(hre, { chain })).to.be.rejectedWith(/collateralProvider is required/);
  });
});
