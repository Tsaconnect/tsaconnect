// TsaLiquidityPool — shared pool, no company collateral (spec 2026-10-14
// "liquidity-no-collateral").
//
// The two properties everything else serves:
//   1. pro-rata earnings: a fill's spread is split by principal (the PM's table);
//   2. accounting: balance + outstanding always covers every LP's principal,
//      earnings and pending withdrawals; a withdraw pays what is liquid and
//      records the rest as pending, payable only to that LP after a refill.

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

// balance + outstanding − (principal + Σclaimable + pending withdrawals):
// dust the accumulator kept back, which must never go negative.
async function slack(pool, token, lps) {
  const bal = await token.balanceOf(await pool.getAddress());
  let owed = (await pool.totalPrincipal()) + (await pool.totalPendingWithdrawals());
  for (const lp of lps) owed += await pool.claimable(lp.address);
  return bal + (await pool.outstanding()) - owed;
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

      // The fill sells the LPs' own money: $10 of the $25 leaves for the treasury.
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("order-1"));

      const want = ["0.2", "0.4", "0.1", "0.1", "0.2"];
      for (let i = 0; i < 5; i++) expect(await pool.claimable(lps[i].address)).to.equal(u(want[i]));
      expect(await pool.outstanding()).to.equal(u(11));
      expect(await pool.liquid()).to.equal(u(15));
      expect(await ctx.token.balanceOf(treasury.address)).to.equal(u(10));
    });

    it("a depositor arriving after a fill earns nothing from it", async function () {
      const { pool, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("a"));
      await pool.connect(lps[1]).deposit(u(100));
      expect(await pool.claimable(lps[1].address)).to.equal(0);
      await pool.connect(operator).fill(treasury.address, u(20), u(2), refOf("b"));
      expect(await pool.claimable(lps[0].address)).to.equal(u(2));
      expect(await pool.claimable(lps[1].address)).to.equal(u(1));
    });

    it("earnings survive a partial withdraw and are claimable to self, another address, or by anyone for the LP", async function () {
      const { pool, token, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(3), refOf("a"));
      await pool.connect(provider).refill(u(53), refOf("r"));
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

  describe("liquidity and pending withdrawals (no collateral)", function () {
    it("a fill draws the LPs' money and records amount + spread as outstanding", async function () {
      const { pool, token, operator, treasury, lps, poolAddr } = ctx;
      await pool.connect(lps[0]).deposit(u(300));
      await pool.connect(lps[1]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(200), u(4), refOf("a"));
      expect(await token.balanceOf(poolAddr)).to.equal(u(200));
      expect(await token.balanceOf(treasury.address)).to.equal(u(200));
      expect(await pool.outstanding()).to.equal(u(204));
      expect(await pool.liquid()).to.equal(u(200));
      expect(await pool.claimable(lps[0].address)).to.equal(u(3));
      expect(await pool.claimable(lps[1].address)).to.equal(u(1));
      expect(await slack(pool, token, lps.slice(0, 6))).to.equal(0);
    });

    it("refuses a fill larger than the liquid balance", async function () {
      const { pool, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(90), u(1), refOf("a"));
      await expect(pool.connect(operator).fill(treasury.address, u("10.000000000000000001"), 0, refOf("b")))
        .to.be.revertedWith("Insufficient liquidity");
      // The spread is owed, not paid out, so it needs no backing.
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("c"));
      expect(await pool.liquid()).to.equal(0);
      expect(await pool.outstanding()).to.equal(u(102));
    });

    it("a refill restores liquidity, only up to outstanding, with its own single-use refs", async function () {
      const { pool, operator, provider, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(1), refOf("a"));
      await expect(pool.connect(operator).refill(u(1), refOf("r1"))).to.be.revertedWith("Not collateral provider");
      await pool.connect(provider).refill(u(31), refOf("r1"));
      expect(await pool.liquid()).to.equal(u(81));
      await expect(pool.connect(provider).refill(1, refOf("r1"))).to.be.revertedWith("Bad or used ref");
      await expect(pool.connect(provider).refill(u(21), refOf("r2"))).to.be.revertedWith("Bad amount");
      await pool.connect(provider).refill(u(20), refOf("r2"));
      expect(await pool.outstanding()).to.equal(0);
      expect(await pool.liquid()).to.equal(u(101));
      // Every LP claims and withdraws in full; nothing is left over.
      await pool.connect(lps[0]).claim();
      await pool.connect(lps[0]).withdraw(u(100));
      expect(await ctx.token.balanceOf(ctx.poolAddr)).to.equal(0);
    });

    it("a fully liquid withdraw pays everything now and records nothing pending", async function () {
      const { pool, token, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      const before = await token.balanceOf(lps[0].address);
      await expect(pool.connect(lps[0]).withdraw(u(100)))
        .to.emit(pool, "Withdrawn").withArgs(lps[0].address, u(100))
        .and.not.to.emit(pool, "WithdrawalPending");
      expect(await token.balanceOf(lps[0].address)).to.equal(before + u(100));
      expect(await pool.pendingWithdrawal(lps[0].address)).to.equal(0);
    });

    it("a partly liquid withdraw pays what it can, records the rest pending, and the rest stops earning", async function () {
      const { pool, token, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(lps[1]).deposit(u(110));
      await pool.connect(operator).fill(treasury.address, u(180), 0, refOf("a")); // 30 liquid left
      const before = await token.balanceOf(lps[0].address);
      await expect(pool.connect(lps[0]).withdraw(u(100)))
        .to.emit(pool, "Withdrawn").withArgs(lps[0].address, u(30))
        .and.to.emit(pool, "WithdrawalPending").withArgs(lps[0].address, u(70));
      expect(await token.balanceOf(lps[0].address)).to.equal(before + u(30));
      expect(await pool.principal(lps[0].address)).to.equal(0);
      expect(await pool.totalPrincipal()).to.equal(u(110));
      expect(await pool.pendingWithdrawal(lps[0].address)).to.equal(u(70));
      expect(await pool.totalPendingWithdrawals()).to.equal(u(70));
      expect(await pool.liquid()).to.equal(0);

      // Nothing liquid: the next withdraw is all pending, with no Withdrawn event.
      await expect(pool.connect(lps[1]).withdraw(u(10)))
        .to.emit(pool, "WithdrawalPending").withArgs(lps[1].address, u(10))
        .and.not.to.emit(pool, "Withdrawn");
      expect(await pool.totalPendingWithdrawals()).to.equal(u(80));

      // A spread now goes only to the principal still in: lps[0]'s pending earns nothing.
      await pool.connect(ctx.provider).refill(u(180), refOf("r"));
      await pool.connect(operator).fill(treasury.address, u(10), u(1), refOf("b"));
      expect(await pool.claimable(lps[0].address)).to.equal(0);
      expect(await pool.claimable(lps[1].address)).to.equal(u(1));
      expect(await slack(pool, token, lps.slice(0, 6))).to.equal(0);
    });

    it("payPending pays exactly the pending amount, only to the LP, by operator, provider or owner", async function () {
      const { pool, token, owner, provider, operator, treasury, lps } = ctx;
      for (const lp of [lps[0], lps[1], lps[2]]) await pool.connect(lp).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(300), 0, refOf("a"));
      for (const lp of [lps[0], lps[1], lps[2]]) await pool.connect(lp).withdraw(u(100));
      expect(await pool.totalPendingWithdrawals()).to.equal(u(300));

      await expect(pool.connect(lps[0]).payPending(lps[0].address)).to.be.revertedWith("Not operator, provider or owner");
      // Nothing to pay yet: a harmless no-op.
      await expect(pool.connect(operator).payPending(lps[0].address)).not.to.emit(pool, "WithdrawalPaid");

      await pool.connect(provider).refill(u(150), refOf("r1"));
      const treasuryBefore = await token.balanceOf(treasury.address);
      const opBefore = await token.balanceOf(operator.address);
      const b0 = await token.balanceOf(lps[0].address);
      await expect(pool.connect(operator).payPending(lps[0].address))
        .to.emit(pool, "WithdrawalPaid").withArgs(lps[0].address, u(100));
      expect(await token.balanceOf(lps[0].address)).to.equal(b0 + u(100));
      expect(await token.balanceOf(operator.address)).to.equal(opBefore);
      expect(await token.balanceOf(treasury.address)).to.equal(treasuryBefore);
      expect(await pool.pendingWithdrawal(lps[0].address)).to.equal(0);
      // Idempotent: a retry pays nothing more.
      await expect(pool.connect(operator).payPending(lps[0].address)).not.to.emit(pool, "WithdrawalPaid");
      expect(await token.balanceOf(lps[0].address)).to.equal(b0 + u(100));

      // Only 50 left in the pool: the provider pays lps[1] partly.
      const b1 = await token.balanceOf(lps[1].address);
      await pool.connect(provider).payPending(lps[1].address);
      expect(await token.balanceOf(lps[1].address)).to.equal(b1 + u(50));
      expect(await pool.pendingWithdrawal(lps[1].address)).to.equal(u(50));

      await pool.connect(provider).refill(u(150), refOf("r2"));
      await pool.connect(owner).payPending(lps[1].address);
      await pool.connect(owner).payPending(lps[2].address);
      expect(await pool.totalPendingWithdrawals()).to.equal(0);
      expect(await token.balanceOf(ctx.poolAddr)).to.equal(0);
    });

    it("fills and claims cannot use liquidity reserved for pending withdrawals", async function () {
      const { pool, provider, operator, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(lps[1]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(190), u(10), refOf("a")); // 10 liquid
      await pool.connect(lps[0]).withdraw(u(100)); // 10 now, 90 pending
      await pool.connect(provider).refill(u(60), refOf("r1")); // balance 60, all reserved
      expect(await pool.liquid()).to.equal(0);
      await expect(pool.connect(operator).fill(treasury.address, u(1), 0, refOf("b")))
        .to.be.revertedWith("Insufficient liquidity");
      await expect(pool.connect(lps[1]).claim()).to.be.revertedWith("Insufficient liquidity");
      await expect(pool.connect(lps[0]).claimTo(lps[5].address)).to.be.revertedWith("Insufficient liquidity");
      await expect(pool.connect(lps[5]).claimFor(lps[0].address)).to.be.revertedWith("Insufficient liquidity");

      await pool.connect(provider).refill(u(140), refOf("r2")); // balance 200, 90 reserved
      expect(await pool.liquid()).to.equal(u(110));
      await pool.connect(lps[1]).claim();
      await pool.connect(lps[0]).claim();
      await pool.connect(operator).payPending(lps[0].address);
      await pool.connect(lps[1]).withdraw(u(100));
      expect(await ctx.token.balanceOf(ctx.poolAddr)).to.equal(0);
    });

    it("withdraw, claim and payPending work while paused and after the LP is delisted; deposit and fill do not", async function () {
      const { pool, provider, operator, guardian, treasury, lps } = ctx;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(1), refOf("a"));
      await pool.connect(guardian).pause();
      await pool.connect(operator).setDepositor(lps[0].address, false);

      await expect(pool.connect(lps[0]).deposit(u(10))).to.be.reverted;
      await expect(pool.connect(operator).fill(treasury.address, u(1), 0, refOf("b"))).to.be.reverted;
      await pool.connect(lps[0]).claim();
      await pool.connect(lps[0]).withdraw(u(100)); // 49 now, 51 pending
      expect(await pool.principal(lps[0].address)).to.equal(0);
      await pool.connect(provider).refill(u(51), refOf("r"));
      await pool.connect(operator).payPending(lps[0].address);
      expect(await pool.pendingWithdrawal(lps[0].address)).to.equal(0);
    });

    it("keeps the invariant over a random sequence of every operation, and everyone exits after refills", async function () {
      const { pool, token, provider, operator, treasury, lps, poolAddr } = ctx;
      const actors = lps.slice(0, 6);
      let seed = 0x5eed;
      const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
      let refs = 0;
      // Each fill's accumulator step rounds down by under one wei per whole
      // token of principal; each harvest by under one wei more.
      let dustBound = 0n;
      for (let step = 0; step < 300; step++) {
        const lp = actors[rnd(actors.length)];
        const amt = u(1 + rnd(200));
        // Weighted so the pool often runs short and withdrawals go pending.
        const op = rnd(9);
        try {
          if (op <= 1) await pool.connect(lp).deposit(amt);
          else if (op <= 3) await pool.connect(lp).withdraw(amt);
          else if (op === 4) await pool.connect(lp).claim();
          else if (op <= 6) {
            const tp = await pool.totalPrincipal();
            await pool.connect(operator).fill(treasury.address, amt * 3n, (amt * 3n) / 100n, refOf(`f${refs++}`));
            dustBound += tp / 10n ** 18n + 1n;
          }
          else if (op === 7) await pool.connect(provider).refill(amt, refOf(`r${refs++}`));
          else await pool.connect(operator).payPending(lp.address);
        } catch (_) { /* refused operations are part of the walk */ }
        dustBound += BigInt(actors.length);
        const s = await slack(pool, token, actors);
        expect(s).to.be.gte(0);
        expect(s).to.be.lte(dustBound); // rounding dust only
      }
      // The company refills everything it owes; then everyone exits in full.
      const owed = await pool.outstanding();
      if (owed > 0n) await pool.connect(provider).refill(owed, refOf("final"));
      for (const lp of actors) {
        const p = await pool.principal(lp.address);
        if (p > 0n) await pool.connect(lp).withdraw(p);
        if ((await pool.claimable(lp.address)) > 0n) await pool.connect(lp).claim();
        if ((await pool.pendingWithdrawal(lp.address)) > 0n) await pool.connect(operator).payPending(lp.address);
      }
      for (const lp of actors) expect(await pool.pendingWithdrawal(lp.address)).to.equal(0);
      expect(await pool.totalPendingWithdrawals()).to.equal(0);
      expect(await pool.totalPrincipal()).to.equal(0);
      expect(await token.balanceOf(poolAddr)).to.be.lte(dustBound); // dust only
      expect(await pool.outstanding()).to.equal(0);
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
      // Fills here are larger than the LPs' wei-sized stakes: seed the pool
      // with a surplus the books do not owe anyone, and check it survives.
      const seedAmt = 1000n;
      await token.connect(provider).transfer(await pool.getAddress(), seedAmt);
      let n = 0;
      const fill = (s) => pool.connect(operator).fill(treasury.address, 100n, s, refOf(`f${n++}`));
      await pool.connect(B).deposit(5n); await fill(4n); await pool.connect(A).deposit(2n); await fill(2n); await fill(1n);
      await pool.connect(A).deposit(3n); await pool.connect(B).deposit(2n); await pool.connect(A).deposit(2n); await fill(3n);
      await pool.connect(A).withdraw(1n); await fill(2n);
      const credited = 4n + 2n + 1n + 3n + 2n;
      expect((await pool.claimable(A.address)) + (await pool.claimable(B.address))).to.be.lte(credited);
      expect(await slack(pool, token, [A, B])).to.be.gte(0n);
      await pool.connect(provider).refill(await pool.outstanding(), refOf("r"));
      await pool.connect(A).withdraw(await pool.principal(A.address));
      await pool.connect(B).withdraw(await pool.principal(B.address));
      await pool.connect(A).claim();
      await pool.connect(B).claim();
      // What is left is the seed plus the dust the accumulator kept back.
      const left = await token.balanceOf(await pool.getAddress());
      expect(left).to.be.gte(seedAmt);
      expect(left - seedAmt).to.be.lte(credited);
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
    });

    it("a fresh deposit earns at once but cannot be withdrawn until the lock ends; earnings can be claimed", async function () {
      const { pool, operator, treasury, lps } = c;
      await pool.connect(lps[0]).deposit(u(100));
      await pool.connect(operator).fill(treasury.address, u(50), u(1), refOf("a"));
      expect(await pool.claimable(lps[0].address)).to.equal(u(1)); // earning from the deposit on
      expect(await pool.withdrawable(lps[0].address)).to.equal(0);
      await expect(pool.connect(lps[0]).withdraw(u(1))).to.be.revertedWith("Deposit still locked");
      await pool.connect(lps[0]).claim(); // earnings are never locked
      await time.increase(DAY);
      expect(await pool.withdrawable(lps[0].address)).to.equal(u(100));
      await pool.connect(c.provider).refill(u(51), refOf("r"));
      await pool.connect(lps[0]).withdraw(u(100));
      expect(await pool.pendingWithdrawal(lps[0].address)).to.equal(0);
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
      // Liquidity for the cap tests beyond the one LP's 1000: a surplus the
      // books owe no one (it stands in for refilled money).
      await ctx.token.connect(ctx.provider).transfer(ctx.poolAddr, u(20000));
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
    // could credit the whole pool to LPs as "spread" in one 1-wei fill.
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
      await expect(pool.connect(stranger).payPending(stranger.address)).to.be.revertedWith("Not operator, provider or owner");
      await expect(pool.connect(stranger).setDepositor(stranger.address, true)).to.be.revertedWith("Not operator or owner");
      await expect(pool.connect(stranger).refill(1, refOf("a"))).to.be.revertedWith("Not collateral provider");
      await expect(pool.connect(stranger).pause()).to.be.revertedWith("Not owner or guardian");
      await expect(pool.connect(provider).setRecipient(stranger.address, true)).to.be.reverted;
      await expect(pool.connect(provider).upgradeToAndCall(stranger.address, "0x")).to.be.reverted;
    });

    it("an operator cannot burn a refill ref: refill is the refill wallet's alone", async function () {
      const { pool, provider, operator, treasury } = ctx;
      await pool.connect(operator).fill(treasury.address, u(50), u(1), refOf("planned"));
      await expect(pool.connect(operator).refill(u(10), refOf("planned"))).to.be.revertedWith("Not collateral provider");
      await pool.connect(provider).refill(u(10), refOf("planned"));
      expect(await pool.outstanding()).to.equal(u(41));
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

    it("the owner moves the refill wallet; it must never be an operator", async function () {
      const { pool, operator, guardian, lps } = ctx;
      await expect(pool.connect(lps[5]).setRoles(lps[5].address, guardian.address)).to.be.reverted;
      await expect(pool.setRoles(operator.address, guardian.address)).to.be.revertedWith("provider must not be an operator");
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

  // In-place upgrade of the deployed collateral-model pool (BSC mainnet
  // 0xA786…60f1, testnet 0x3Aa2…403C): storage is append-only.
  describe("upgrade from V1 (collateral model)", function () {
    it("validates the storage layout, keeps every balance, and runs the new rules on the old state", async function () {
      const [owner, provider, operator, guardian, treasury, a, b] = await ethers.getSigners();
      const token = await (await ethers.getContractFactory("MockERC20")).deploy("Tether USD", "USDT", 18);
      const V1 = await ethers.getContractFactory("TsaLiquidityPoolV1");
      const pool = await upgrades.deployProxy(V1, [{
        token: await token.getAddress(), owner: owner.address, collateralProvider: provider.address,
        operator: operator.address, guardian: guardian.address, recipient: treasury.address,
        minDeposit: u(5), maxPerLp: u(5000), totalCap: u(50000), maxFillPerTx: u(1000),
        fillWindow: 24 * HOUR, fillWindowCap: u(10000), maxSpreadBps: 1000, withdrawLock: 24 * HOUR,
      }], { kind: "uups" });
      const addr = await pool.getAddress();
      for (const s of [provider, a, b]) {
        await token.mint(s.address, u(10000));
        await token.connect(s).approve(addr, ethers.MaxUint256);
      }
      for (const s of [a, b]) await pool.connect(operator).setDepositor(s.address, true);
      await pool.connect(a).deposit(u(300));
      await pool.connect(b).deposit(u(100));
      await pool.connect(provider).postCollateral(u(500));
      await pool.connect(operator).fill(treasury.address, u(200), u(4), refOf("v1-fill"));
      await pool.connect(provider).refill(u(100), refOf("v1-refill"));
      await pool.connect(operator).settleFromCollateral(u(4), refOf("v1-settle"));
      await pool.connect(provider).withdrawCollateral(u(396)); // free collateral, as on mainnet: 100 still backing
      const lockedUntilB = await pool.lockedUntil(b.address);
      const balBefore = await token.balanceOf(addr);

      const New = await ethers.getContractFactory("TsaLiquidityPool");
      await upgrades.validateUpgrade(V1, New, { kind: "uups" });
      await expect(upgrades.upgradeProxy(addr, New.connect(provider), { kind: "uups" })).to.be.reverted;
      const up = await upgrades.upgradeProxy(addr, New, { kind: "uups" });

      expect(await up.owner()).to.equal(owner.address);
      expect(await up.token()).to.equal(await token.getAddress());
      expect(await up.collateralProvider()).to.equal(provider.address);
      expect(await up.operators(operator.address)).to.equal(true);
      expect(await up.recipient(treasury.address)).to.equal(true);
      expect(await up.depositor(a.address)).to.equal(true);
      expect(await up.principal(a.address)).to.equal(u(300));
      expect(await up.totalPrincipal()).to.equal(u(400));
      expect(await up.claimable(a.address)).to.equal(u(3));
      expect(await up.claimable(b.address)).to.equal(u(1));
      expect(await up.outstanding()).to.equal(u(100));
      expect(await up.collateral()).to.equal(u(100));
      expect(await up.usedFillRef(refOf("v1-fill"))).to.equal(true);
      expect(await up.usedRefillRef(refOf("v1-refill"))).to.equal(true);
      expect(await up.usedSettleRef(refOf("v1-settle"))).to.equal(true);
      expect(await up.lockedUntil(b.address)).to.equal(lockedUntilB);
      expect(await up.withdrawLock()).to.equal(BigInt(24 * HOUR));
      expect(await up.maxSpreadBps()).to.equal(1000n);
      expect(await up.filledInWindow()).to.equal(u(204));
      expect(await up.totalPendingWithdrawals()).to.equal(0);
      expect(await up.liquid()).to.equal(balBefore);

      // The old entry points are gone.
      for (const sig of ["postCollateral(uint256)", "withdrawCollateral(uint256)", "settleFromCollateral(uint256,bytes32)", "freeCapacity()"]) {
        const sel = ethers.id(sig).slice(0, 10);
        await expect(provider.sendTransaction({ to: addr, data: sel + "00".repeat(64) })).to.be.reverted;
      }
      // A refill ref used before the upgrade stays used; the new rules run.
      await expect(up.connect(provider).refill(1, refOf("v1-refill"))).to.be.revertedWith("Bad or used ref");
      await time.increase(24 * HOUR);
      await up.connect(a).withdraw(u(300));
      await up.connect(a).claim();
      expect(await up.principal(a.address)).to.equal(0);
    });
  });
});
