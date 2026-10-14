// TsaLiquidityPool — shared pool, 100% collateral (spec 2026-10-14).
//
// The two properties everything else serves:
//   1. pro-rata earnings: a fill's spread is split by principal (the PM's table);
//   2. custody: the pool always holds every LP's principal + earnings, so
//      withdraw/claim always succeed — paused, delisted, or never refilled.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

const u = (n) => ethers.parseUnits(String(n), 18);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));
const HOUR = 3600;

async function deployPool(overrides = {}) {
  const [owner, provider, operator, guardian, treasury, ...lps] = await ethers.getSigners();
  const MockERC20 = await ethers.getContractFactory("MockERC20");
  const token = overrides.token || (await MockERC20.deploy("Tether USD", "USDT", 18));
  const params = {
    token: await token.getAddress(),
    owner: owner.address,
    collateralProvider: provider.address,
    operator: operator.address,
    guardian: guardian.address,
    recipient: treasury.address,
    minDeposit: u(5),
    maxPerLp: u(5000),
    totalCap: u(50000),
    maxFillPerTx: u(1000),
    fillWindow: 24 * HOUR,
    fillWindowCap: u(10000),
    maxSpreadBps: 1000,
    // Most cases exercise custody, not the lock: no lock unless a test sets one.
    withdrawLock: 0,
    ...overrides.params,
  };
  const Pool = await ethers.getContractFactory("TsaLiquidityPool");
  const pool = await upgrades.deployProxy(Pool, [params], { kind: "uups" });
  await pool.waitForDeployment();
  const poolAddr = await pool.getAddress();
  if (!overrides.token) {
    for (const s of [provider, ...lps]) {
      await token.mint(s.address, u(1_000_000));
      await token.connect(s).approve(poolAddr, ethers.MaxUint256);
    }
  }
  return { pool, token, owner, provider, operator, guardian, treasury, lps, poolAddr };
}

// balance − (principal + Σclaimable + collateral − outstanding): dust the
// accumulator kept back, which must never go negative.
async function slack(pool, token, lps) {
  const bal = await token.balanceOf(await pool.getAddress());
  let owed = (await pool.totalPrincipal()) + (await pool.collateral()) - (await pool.outstanding());
  for (const lp of lps) owed += await pool.claimable(lp.address);
  return bal - owed;
}

