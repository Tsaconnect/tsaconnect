// The remaining `require` false-paths, plus one real reentrancy attempt.
//
// Coverage-driven: every branch below was reachable but unexercised after the
// parity, guards, forwarder and context suites. A `require` nobody tests is a
// `require` a refactor can delete for free.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const LEGACY_SELL = 2;

const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));
const ZERO = ethers.ZeroAddress;
const ZERO_REF = "0x" + "00".repeat(32);
// minCredit, maxCreditPerTx, creditWindow, creditWindowCap, maxDebitPerTx, debitWindow, debitWindowCap
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000), mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — remaining revert paths", function () {
  let swap, mcgpToken, usdcToken;
  let owner, alice, bob, operator, guardian;

  beforeEach(async function () {
    [owner, alice, bob, operator, guardian] = await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await mcgpToken.waitForDeployment();
    await usdcToken.waitForDeployment();

    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    swap = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
      { kind: "uups" },
    );
    await swap.waitForDeployment();

    await swap.addPhase(PREMIUM_BUY, 20000, "premium buy");
    await swap.addPhase(PREMIUM_SELL, 18000, "premium sell");
    await swap.addPhase(LEGACY_SELL, 10000, "legacy redeem");
    await swap.addOperator(operator.address);
    await swap.addGuardian(guardian.address);

    await mcgpToken.mint(owner.address, mcgp(10_000_000));
    await usdcToken.mint(owner.address, usdc(10_000_000));
    await usdcToken.mint(alice.address, usdc(1_000_000));
    await mcgpToken.mint(alice.address, mcgp(100_000));
  });

  async function fundMcgp(amount) {
    await mcgpToken.approve(swap.target, amount);
    await swap.fund(await mcgpToken.getAddress(), amount);
  }

  describe("zero-amount guards", function () {
    it("every value-moving entry point rejects a zero amount", async function () {
      await expect(swap.connect(alice).buyPremium(0, 0)).to.be.revertedWith("Amount must be > 0");
      await expect(swap.connect(alice).sellPremium(0, 0)).to.be.revertedWith("Amount must be > 0");
      await expect(swap.connect(alice).sellLegacy(0, 0)).to.be.revertedWith("Amount must be > 0");
      await expect(swap.connect(alice).withdrawPremiumToWallet(0)).to.be.revertedWith("Amount must be > 0");
      await expect(swap.connect(operator).creditPremium(alice.address, 0, refOf("z"))).to.be.revertedWith("Amount must be > 0");
      await expect(swap.connect(operator).debitPremium(alice.address, 0, refOf("z"))).to.be.revertedWith("Amount must be > 0");
      await expect(swap.fund(await mcgpToken.getAddress(), 0)).to.be.revertedWith("Amount must be > 0");
    });
  });

  describe("dust that prices to zero USDC is rejected, not silently free", function () {
    // 1 wei of MCGP * 20000 / 1e18 floors to 0 USDC. Without these guards a user
    // could mint premium, or redeem legacy, for nothing.
    it("buyPremium rejects an amount that quotes to zero USDC", async function () {
      await fundMcgp(mcgp(1000));
      expect(await swap.quote(PREMIUM_BUY, 1n)).to.equal(0n);
      await expect(swap.connect(alice).buyPremium(1n, 0)).to.be.revertedWith("USDC amount must be > 0");
    });

    it("sellPremium rejects an amount that quotes to zero USDC", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(10));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(10), cost);
      await expect(swap.connect(alice).sellPremium(1n, 0)).to.be.revertedWith("USDC amount must be > 0");
    });

    it("sellLegacy rejects an amount that quotes to zero USDC", async function () {
      await swap.seedLegacy([alice.address], [mcgp(10)]);
      await mcgpToken.connect(alice).approve(swap.target, mcgp(10));
      await expect(swap.connect(alice).sellLegacy(1n, 0)).to.be.revertedWith("USDC amount must be > 0");
    });
  });

  describe("operator argument validation", function () {
    it("creditPremium rejects the zero user and the zero ref", async function () {
      await fundMcgp(mcgp(1000));
      await expect(swap.connect(operator).creditPremium(ZERO, mcgp(1), refOf("v"))).to.be.revertedWith("Invalid user");
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1), ZERO_REF)).to.be.revertedWith("Invalid ref");
    });

    it("debitPremium rejects the zero ref and an over-draw", async function () {
      await fundMcgp(mcgp(1000));
      await swap.connect(operator).creditPremium(alice.address, mcgp(10), refOf("d:seed"));
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(1), ZERO_REF)).to.be.revertedWith("Invalid ref");
      // Message changed with the bucket rule: a debit may now draw on bought too,
      // so the shortfall is against the COMBINED balance.
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(11), refOf("d:over")))
        .to.be.revertedWith("Insufficient premium balance");
    });

    it("debitPremium is blocked while paused", async function () {
      await fundMcgp(mcgp(1000));
      await swap.connect(operator).creditPremium(alice.address, mcgp(10), refOf("d:p"));
      await swap.pause();
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(1), refOf("d:p2")))
        .to.be.revertedWithCustomError(swap, "EnforcedPause");
    });
  });

  describe("withdraw", function () {
    it("rejects a USDC withdrawal beyond the balance", async function () {
      await expect(swap.withdraw(await usdcToken.getAddress(), owner.address, usdc(1)))
        .to.be.revertedWith("Insufficient USDC");
    });
  });

  describe("legacyRemaining", function () {
    it("floors at zero rather than underflowing", async function () {
      expect(await swap.legacyRemaining(bob.address)).to.equal(0n); // never seeded
      await swap.seedLegacy([alice.address], [mcgp(100)]);
      expect(await swap.legacyRemaining(alice.address)).to.equal(mcgp(100));
    });
  });

  describe("renounceOwnership", function () {
    it("rejects a non-owner before it even reaches the disabled revert", async function () {
      await expect(swap.connect(alice).renounceOwnership())
        .to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
    });
  });

  describe("reentrancy", function () {
    it("a token that re-enters withdrawPremiumToWallet cannot double-spend", async function () {
      // Stand up a swap whose MCGP is a hostile token that calls back in on transfer.
      const Reentrant = await ethers.getContractFactory("ReentrantERC20");
      const evilMcgp = await Reentrant.deploy("Evil MCGP", "eMCGP", 18);
      await evilMcgp.waitForDeployment();

      const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
      const evilSwap = await upgrades.deployProxy(
        Swap,
        [owner.address, await evilMcgp.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
        { kind: "uups" },
      );
      await evilSwap.waitForDeployment();
      await evilSwap.addPhase(PREMIUM_BUY, 20000, "premium buy");

      await evilMcgp.mint(owner.address, mcgp(10_000));
      await evilMcgp.approve(evilSwap.target, mcgp(10_000));
      await evilSwap.fund(await evilMcgp.getAddress(), mcgp(10_000));

      const cost = await evilSwap.quote(PREMIUM_BUY, mcgp(100));
      await usdcToken.connect(alice).approve(evilSwap.target, cost);
      await evilSwap.connect(alice).buyPremium(mcgp(100), cost);

      // On the next outgoing transfer, re-enter withdrawPremiumToWallet.
      const reenter = evilSwap.interface.encodeFunctionData("withdrawPremiumToWallet", [mcgp(50)]);
      await evilMcgp.armAttack(await evilSwap.getAddress(), reenter);

      await expect(evilSwap.connect(alice).withdrawPremiumToWallet(mcgp(50))).to.be.reverted;

      // Nothing moved: the guard reverted the whole transaction.
      expect(await evilSwap.premiumBought(alice.address)).to.equal(mcgp(100));
    });
  });
});
