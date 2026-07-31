// The operator debit path, which is how a utility bill gets paid in MCGP:
// debitPremium on order → settleDebit when the vend succeeds → reverseDebit when
// it fails. Entirely operator-signed, so the user needs no gas and signs nothing.
//
// Two properties carry the weight here:
//
//   1. A debit draws credited-first then BOUGHT, so a user who bought premium
//      with USDC can still pay a bill. That is a deliberate weakening — V1's
//      operator could never reach bought premium — so the caps below are what
//      bound it.
//   2. A reversal restores each bucket EXACTLY as drawn. Returning a
//      bought-funded debit as credited would convert self-exitable MCGP into
//      spend-only, so a failed bill would quietly cost the user their withdrawal
//      rights.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));

// minCredit, maxCreditPerTx, creditWindow, creditWindowCap, maxDebitPerTx, debitWindow, debitWindowCap
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000), mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — operator debit (bills)", function () {
  let swap, mcgpToken, usdcToken;
  let owner, alice, operator, guardian;

  async function deploy(caps = DEFAULT_CAPS) {
    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    const s = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...caps],
      { kind: "uups" },
    );
    await s.waitForDeployment();
    await s.addPhase(PREMIUM_BUY, 20000, "premium buy");
    await s.addOperator(operator.address);
    await s.addGuardian(guardian.address);
    return s;
  }

  beforeEach(async function () {
    [owner, alice, operator, guardian] = await ethers.getSigners();
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await mcgpToken.waitForDeployment();
    await usdcToken.waitForDeployment();
    await mcgpToken.mint(owner.address, mcgp(1_000_000));
    await usdcToken.mint(alice.address, usdc(1_000_000));
    swap = await deploy();
  });

  async function fund(amount) {
    await mcgpToken.approve(swap.target, amount);
    await swap.fund(await mcgpToken.getAddress(), amount);
  }
  async function giveCredited(to, amount) {
    await fund(amount);
    await swap.connect(operator).creditPremium(to, amount, refOf(`credit:${to}:${amount}`));
  }
  async function giveBought(signer, amount) {
    await fund(amount);
    const cost = await swap.quote(PREMIUM_BUY, amount);
    await usdcToken.connect(signer).approve(swap.target, cost);
    await swap.connect(signer).buyPremium(amount, cost);
  }

  describe("bucket selection", function () {
    it("draws credited first, leaving the user's self-exitable balance intact", async function () {
      await giveCredited(alice.address, mcgp(30));
      await giveBought(alice, mcgp(70));

      await swap.connect(operator).debitPremium(alice.address, mcgp(20), refOf("d1"));

      expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(10));
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(70)); // untouched
      expect(await swap.pendingSettle(alice.address)).to.equal(mcgp(20));
      expect(await swap.debitFromBought(refOf("d1"))).to.equal(0n);
    });

    it("spills into bought only once credited is exhausted", async function () {
      await giveCredited(alice.address, mcgp(30));
      await giveBought(alice, mcgp(70));

      await swap.connect(operator).debitPremium(alice.address, mcgp(50), refOf("d2"));

      expect(await swap.premiumCredited(alice.address)).to.equal(0n);
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(50)); // 70 - 20 spill
      expect(await swap.debitFromBought(refOf("d2"))).to.equal(mcgp(20));
    });

    // The whole reason for the bucket change: bills must work for USDC buyers.
    it("a user holding ONLY bought premium can be debited", async function () {
      await giveBought(alice, mcgp(40));
      await swap.connect(operator).debitPremium(alice.address, mcgp(15), refOf("d3"));

      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(25));
      expect(await swap.debitFromBought(refOf("d3"))).to.equal(mcgp(15));
    });

    it("rejects a debit beyond the COMBINED balance", async function () {
      await giveCredited(alice.address, mcgp(10));
      await giveBought(alice, mcgp(10));
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(21), refOf("d4")))
        .to.be.revertedWith("Insufficient premium balance");
    });
  });

  describe("reversal restores provenance exactly", function () {
    it("a mixed debit comes back to the same two buckets", async function () {
      await giveCredited(alice.address, mcgp(30));
      await giveBought(alice, mcgp(70));
      await swap.connect(operator).debitPremium(alice.address, mcgp(50), refOf("r1"));

      await swap.connect(operator).reverseDebit(refOf("r1"));

      // Not 30/70 by luck — by reconstruction from the recorded split.
      expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(30));
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(70));
      expect(await swap.pendingSettle(alice.address)).to.equal(0n);
    });

    // The failure this guards against: a bought-funded debit returned as
    // credited would leave the user unable to withdraw or sell what they bought.
    it("a bought-funded reversal does NOT downgrade to spend-only", async function () {
      await giveBought(alice, mcgp(40));
      await swap.connect(operator).debitPremium(alice.address, mcgp(40), refOf("r2"));
      expect(await swap.premiumBought(alice.address)).to.equal(0n);

      await swap.connect(operator).reverseDebit(refOf("r2"));

      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(40));
      expect(await swap.premiumCredited(alice.address)).to.equal(0n);
      // Still self-exitable, which is the point.
      await expect(swap.connect(alice).withdrawPremiumToWallet(mcgp(40)))
        .to.emit(swap, "PremiumWithdrawn");
    });

    it("clears the recorded split so a ref cannot be reversed twice", async function () {
      await giveBought(alice, mcgp(10));
      await swap.connect(operator).debitPremium(alice.address, mcgp(10), refOf("r3"));
      await swap.connect(operator).reverseDebit(refOf("r3"));

      expect(await swap.debitFromBought(refOf("r3"))).to.equal(0n);
      await expect(swap.connect(operator).reverseDebit(refOf("r3"))).to.be.revertedWith("Not pending");
    });

    it("settle frees backing regardless of which bucket funded it", async function () {
      await giveBought(alice, mcgp(20));
      const accountedBefore = await swap.accountedMcgp();
      await swap.connect(operator).debitPremium(alice.address, mcgp(20), refOf("s1"));
      expect(await swap.accountedMcgp()).to.equal(accountedBefore); // not yet freed

      await swap.connect(operator).settleDebit(refOf("s1"));
      expect(await swap.accountedMcgp()).to.equal(accountedBefore - mcgp(20));
      expect(await swap.pendingSettle(alice.address)).to.equal(0n);
    });
  });

  describe("caps bound a compromised operator", function () {
    it("enforces the per-tx ceiling", async function () {
      swap = await deploy([0, mcgp(1e9), 3600, mcgp(1e9), mcgp(10), 3600, mcgp(1000)]);
      await giveCredited(alice.address, mcgp(100));
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(11), refOf("c1")))
        .to.be.revertedWith("Over per-tx debit cap");
      await swap.connect(operator).debitPremium(alice.address, mcgp(10), refOf("c2"));
    });

    // A per-tx cap alone bounds nothing — many small debits get there just as
    // fast. This is the control that actually limits a stolen key.
    it("enforces the rolling window across many small debits", async function () {
      swap = await deploy([0, mcgp(1e9), 3600, mcgp(1e9), mcgp(10), 3600, mcgp(25)]);
      await giveCredited(alice.address, mcgp(100));

      for (let i = 0; i < 2; i++) {
        await swap.connect(operator).debitPremium(alice.address, mcgp(10), refOf(`w${i}`));
      }
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(10), refOf("w-over")))
        .to.be.revertedWith("Over debit window cap");

      await ethers.provider.send("evm_increaseTime", [3601]);
      await ethers.provider.send("evm_mine", []);

      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(10), refOf("w-next")))
        .to.emit(swap, "PremiumDebitPending");
    });

    it("setDebitCaps is owner-only and rejects zeroes", async function () {
      await expect(swap.connect(alice).setDebitCaps(mcgp(1), 3600, mcgp(1)))
        .to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      await expect(swap.setDebitCaps(0, 3600, mcgp(1))).to.be.revertedWith("maxDebitPerTx must be > 0");
      await expect(swap.setDebitCaps(mcgp(1), 0, mcgp(1))).to.be.revertedWith("debitWindow must be > 0");
      await expect(swap.setDebitCaps(mcgp(1), 3600, 0)).to.be.revertedWith("debitWindowCap must be > 0");
    });

    it("a guardian can pause settlement while leaving reversals open", async function () {
      await giveCredited(alice.address, mcgp(50));
      await swap.connect(operator).debitPremium(alice.address, mcgp(20), refOf("p1"));
      await swap.connect(operator).debitPremium(alice.address, mcgp(20), refOf("p2"));

      await swap.connect(guardian).pause();

      // Settling frees backing, so it must stop during an incident…
      await expect(swap.connect(operator).settleDebit(refOf("p1")))
        .to.be.revertedWithCustomError(swap, "EnforcedPause");
      // …but users must still be able to be made whole.
      await expect(swap.connect(operator).reverseDebit(refOf("p2")))
        .to.emit(swap, "PremiumDebitReversed");
      // And no new debits can start.
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(1), refOf("p3")))
        .to.be.revertedWithCustomError(swap, "EnforcedPause");
    });
  });

  describe("the accounting invariant holds throughout", function () {
    it("accountedMcgp tracks the sum across every debit state", async function () {
      await giveCredited(alice.address, mcgp(30));
      await giveBought(alice, mcgp(70));

      const sum = async () =>
        (await swap.premiumBought(alice.address)) +
        (await swap.premiumCredited(alice.address)) +
        (await swap.pendingSettle(alice.address));

      expect(await swap.accountedMcgp()).to.equal(await sum());
      await swap.connect(operator).debitPremium(alice.address, mcgp(50), refOf("i1"));
      expect(await swap.accountedMcgp()).to.equal(await sum());
      await swap.connect(operator).reverseDebit(refOf("i1"));
      expect(await swap.accountedMcgp()).to.equal(await sum());
      await swap.connect(operator).debitPremium(alice.address, mcgp(50), refOf("i2"));
      await swap.connect(operator).settleDebit(refOf("i2"));
      expect(await swap.accountedMcgp()).to.equal(await sum());

      const [held, owed] = await swap.backing();
      expect(held).to.be.gte(owed);
    });
  });
});
