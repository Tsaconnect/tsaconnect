const { expect } = require("chai");
const hre = require("hardhat");
const { ethers } = hre;
const { poolFixture, depositAndSign, openSlice, POOL_WINDOW } = require("../helpers/pool");

describe("deploy/pool", () => {
  it("PoolDeploy_ZeroFeeBothPaths", async () => {
    const ctx = await poolFixture();
    const voucher = await depositAndSign(ctx, ctx.lpA, ethers.parseUnits("100", 18));

    // Active rows at 0: the trade snapshots 0 and releases the full amount.
    const id1 = ethers.id("pool-zero-fee-active");
    const amt1 = ethers.parseUnits("60", 18);
    await openSlice(ctx, voucher, id1, amt1);
    const t1 = await ctx.esc.trades(id1);
    expect(t1.platformFeeBpsSnapshot).to.equal(0n);
    expect(t1.gasFeeBpsSnapshot).to.equal(0n);
    await expect(ctx.esc.connect(ctx.admin).adminRelease(id1))
      .to.emit(ctx.esc, "TradeReleased").withArgs(id1, ctx.treasury.address, amt1, 0n);

    // Rows flipped inactive fall back to the legacy feeBps, which is also 0 (S2).
    await (await ctx.fm.connect(ctx.ownerWallet).setRates(["p2p_platform", "p2p"], ["USDT", "USDT"],
      [[0, 0, false, false], [0, 0, false, false]])).wait();
    const id2 = ethers.id("pool-zero-fee-inactive");
    const amt2 = ethers.parseUnits("10", 18);
    await openSlice(ctx, voucher, id2, amt2);
    expect((await ctx.esc.trades(id2)).platformFeeBpsSnapshot).to.equal(0n);
    await expect(ctx.esc.connect(ctx.admin).adminRelease(id2))
      .to.emit(ctx.esc, "TradeReleased").withArgs(id2, ctx.treasury.address, amt2, 0n);
  });

  it("wires exactly: window, admin, system wallet, pool tokens only, owner last", async () => {
    const ctx = await poolFixture();
    expect(await ctx.esc.paymentWindow()).to.equal(BigInt(POOL_WINDOW));
    expect(await ctx.esc.feeBps()).to.equal(0n);
    expect(await ctx.esc.feeManager()).to.equal(ctx.out.feeManager);
    expect(await ctx.esc.isAdmin(ctx.admin.address)).to.equal(true);
    expect(await ctx.esc.systemWallet()).to.equal(ctx.treasury.address);
    expect(await ctx.esc.acceptedTokens(ctx.chain.tokens.USDT)).to.equal(true);
    expect(await ctx.esc.acceptedTokens(ctx.chain.tokens.USDC)).to.equal(false);
    expect(await ctx.esc.owner()).to.equal(ctx.ownerWallet.address);
    expect(await ctx.fm.owner()).to.equal(ctx.ownerWallet.address);
    expect(ctx.out.owner).to.equal(ctx.ownerWallet.address);
  });

  it("PoolDeploy_AssertsBeforeOwnershipTransfer", async () => {
    let err;
    try {
      await poolFixture({ tokenDecimals: 4 }); // below 6: the decimals assertion fails
    } catch (e) {
      err = e;
    }
    expect(err, "a 4-dp pool token must fail the assertions").to.not.equal(undefined);
    expect(err.message).to.match(/fewer than 6 decimals/);
    const [deployer] = await ethers.getSigners();
    const esc = await ethers.getContractAt("P2PEscrowV3", err.deployed.escrow);
    const fm = await ethers.getContractAt("FeeManager", err.deployed.feeManager);
    expect(await esc.owner()).to.equal(deployer.address);
    expect(await fm.owner()).to.equal(deployer.address);
  });

  it("refuses a requireOwner network with no owner wallet before deploying anything", async () => {
    const before = await ethers.provider.getTransactionCount((await ethers.getSigners())[0].address);
    await expect(poolFixture({ poolOwner: null, requireOwner: true })).to.be.rejectedWith(/POOL_OWNER_ADDRESS/);
    // Only the two mock-token deploys happened; no proxy was deployed.
    expect(await ethers.provider.getTransactionCount((await ethers.getSigners())[0].address)).to.equal(before + 2);
  });

  it("refuses a requireOwner network whose owner wallet is the deployer (review m1)", async () => {
    const [deployer] = await ethers.getSigners();
    await expect(poolFixture({ poolOwner: deployer.address, requireOwner: true })).to.be.rejectedWith(/is the deployer/);
  });

  it("refuses a requireOwner network without a matching POOL_OWNER_CONFIRM (review m1)", async () => {
    const [, , , , , ownerWallet, stranger] = await ethers.getSigners();
    await expect(poolFixture({ poolOwner: ownerWallet.address, requireOwner: true, ownerConfirm: stranger.address }))
      .to.be.rejectedWith(/POOL_OWNER_CONFIRM/);
    await expect(poolFixture({ poolOwner: ownerWallet.address, requireOwner: true, ownerConfirm: null }))
      .to.be.rejectedWith(/POOL_OWNER_CONFIRM/);
  });

  it("keeps the deployer as owner when no owner wallet is set (testnet)", async () => {
    const ctx = await poolFixture({ poolOwner: null });
    expect(await ctx.esc.owner()).to.equal(ctx.deployer.address);
  });
});
