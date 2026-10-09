const { expect } = require("chai");
const { ethers } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");
const { poolFixture, depositAndSign, openSlice, POOL_WINDOW } = require("./helpers/pool");

const u = (n) => ethers.parseUnits(n, 18);

describe("P2PEscrowV3 as the TSA Liquidity pool (18-dp)", () => {
  // Spec §10.2 split at the 50 bps spread: LP A 60.000000, LP B 12.419593 USDT, in 18-dp chain units.
  it("Pool_TreasuryOpensWithLPVoucher_18dp", async () => {
    const ctx = await poolFixture();
    const token = await ctx.usdt.getAddress();
    const a = await depositAndSign(ctx, ctx.lpA, u("60"));
    const b = await depositAndSign(ctx, ctx.lpB, u("45"));
    await openSlice(ctx, a, ethers.id("split-a"), 60000000000000000000n);   // 60.000000 × 10^12
    await openSlice(ctx, b, ethers.id("split-b"), 12419593000000000000n);   // 12.419593 × 10^12
    expect(await ctx.esc.available(ctx.lpA.address, token)).to.equal(0n);
    expect(await ctx.esc.locked(ctx.lpA.address, token)).to.equal(60000000000000000000n);
    expect(await ctx.esc.available(ctx.lpB.address, token)).to.equal(32580407000000000000n); // 45 − 12.419593
    expect(await ctx.esc.locked(ctx.lpB.address, token)).to.equal(12419593000000000000n);
    const t = await ctx.esc.trades(ethers.id("split-b"));
    expect(t.buyer).to.equal(ctx.treasury.address);
    expect(t.seller).to.equal(ctx.lpB.address);
  });

  it("Pool_ReopenSameTradeIdReverts", async () => {
    const ctx = await poolFixture();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    await openSlice(ctx, a, ethers.id("dup"), u("5"));
    await expect(openSlice(ctx, a, ethers.id("dup"), u("5"))).to.be.revertedWithCustomError(ctx.esc, "TradeExists");
  });

  it("Pool_CancelByTimeoutAt60Min", async () => {
    const ctx = await poolFixture();
    const token = await ctx.usdt.getAddress();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    const id = ethers.id("abandoned");
    await openSlice(ctx, a, id, u("20"));
    const createdAt = (await ctx.esc.trades(id)).createdAt;
    const voucherHash = await ctx.esc.hashVoucher(a.v);
    await time.increaseTo(createdAt + BigInt(POOL_WINDOW) - 2n);
    await expect(ctx.esc.connect(ctx.stranger).cancelByTimeout(id)).to.be.revertedWithCustomError(ctx.esc, "TimeoutNotElapsed");
    await time.increaseTo(createdAt + BigInt(POOL_WINDOW));
    await expect(ctx.esc.connect(ctx.stranger).cancelByTimeout(id)).to.emit(ctx.esc, "TradeCancelled").withArgs(id, u("20"), true);
    expect(await ctx.esc.available(ctx.lpA.address, token)).to.equal(u("50"));
    expect(await ctx.esc.locked(ctx.lpA.address, token)).to.equal(0n);
    expect(await ctx.esc.voucherFilled(voucherHash)).to.equal(0n);
  });

  it("Pool_AdminReleaseOnlyToBuyer", async () => {
    const ctx = await poolFixture();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    const id = ethers.id("release-to-buyer");
    await openSlice(ctx, a, id, u("20"));
    await expect(ctx.esc.connect(ctx.stranger).adminRelease(id)).to.be.revertedWithCustomError(ctx.esc, "NotAdmin");
    const adminBefore = await ctx.usdt.balanceOf(ctx.admin.address);
    await expect(ctx.esc.connect(ctx.admin).adminRelease(id))
      .to.emit(ctx.esc, "TradeReleased").withArgs(id, ctx.treasury.address, u("20"), 0n);
    expect(await ctx.usdt.balanceOf(ctx.admin.address)).to.equal(adminBefore);
  });

  it("Pool_LPWithdrawLockedReverts", async () => {
    const ctx = await poolFixture();
    const token = await ctx.usdt.getAddress();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    await openSlice(ctx, a, ethers.id("locked"), u("20"));
    await expect(ctx.esc.connect(ctx.lpA).withdraw(token, u("31"))).to.be.revertedWithCustomError(ctx.esc, "InsufficientAvailable");
    await ctx.esc.connect(ctx.lpA).withdraw(token, u("30")); // only `available` leaves
  });

  it("Pool_LPDisputeThenAdminResolveToBuyer", async () => {
    const ctx = await poolFixture();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    const id = ethers.id("lp-dispute-paid");
    await openSlice(ctx, a, id, u("20"));
    await expect(ctx.esc.connect(ctx.lpA).raiseDispute(id)).to.emit(ctx.esc, "TradeDisputed").withArgs(id, ctx.lpA.address);
    await expect(ctx.esc.connect(ctx.admin).adminResolve(id, true))
      .to.emit(ctx.esc, "TradeResolved").withArgs(id, true, u("20"), 0n);
    expect(await ctx.usdt.balanceOf(ctx.treasury.address)).to.equal(u("20"));
  });

  it("Pool_LPDisputeThenAdminResolveToLP", async () => {
    const ctx = await poolFixture();
    const token = await ctx.usdt.getAddress();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    const id = ethers.id("lp-dispute-unpaid");
    await openSlice(ctx, a, id, u("20"));
    await ctx.esc.connect(ctx.lpA).raiseDispute(id);
    await ctx.esc.connect(ctx.admin).adminResolve(id, false);
    expect(await ctx.esc.available(ctx.lpA.address, token)).to.equal(u("50"));
  });

  it("Pool_LPSelfRelease", async () => {
    const ctx = await poolFixture();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    const id = ethers.id("lp-self-release");
    await openSlice(ctx, a, id, u("20"));
    await expect(ctx.esc.connect(ctx.lpA).release(id))
      .to.emit(ctx.esc, "TradeReleased").withArgs(id, ctx.treasury.address, u("20"), 0n);
  });

  it("Pool_CancelVoucherBlocksOpen", async () => {
    const ctx = await poolFixture();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    await ctx.esc.connect(ctx.lpA).cancelVoucher(a.v);
    await expect(openSlice(ctx, a, ethers.id("after-cancel"), u("5"))).to.be.revertedWithCustomError(ctx.esc, "VoucherCancelledErr");
  });

  it("Pool_PausedEscrowBlocksAdminRelease (runbook: never pause with paid slices)", async () => {
    const ctx = await poolFixture();
    const a = await depositAndSign(ctx, ctx.lpA, u("50"));
    const id = ethers.id("paused");
    await openSlice(ctx, a, id, u("20"));
    await ctx.esc.connect(ctx.ownerWallet).pause();
    await expect(ctx.esc.connect(ctx.admin).adminRelease(id)).to.be.revertedWithCustomError(ctx.esc, "EnforcedPause");
  });
});
