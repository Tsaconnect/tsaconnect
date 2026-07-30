// ERC-2771 behaviour via MockForwarder.
//
// Two things are being proven here, and the second matters more:
//
//  1. Every user-facing entry point resolves the caller through _msgSender(), so
//     a relayer pays gas but the USER is debited and credited. The dangerous
//     failure is a PARTIALLY converted function — e.g. buyPremium pulling USDC
//     from the forwarder while crediting the user — so each test asserts the
//     token movement AND that the forwarder holds zero of every ledger.
//  2. No privileged role is reachable through the forwarder. Owner, guardian and
//     operator checks use raw msg.sender by design; if any of them were converted
//     to _msgSender() the forwarder could pause the contract, strip operators,
//     mint credited premium, or upgrade the implementation.
//
// MockForwarder has no signatures, nonces or deadlines — request validity is
// covered separately in McgpEntitlementSwapV2.forwarder.real.test.js.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const LEGACY_SELL = 2;

const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — ERC-2771", function () {
  let swap, mcgpToken, usdcToken, forwarder, rogueForwarder;
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

    const MockForwarder = await ethers.getContractFactory("MockForwarder");
    forwarder = await MockForwarder.deploy();
    rogueForwarder = await MockForwarder.deploy();
    await forwarder.waitForDeployment();
    await rogueForwarder.waitForDeployment();

    await swap.addPhase(PREMIUM_BUY, 20000, "premium buy");
    await swap.addPhase(PREMIUM_SELL, 18000, "premium sell");
    await swap.addPhase(LEGACY_SELL, 10000, "legacy redeem");
    await swap.addOperator(operator.address);
    await swap.addGuardian(guardian.address);
    await swap.setTrustedForwarder(await forwarder.getAddress());

    await mcgpToken.mint(owner.address, mcgp(10_000_000));
    await usdcToken.mint(owner.address, usdc(10_000_000));
    await usdcToken.mint(alice.address, usdc(1_000_000));
    await mcgpToken.mint(alice.address, mcgp(100_000));
  });

  const call = (fn, args) => swap.interface.encodeFunctionData(fn, args);
  const relay = (fn, args, from) => forwarder.relay(swap.target, call(fn, args), from.address);
  const relayVia = (fwd, fn, args, from) => fwd.relay(swap.target, call(fn, args), from.address);

  async function fundMcgp(amount) {
    await mcgpToken.approve(swap.target, amount);
    await swap.fund(await mcgpToken.getAddress(), amount);
  }
  async function fundUsdc(amount) {
    await usdcToken.approve(swap.target, amount);
    await swap.fund(await usdcToken.getAddress(), amount);
  }

  // The catch-all for a half-converted function: the relayer must never end up
  // holding entitlement of any kind.
  async function expectForwarderHoldsNothing() {
    const f = await forwarder.getAddress();
    expect(await swap.premiumBought(f)).to.equal(0n);
    expect(await swap.premiumCredited(f)).to.equal(0n);
    expect(await swap.pendingSettle(f)).to.equal(0n);
    expect(await swap.legacyEntitlement(f)).to.equal(0n);
    expect(await swap.legacyRedeemed(f)).to.equal(0n);
    expect(await mcgpToken.balanceOf(f)).to.equal(0n);
    expect(await usdcToken.balanceOf(f)).to.equal(0n);
  }

  describe("relayed user actions credit the user, not the relayer", function () {
    it("buyPremium: USDC leaves the USER, premiumBought credits the USER", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      const before = await usdcToken.balanceOf(alice.address);

      await relay("buyPremium", [mcgp(500), cost], alice);

      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(500));
      expect(await usdcToken.balanceOf(alice.address)).to.equal(before - cost);
      await expectForwarderHoldsNothing();
    });

    it("spend: debits the USER and credits the recipient", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);

      await relay("spend", [bob.address, mcgp(100), refOf("relayed:spend")], alice);

      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(400));
      expect(await swap.premiumBought(bob.address)).to.equal(mcgp(100));
      await expectForwarderHoldsNothing();
    });

    it("sellPremium: USDC goes to the USER", async function () {
      await fundMcgp(mcgp(1000));
      await fundUsdc(usdc(100_000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);

      const proceeds = await swap.quote(PREMIUM_SELL, mcgp(200));
      const before = await usdcToken.balanceOf(alice.address);
      await relay("sellPremium", [mcgp(200), proceeds], alice);

      expect(await usdcToken.balanceOf(alice.address)).to.equal(before + proceeds);
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(300));
      await expectForwarderHoldsNothing();
    });

    it("withdrawPremiumToWallet: MCGP goes to the USER", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);

      const before = await mcgpToken.balanceOf(alice.address);
      await relay("withdrawPremiumToWallet", [mcgp(200)], alice);

      expect(await mcgpToken.balanceOf(alice.address)).to.equal(before + mcgp(200));
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(300));
      await expectForwarderHoldsNothing();
    });

    it("sellLegacy: MCGP leaves the USER, USDC returns to the USER, entitlement is the USER's", async function () {
      await fundUsdc(usdc(100_000));
      await swap.seedLegacy([alice.address], [mcgp(1000)]);
      await mcgpToken.connect(alice).approve(swap.target, mcgp(1000));

      const proceeds = await swap.quote(LEGACY_SELL, mcgp(400));
      const mcgpBefore = await mcgpToken.balanceOf(alice.address);
      const usdcBefore = await usdcToken.balanceOf(alice.address);

      await relay("sellLegacy", [mcgp(400), proceeds], alice);

      expect(await mcgpToken.balanceOf(alice.address)).to.equal(mcgpBefore - mcgp(400));
      expect(await usdcToken.balanceOf(alice.address)).to.equal(usdcBefore + proceeds);
      expect(await swap.legacyRedeemed(alice.address)).to.equal(mcgp(400));
      await expectForwarderHoldsNothing();
    });
  });

  describe("no privileged role is reachable through the forwarder", function () {
    it("owner-only functions revert even when the forwarder claims to be the owner", async function () {
      for (const [fn, args] of [
        ["addOperator", [bob.address]],
        ["addGuardian", [bob.address]],
        ["removeGuardian", [guardian.address]],
        ["seedLegacy", [[bob.address], [mcgp(1)]]],
        ["setCreditCaps", [0, mcgp(1), 3600, mcgp(1)]],
        ["addPhase", [PREMIUM_BUY, 1, "x"]],
        ["setActivePhase", [PREMIUM_BUY, 0]],
        ["updatePhasePrice", [PREMIUM_BUY, 0, 1]],
        ["unpause", []],
        ["setFeeManager", [bob.address]],
      ]) {
        await expect(relay(fn, args, owner), `${fn} must not be relayable`)
          .to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      }
    });

    it("setTrustedForwarder is not reachable through the forwarder itself", async function () {
      await expect(relay("setTrustedForwarder", [bob.address], owner))
        .to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      expect(await swap.trustedForwarder()).to.equal(await forwarder.getAddress());
    });

    it("upgradeToAndCall is not reachable through the forwarder", async function () {
      const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
      const nextImpl = await Swap.deploy();
      await nextImpl.waitForDeployment();
      await expect(relay("upgradeToAndCall", [await nextImpl.getAddress(), "0x"], owner))
        .to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
    });

    it("every operator function reverts through the forwarder", async function () {
      await fundMcgp(mcgp(10_000));
      await swap.connect(operator).creditPremium(alice.address, mcgp(100), refOf("seed"));
      await swap.connect(operator).debitPremium(alice.address, mcgp(50), refOf("debit"));

      for (const [fn, args] of [
        ["creditPremium", [alice.address, mcgp(1), refOf("relayed:credit")]],
        ["debitPremium", [alice.address, mcgp(1), refOf("relayed:debit")]],
        ["settleDebit", [refOf("debit")]],
        ["reverseDebit", [refOf("debit")]],
      ]) {
        await expect(relay(fn, args, operator), `${fn} must not be relayable`)
          .to.be.revertedWith("Not operator");
      }
    });

    it("guardian functions (pause, removeOperator) revert through the forwarder", async function () {
      await expect(relay("pause", [], guardian)).to.be.revertedWith("Not owner/guardian");
      await expect(relay("removeOperator", [operator.address], guardian)).to.be.revertedWith("Not owner/guardian");
      expect(await swap.paused()).to.equal(false);
      expect(await swap.operators(operator.address)).to.equal(true);
    });
  });

  describe("only the configured forwarder is trusted", function () {
    it("an untrusted contract appending 20 bytes cannot spoof a sender", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);

      // The rogue forwarder's appended bytes are ignored, so _msgSender() is the
      // rogue contract itself — which holds no premium.
      await expect(relayVia(rogueForwarder, "spend", [bob.address, mcgp(1), refOf("spoof")], alice))
        .to.be.revertedWith("Insufficient premium balance");
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(500));
    });

    it("clearing the forwarder disables meta-transactions entirely", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);

      await swap.setTrustedForwarder(ethers.ZeroAddress);

      await expect(relay("spend", [bob.address, mcgp(1), refOf("after-clear")], alice))
        .to.be.revertedWith("Insufficient premium balance");
    });

    it("direct (non-relayed) calls are unaffected while a forwarder is set", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(swap.target, cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);
      await swap.connect(alice).spend(bob.address, mcgp(10), refOf("direct"));
      expect(await swap.premiumBought(bob.address)).to.equal(mcgp(10));
    });

    it("emits TrustedForwarderUpdated with the previous and current address", async function () {
      await expect(swap.setTrustedForwarder(bob.address))
        .to.emit(swap, "TrustedForwarderUpdated")
        .withArgs(await forwarder.getAddress(), bob.address);
    });
  });
});