describe("TsaLiquidityPool", function () {
  let ctx;
  beforeEach(async function () {
    ctx = await deployPool();
    const { pool, operator, lps } = ctx;
    for (const lp of lps.slice(0, 6)) await pool.connect(operator).setDepositor(lp.address, true);
  });

  describe("pro-rata earnings", function () {
    it("splits $1 of spread across $25 of stakes exactly as the PM's table", async function () {
      const { pool, provider, operator, treasury, lps } = ctx;
      const stakes = ["5", "10", "2.5", "2.5", "5"];
      // Per-LP minimum is $5; lower it so the $2.50 stakes of the table fit.
      await pool.setLimits(u(1), u(5000), u(50000));
      for (let i = 0; i < 5; i++) await pool.connect(lps[i]).deposit(u(stakes[i]));
      await pool.connect(provider).postCollateral(u(200));

      await pool.connect(operator).fill(treasury.address, u(100), u(1), refOf("order-1"));

      const want = ["0.2", "0.4", "0.1", "0.1", "0.2"];
      for (let i = 0; i < 5; i++) expect(await pool.claimable(lps[i].address)).to.equal(u(want[i]));
      expect(await pool.outstanding()).to.equal(u(101));
      expect(await ctx.token.balanceOf(treasury.address)).to.equal(u(100));
    });

    it("a depositor arriving after a fill earns nothing from it", async function () {
      const { pool, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(10));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("a"));
      await pool.connect(lps[1]).deposit(u(10));
      expect(await pool.claimable(lps[1].address)).to.equal(0);
      await pool.connect(operator).fill(treasury.address, u(20), u(2), refOf("b"));
      expect(await pool.claimable(lps[0].address)).to.equal(u(2));
      expect(await pool.claimable(lps[1].address)).to.equal(u(1));
    });

    it("earnings survive a partial withdraw and are claimable to self, another address, or by anyone for the LP", async function () {
      const { pool, token, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(3), refOf("a"));
      await pool.connect(lps[0]).withdraw(u(60));
      expect(await pool.claimable(lps[0].address)).to.equal(u(3));

      const before = await token.balanceOf(lps[0].address);
      await pool.connect(lps[5]).claimFor(lps[0].address); // a relayer pays gas
      expect(await token.balanceOf(lps[0].address)).to.equal(before + u(3));
      await expect(pool.connect(lps[0]).claim()).to.be.revertedWith("Nothing to claim");

      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("b"));
      const off = lps[5].address;
      const offBefore = await token.balanceOf(off);
      await pool.connect(lps[0]).claimTo(off);
      expect(await token.balanceOf(off)).to.equal(offBefore + u(1));
    });
  });

  describe("custody (100% collateral)", function () {
    it("refuses a fill that would leave outstanding above collateral", async function () {
      const { pool, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(1000));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(90), u(1), refOf("a"));
      await expect(
        pool.connect(operator).fill(treasury.address, u("9.5"), u("0.5"), refOf("b")),
      ).to.be.revertedWith("Not covered by collateral");
      await pool.connect(operator).fill(treasury.address, u("8.5"), u("0.5"), refOf("c")); // exactly 100
      expect(await pool.freeCapacity()).to.equal(0);
    });

    it("every LP withdraws in full without any refill, after the provider's collateral is consumed", async function () {
      const { pool, token, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(300));
      await pool.connect(lps[1]).deposit(u(100));
      await pool.connect(provider).postCollateral(u(400));
      await pool.connect(operator).fill(treasury.address, u(392), u(8), refOf("a"));
      // Provider never refills and cannot take collateral back.
      await expect(pool.connect(provider).withdrawCollateral(1)).to.be.revertedWith("Collateral is backing fills");

      for (const [lp, p, e] of [[lps[0], "300", "6"], [lps[1], "100", "2"]]) {
        const before = await token.balanceOf(lp.address);
        await pool.connect(lp).withdraw(u(p));
        await pool.connect(lp).claim();
        expect(await token.balanceOf(lp.address)).to.equal(before + u(p) + u(e));
      }
      expect(await token.balanceOf(await pool.getAddress())).to.equal(0);
    });

    it("withdraw and claim work while paused and after the LP is delisted; deposit and fill do not", async function () {
      const { pool, provider, operator, guardian, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("a"));
      await pool.connect(guardian).pause();
      await pool.connect(operator).setDepositor(lps[0].address, false);

      await expect(pool.connect(lps[0]).deposit(u(10))).to.be.reverted;
      await expect(pool.connect(operator).fill(treasury.address, u(1), 0, refOf("b"))).to.be.reverted;
      await pool.connect(lps[0]).claim();
      await pool.connect(lps[0]).withdraw(u(100));
      expect(await pool.principal(lps[0].address)).to.equal(0);
    });

    it("refill and settle-from-collateral close outstanding and free collateral", async function () {
      const { pool, token, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(1), refOf("a"));
      await pool.connect(provider).refill(u(31), refOf("r1"));
      await pool.connect(operator).settleFromCollateral(u(20), refOf("s1"));
      expect(await pool.outstanding()).to.equal(0);
      expect(await pool.collateral()).to.equal(u(80));
      await expect(pool.connect(provider).refill(1, refOf("r2"))).to.be.revertedWith("Bad amount");
      await expect(pool.connect(provider).refill(1, refOf("r1"))).to.be.revertedWith("Bad or used ref");

      const before = await token.balanceOf(provider.address);
      await pool.connect(provider).withdrawCollateral(u(80));
      expect(await token.balanceOf(provider.address)).to.equal(before + u(80));
      expect(await slack(pool, token, lps.slice(0, 6))).to.equal(0);
    });

    it("keeps the invariant over a random sequence of every operation", async function () {
      const { pool, token, provider, operator, treasury, lps } = ctx;
      const actors = lps.slice(0, 6);
      let seed = 0x5eed;
      const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
      let refs = 0;
      for (let step = 0; step < 300; step++) {
        const lp = actors[rnd(actors.length)];
        const amt = u(1 + rnd(200));
        const op = rnd(8);
        try {
          if (op === 0) await pool.connect(lp).deposit(amt);
          else if (op === 1) await pool.connect(lp).withdraw(amt);
          else if (op === 2) await pool.connect(lp).claim();
          else if (op === 3) await pool.connect(provider).postCollateral(amt);
          else if (op === 4) await pool.connect(operator).fill(treasury.address, amt, amt / 100n, refOf(`f${refs++}`));
          else if (op === 5) await pool.connect(provider).refill(amt, refOf(`r${refs++}`));
          else if (op === 6) await pool.connect(operator).settleFromCollateral(amt, refOf(`s${refs++}`));
          else await pool.connect(provider).withdrawCollateral(amt);
        } catch (_) { /* refused operations are part of the walk */ }
        expect(await pool.outstanding()).to.be.lte(await pool.collateral());
        const s = await slack(pool, token, actors);
        expect(s).to.be.gte(0);
        expect(s).to.be.lte(1000n); // rounding dust only
      }
      // Everyone exits in full at the end.
      for (const lp of actors) {
        const p = await pool.principal(lp.address);
        if (p > 0n) await pool.connect(lp).withdraw(p);
        if ((await pool.claimable(lp.address)) > 0n) await pool.connect(lp).claim();
      }
    });
  });

  // Review 2026-10-14 (LOW): reward debt set rounded down let an LP keep a
  // wei of fraction per deposit/withdraw, so the books could exceed the
  // balance and the last claim reverted.
  describe("rounding", function () {
    it("never credits LPs more than the spread paid in; the last LP still exits", async function () {
      const c = await deployPool({ params: {
        minDeposit: 1n, maxPerLp: 100n, totalCap: 1000n, maxFillPerTx: 200n, fillWindowCap: 2000n, maxSpreadBps: 1000,
      } });
      const { pool, token, provider, operator, treasury, lps } = c;
      const [A, B] = lps;
      for (const l of [A, B]) await pool.connect(operator).setDepositor(l.address, true);
      await pool.connect(provider).postCollateral(1000n);
      let n = 0;
      const fill = (s) => pool.connect(operator).fill(treasury.address, 100n, s, refOf(`f${n++}`));
      await pool.connect(B).deposit(5n); await fill(4n); await pool.connect(A).deposit(2n); await fill(2n); await fill(1n);
      await pool.connect(A).deposit(3n); await pool.connect(B).deposit(2n); await pool.connect(A).deposit(2n); await fill(3n);
      await pool.connect(A).withdraw(1n); await fill(2n);
      const credited = 4n + 2n + 1n + 3n + 2n;
      expect((await pool.claimable(A.address)) + (await pool.claimable(B.address))).to.be.lte(credited);
      expect(await slack(pool, token, [A, B])).to.be.gte(0n);
      await pool.connect(provider).withdrawCollateral(await pool.freeCapacity());
      await pool.connect(A).withdraw(await pool.principal(A.address));
      await pool.connect(B).withdraw(await pool.principal(B.address));
      await pool.connect(A).claim();
      await pool.connect(B).claim();
    });
  });

  // Owner 2026-10-14: a deposit earns from the start but cannot leave for
  // withdrawLock, so a depositor cannot deposit just before a fill and
  // withdraw right after with a share of its spread.
  describe("withdraw lock", function () {
    const DAY = 24 * HOUR;
    let c;
    beforeEach(async function () {
      c = await deployPool({ params: { withdrawLock: DAY } });
      await c.pool.connect(c.operator).setDepositor(c.lps[0].address, true);
      await c.pool.connect(c.provider).postCollateral(u(1000));
    });

    it("a fresh deposit earns at once but cannot be withdrawn until the lock ends; earnings can be claimed", async function () {
      const { pool, operator, treasury, lps } = c;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(100), u(1), refOf("a"));
      expect(await pool.claimable(lps[0].address)).to.equal(u(1)); // earning from the deposit on
      expect(await pool.withdrawable(lps[0].address)).to.equal(0);
      await expect(pool.connect(lps[0]).withdraw(u(1))).to.be.revertedWith("Deposit still locked");
      await pool.connect(lps[0]).claim(); // earnings are never locked
      await time.increase(DAY);
      expect(await pool.withdrawable(lps[0].address)).to.equal(u(100));
      await pool.connect(lps[0]).withdraw(u(100));
    });

    it("locks only the new money: older, unlocked principal stays withdrawable after a top-up", async function () {
      const { pool, lps } = c;
      await pool.connect(lps[0]).deposit(u(100));
      await time.increase(DAY);
      await pool.connect(lps[0]).deposit(u(50));
      expect(await pool.withdrawable(lps[0].address)).to.equal(u(100));
      await expect(pool.connect(lps[0]).withdraw(u(101))).to.be.revertedWith("Deposit still locked");
      await pool.connect(lps[0]).withdraw(u(100));
      // Two deposits inside one lock join it and unlock a day after the newest.
      await time.increase(HOUR);
      await pool.connect(lps[0]).deposit(u(20));
      expect(await pool.withdrawable(lps[0].address)).to.equal(0);
      await time.increase(DAY - HOUR + 1);
      expect(await pool.withdrawable(lps[0].address)).to.equal(0); // the first one's day passed, the newest's has not
      await time.increase(HOUR);
      expect(await pool.withdrawable(lps[0].address)).to.equal(u(70));
    });

    it("the lock holds while paused too, and only the owner (timelock) changes it, up to 7 days", async function () {
      const { pool, guardian, lps } = c;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(guardian).pause();
      await expect(pool.connect(lps[0]).withdraw(u(1))).to.be.revertedWith("Deposit still locked");
      await expect(pool.connect(lps[0]).setWithdrawLock(0)).to.be.reverted;
      await expect(pool.setWithdrawLock(7 * DAY + 1)).to.be.revertedWith("withdraw lock over maximum");
      await pool.setWithdrawLock(0);
      // The change applies to new deposits; the existing lock keeps its end.
      expect(await pool.withdrawable(lps[0].address)).to.equal(0);
      await time.increase(DAY);
      await pool.connect(lps[0]).withdraw(u(100));
    });
  });

  describe("operator bounds", function () {
    beforeEach(async function () {
      await ctx.pool.connect(ctx.lps[0]).deposit(u(1000));
      await ctx.pool.connect(ctx.provider).postCollateral(u(20000));
    });

    it("only pays approved recipients, never replays a ref, and respects per-tx and window caps", async function () {
      const { pool, operator, treasury, lps } = ctx;
      await expect(pool.connect(operator).fill(lps[1].address, u(1), 0, refOf("x")))
        .to.be.revertedWith("Not an approved recipient");
      await pool.connect(operator).fill(treasury.address, u(1), 0, refOf("x"));
      await expect(pool.connect(operator).fill(treasury.address, u(1), 0, refOf("x")))
        .to.be.revertedWith("Bad or used ref");
      await expect(pool.connect(operator).fill(treasury.address, u(1001), 0, refOf("y")))
        .to.be.revertedWith("Bad fill amount");
      for (let i = 0; i < 9; i++) await pool.connect(operator).fill(treasury.address, u(1000), 0, refOf(`w${i}`));
      await expect(pool.connect(operator).fill(treasury.address, u(1000), 0, refOf("over")))
        .to.be.revertedWith("Over fill window cap");
      await time.increase(24 * HOUR);
      await pool.connect(operator).fill(treasury.address, u(1000), 0, refOf("next-day"));
    });

    // Review 2026-10-14 (HIGH): a leaked operator key that allow-lists itself
    // could credit all free collateral to LPs as "spread" in one 1-wei fill.
    it("caps the spread by the amount, and counts it against the per-tx and window caps", async function () {
      const { pool, operator, treasury } = ctx;
      await expect(pool.connect(operator).fill(treasury.address, 1n, u(500), refOf("drain")))
        .to.be.revertedWith("Spread over maximum");
      await expect(pool.connect(operator).fill(treasury.address, u(100), u("10.000000000000000001"), refOf("a")))
        .to.be.revertedWith("Spread over maximum");
      await pool.connect(operator).fill(treasury.address, u(100), u(10), refOf("a")); // exactly 10%
      await expect(pool.connect(operator).fill(treasury.address, u(1000), u(1), refOf("b")))
        .to.be.revertedWith("Bad fill amount"); // 1000 + 1 over the 1000 per-tx cap
      for (let i = 0; i < 9; i++) await pool.connect(operator).fill(treasury.address, u(900), u(90), refOf(`w${i}`));
      // 110 + 9 × 990 = 9020 of the 10000 window. Another 990 is over it,
      // though on amounts alone (100 + 10 × 900 = 9100) it would pass.
      await expect(pool.connect(operator).fill(treasury.address, u(900), u(90), refOf("w9")))
        .to.be.revertedWith("Over fill window cap");
      expect(await pool.filledInWindow()).to.equal(u(9020));
    });

    it("refuses a spread when there are no LPs to credit", async function () {
      const { pool, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).withdraw(u(1000));
      await expect(pool.connect(operator).fill(treasury.address, u(1), 1, refOf("a")))
        .to.be.revertedWith("No LPs to credit");
      await pool.connect(operator).fill(treasury.address, u(1), 0, refOf("b"));
    });

    it("roles are enforced on every privileged call", async function () {
      const { pool, provider, treasury, lps } = ctx;
      const stranger = lps[5];
      await expect(pool.connect(stranger).fill(treasury.address, 1, 0, refOf("a"))).to.be.revertedWith("Not operator");
      await expect(pool.connect(stranger).settleFromCollateral(1, refOf("a"))).to.be.revertedWith("Not operator or provider");
      await expect(pool.connect(stranger).setDepositor(stranger.address, true)).to.be.revertedWith("Not operator or owner");
      await expect(pool.connect(stranger).postCollateral(1)).to.be.revertedWith("Not collateral provider");
      await expect(pool.connect(stranger).pause()).to.be.revertedWith("Not owner or guardian");
      await expect(pool.connect(provider).setRecipient(stranger.address, true)).to.be.reverted;
      await expect(pool.connect(provider).upgradeToAndCall(stranger.address, "0x")).to.be.reverted;
    });

    it("keeps refill refs apart from settle refs, so an operator cannot burn the provider's", async function () {
      const { pool, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(1), refOf("a"));
      await pool.connect(operator).settleFromCollateral(u(1), refOf("planned"));
      await pool.connect(provider).refill(u(10), refOf("planned"));
      expect(await pool.outstanding()).to.equal(u(40));
    });

    it("the collateral provider adds operators without the timelock; the guardian removes one at once", async function () {
      const { pool, provider, operator, guardian, treasury, lps } = ctx;
      const second = lps[4];
      await expect(pool.connect(second).fill(treasury.address, 1, 0, refOf("a"))).to.be.revertedWith("Not operator");
      await expect(pool.connect(lps[5]).addOperator(second.address)).to.be.revertedWith("Not provider or owner");
      await expect(pool.connect(guardian).addOperator(second.address)).to.be.revertedWith("Not provider or owner");
      await pool.connect(provider).addOperator(second.address);
      await pool.connect(second).fill(treasury.address, u(1), 0, refOf("b"));
      await pool.connect(operator).fill(treasury.address, u(1), 0, refOf("c")); // the first one still works
      await pool.connect(guardian).removeOperator(second.address);
      await expect(pool.connect(second).fill(treasury.address, u(1), 0, refOf("d"))).to.be.revertedWith("Not operator");
      await expect(pool.connect(provider).addOperator(provider.address)).to.be.revertedWith("operator must not be the provider");
      await expect(pool.connect(lps[5]).removeOperator(operator.address)).to.be.revertedWith("Not provider, owner or guardian");
    });

    it("the collateral provider can only change once its collateral is gone", async function () {
      const { pool, provider, operator, guardian, lps } = ctx;
      await expect(pool.setRoles(lps[5].address, guardian.address))
        .to.be.revertedWith("Collateral still posted");
      await pool.connect(provider).withdrawCollateral(u(20000));
      await pool.setRoles(lps[5].address, guardian.address);
      expect(await pool.collateralProvider()).to.equal(lps[5].address);
    });
  });

  describe("deposit limits", function () {
    it("enforces the allow-list, minimum, per-LP maximum and pool cap", async function () {
      const { pool, operator, lps } = ctx;
      await expect(pool.connect(lps[6]).deposit(u(10))).to.be.revertedWith("Not an allowed depositor");
      await expect(pool.connect(lps[0]).deposit(u(4))).to.be.revertedWith("Below minimum deposit");
      await pool.connect(lps[0]).deposit(u(5000));
      await expect(pool.connect(lps[0]).deposit(u(5))).to.be.revertedWith("Over per-LP maximum");
      await pool.setLimits(u(5), u(5000), u(5010));
      await expect(pool.connect(lps[1]).deposit(u(11))).to.be.revertedWith("Over pool cap");
      await pool.connect(lps[1]).deposit(u(10));
    });

    it("rejects zero caps and out-of-order limits", async function () {
      const { pool } = ctx;
      await expect(pool.setFillCaps(0, HOUR, u(1), 300)).to.be.revertedWith("fill caps must be > 0");
      await expect(pool.setFillCaps(u(1), HOUR, u(1), 0)).to.be.revertedWith("fill caps must be > 0");
      await expect(pool.setFillCaps(u(2), HOUR, u(1), 300)).to.be.revertedWith("perTx over window cap");
      await expect(pool.setFillCaps(u(1), HOUR, u(1), 1001)).to.be.revertedWith("spread cap over ceiling");
      await expect(pool.setLimits(u(10), u(5), u(100))).to.be.revertedWith("limits out of order");
    });

    it("refuses a fee-on-transfer token", async function () {
      const Fee = await ethers.getContractFactory("FeeOnTransferERC20");
      const fee = await Fee.deploy();
      const { pool, operator, lps } = await deployPool({ token: fee });
      await fee.mint(lps[0].address, u(100));
      await fee.connect(lps[0]).approve(await pool.getAddress(), ethers.MaxUint256);
      await pool.connect(operator).setDepositor(lps[0].address, true);
      await expect(pool.connect(lps[0]).deposit(u(10))).to.be.revertedWith("Inexact transfer");
    });
  });

  describe("upgrade through the timelock", function () {
    it("an upgrade only lands after the delay, and balances survive it", async function () {
      const { pool, owner, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(provider).postCollateral(u(100));
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("a"));

      const delay = 72 * HOUR;
      const Timelock = await ethers.getContractFactory("TimelockController");
      const tl = await Timelock.deploy(delay, [owner.address], [owner.address], ethers.ZeroAddress);
      await pool.transferOwnership(await tl.getAddress());
      await expect(pool.renounceOwnership()).to.be.reverted;

      const V2 = await ethers.getContractFactory("TsaLiquidityPoolV2Test");
      await upgrades.validateUpgrade(await ethers.getContractFactory("TsaLiquidityPool"), V2, { kind: "uups" });
      const impl = await upgrades.prepareUpgrade(await pool.getAddress(), V2, { kind: "uups" });
      const data = pool.interface.encodeFunctionData("upgradeToAndCall", [impl, "0x"]);
      const args = [await pool.getAddress(), 0, data, ethers.ZeroHash, ethers.ZeroHash];
      await tl.schedule(...args, delay);
      await expect(tl.execute(...args)).to.be.reverted;
      await time.increase(delay);
      await tl.execute(...args);

      const upgraded = await ethers.getContractAt("TsaLiquidityPoolV2Test", await pool.getAddress());
      expect(await upgraded.version()).to.equal("v2-test");
      expect(await upgraded.principal(lps[0].address)).to.equal(u(100));
      expect(await upgraded.claimable(lps[0].address)).to.equal(u(1));
      expect(await upgraded.outstanding()).to.equal(u(11));
    });
  });
});
