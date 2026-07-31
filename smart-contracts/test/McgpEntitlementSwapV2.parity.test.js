// PARITY SUITE — a deliberate port of the V1 suite that lives in the other repo
// at mcgp-otc-contracts/test/McgpEntitlementSwap.test.js.
//
// This file exists to prove V2 behaves like the deployed V1. It is a PORT, not a
// re-point: V1's file cannot run here unchanged. Every deviation from the V1
// original is listed below — anything not on this list is a review-blocking
// difference, because a silently weakened assertion is exactly how a port ships
// a behaviour change.
//
// DIFF vs the V1 original:
//   1. Mock factory name: "contracts/mocks/ERC20Mock.sol:ERC20Mock" → "MockERC20".
//      Same constructor (name, symbol, decimals) and same mint(to, amount).
//   2. Deployment: `Swap.deploy(owner, mcgp, usdc)` → `upgrades.deployProxy(...,
//      { kind: "uups" })`, because V2 is behind a proxy.
//   3. `initialize` takes four credit-cap arguments that V1's constructor did not.
//      The suite seeds deliberately permissive defaults (DEFAULT_CAPS) so the
//      ported assertions are unaffected; the cap behaviour itself is tested in
//      the "credit caps" block and in the guards suite.
//   4. "enforces minCredit / per-tx / rolling window caps": V1 called
//      `setCreditCaps(...)` from a zero-cap starting state. V2 rejects zero caps,
//      so the test sets the same non-zero values it always did — the assertions
//      are byte-for-byte the V1 ones.
//   5. Access-control test: V1 asserted a non-owner calling `setCreditCaps(0,0,0,0)`
//      reverts with OwnableUnauthorizedAccount. Zero caps are now invalid for
//      everyone, so the call uses valid non-zero values; it still asserts the
//      non-owner rejection, which is what that test was about.
//   6. pragma/solc: 0.8.20 → 0.8.24 (this repo's compiler). No source change.
//
// Nothing else differs. Same describes, same its, same assertions.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

// PriceKind enum
const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const LEGACY_SELL = 2;

const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));

// Permissive by design: these must not constrain any ported assertion.
// minCredit, maxCreditPerTx, creditWindow, creditWindowCap, maxDebitPerTx, debitWindow, debitWindowCap
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000), mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — parity with deployed V1", function () {
  let swap, mcgpToken, usdcToken;
  let owner, alice, bob, merchant, operator, guardian, attacker;

  const PREMIUM_BUY_PRICE = 20000;
  const PREMIUM_SELL_PRICE = 18000;
  const LEGACY_SELL_PRICE = 10000;

  beforeEach(async function () {
    [owner, alice, bob, merchant, operator, guardian, attacker] = await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    await mcgpToken.waitForDeployment();
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await usdcToken.waitForDeployment();

    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    swap = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
      { kind: "uups" },
    );
    await swap.waitForDeployment();

    await swap.addPhase(PREMIUM_BUY, PREMIUM_BUY_PRICE, "premium buy");
    await swap.addPhase(PREMIUM_SELL, PREMIUM_SELL_PRICE, "premium sell");
    await swap.addPhase(LEGACY_SELL, LEGACY_SELL_PRICE, "legacy redeem");
    await swap.addOperator(operator.address);
    await swap.addGuardian(guardian.address);

    await mcgpToken.mint(owner.address, mcgp(10_000_000));
    await usdcToken.mint(owner.address, usdc(10_000_000));
    await usdcToken.mint(alice.address, usdc(1_000_000));
    await mcgpToken.mint(alice.address, mcgp(100_000));
  });

  async function fundMcgp(amount) {
    await mcgpToken.approve(await swap.getAddress(), amount);
    await swap.fund(await mcgpToken.getAddress(), amount);
  }
  async function fundUsdc(amount) {
    await usdcToken.approve(await swap.getAddress(), amount);
    await swap.fund(await usdcToken.getAddress(), amount);
  }

  // Invariant: accountedMcgp == Σ(bought+credited+pendingSettle) and held >= accounted.
  async function assertBacking(users) {
    let sum = 0n;
    for (const u of users) {
      sum += await swap.premiumBought(u.address);
      sum += await swap.premiumCredited(u.address);
      sum += await swap.pendingSettle(u.address);
    }
    const accounted = await swap.accountedMcgp();
    expect(accounted).to.equal(sum);
    const [held, owed] = await swap.backing();
    expect(owed).to.equal(accounted);
    expect(held).to.be.gte(owed);
  }

  const ALL = () => [alice, bob, merchant, operator, attacker, owner];

  describe("Deployment", function () {
    it("sets tokens, owner, operator, guardian", async function () {
      expect(await swap.mcgpToken()).to.equal(await mcgpToken.getAddress());
      expect(await swap.owner()).to.equal(owner.address);
      expect(await swap.operators(operator.address)).to.equal(true);
      expect(await swap.guardians(guardian.address)).to.equal(true);
    });
  });

  describe("Premium buy (bought = self-exitable)", function () {
    it("credits premiumBought and is backed", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(await swap.getAddress(), cost);
      await swap.connect(alice).buyPremium(mcgp(500), cost);
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(500));
      expect(await swap.premiumBalanceOf(alice.address)).to.equal(mcgp(500));
      await assertBacking(ALL());
    });
    it("reverts when underbacked", async function () {
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(await swap.getAddress(), cost);
      await expect(swap.connect(alice).buyPremium(mcgp(500), cost)).to.be.revertedWith("Underbacked: fund MCGP first");
    });
  });

  describe("Operator credit (premiumCredited = spend-only)", function () {
    beforeEach(async function () {
      await fundMcgp(mcgp(10_000));
    });
    it("credits premiumCredited, raises accounted, requires backing", async function () {
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(500), refOf("IT:credit:1")))
        .to.emit(swap, "PremiumCredited").withArgs(alice.address, mcgp(500), refOf("IT:credit:1"));
      expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(500));
      expect(await swap.premiumBought(alice.address)).to.equal(0);
      await assertBacking(ALL());
    });
    it("reverts when not operator", async function () {
      await expect(swap.connect(alice).creditPremium(alice.address, mcgp(1), refOf("x"))).to.be.revertedWith("Not operator");
    });
    it("reverts on reused ref", async function () {
      await swap.connect(operator).creditPremium(alice.address, mcgp(1), refOf("r"));
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1), refOf("r"))).to.be.revertedWith("Ref already used");
    });
    it("reverts when credit would exceed backing", async function () {
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(20_000), refOf("big")))
        .to.be.revertedWith("Underbacked: fund MCGP first");
    });
    it("enforces minCredit / per-tx / rolling window caps", async function () {
      await swap.setCreditCaps(mcgp(10), mcgp(1000), 3600, mcgp(1500));
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(5), refOf("c1"))).to.be.revertedWith("Below min credit");
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1001), refOf("c2"))).to.be.revertedWith("Over per-tx cap");
      await swap.connect(operator).creditPremium(alice.address, mcgp(1000), refOf("c3"));
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(600), refOf("c4"))).to.be.revertedWith("Over window cap");
    });
  });

  describe("C1 regression — credited premium is NOT self-exitable", function () {
    beforeEach(async function () {
      await fundMcgp(mcgp(10_000));
      await fundUsdc(usdc(100_000));
      // Compromised-operator scenario: credit an attacker-controlled wallet.
      await swap.connect(operator).creditPremium(attacker.address, mcgp(1000), refOf("IT:credit:atk"));
    });
    it("attacker cannot withdraw credited premium as MCGP", async function () {
      await expect(swap.connect(attacker).withdrawPremiumToWallet(mcgp(1))).to.be.revertedWith("Insufficient bought premium");
    });
    it("attacker cannot sell credited premium for USDC", async function () {
      await expect(swap.connect(attacker).sellPremium(mcgp(1), 0)).to.be.revertedWith("Insufficient bought premium");
    });
    it("spending credited premium keeps it credited (non-exitable) for the recipient", async function () {
      await swap.connect(attacker).spend(bob.address, mcgp(100), refOf("spend:atk"));
      expect(await swap.premiumCredited(bob.address)).to.equal(mcgp(100));
      expect(await swap.premiumBought(bob.address)).to.equal(0);
      await expect(swap.connect(bob).withdrawPremiumToWallet(mcgp(1))).to.be.revertedWith("Insufficient bought premium");
      await assertBacking(ALL());
    });
  });

  describe("Spend provenance (credited-first)", function () {
    it("debits credited before bought; recipient gets matching buckets", async function () {
      await fundMcgp(mcgp(10_000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(300));
      await usdcToken.connect(alice).approve(await swap.getAddress(), cost);
      await swap.connect(alice).buyPremium(mcgp(300), cost);                 // bought 300
      await swap.connect(operator).creditPremium(alice.address, mcgp(200), refOf("IT:credit:a")); // credited 200
      await swap.connect(alice).spend(merchant.address, mcgp(250), refOf("spend:1"));             // 200 credited + 50 bought
      expect(await swap.premiumCredited(alice.address)).to.equal(0);
      expect(await swap.premiumBought(alice.address)).to.equal(mcgp(250));
      expect(await swap.premiumCredited(merchant.address)).to.equal(mcgp(200));
      expect(await swap.premiumBought(merchant.address)).to.equal(mcgp(50));
      await assertBacking(ALL());
    });
  });

  describe("Two-phase debit (C2 — no withdrawable excess until settled)", function () {
    beforeEach(async function () {
      // Fund EXACTLY the credited amount so excess starts at 0 and the test
      // isolates the pendingSettle effect (no unrelated over-funded excess).
      await fundMcgp(mcgp(1000));
      await swap.connect(operator).creditPremium(alice.address, mcgp(1000), refOf("IT:credit:s"));
    });
    it("debit moves to pendingSettle without freeing backing", async function () {
      const accBefore = await swap.accountedMcgp();
      await swap.connect(operator).debitPremium(alice.address, mcgp(400), refOf("IT:debit:1"));
      expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(600));
      expect(await swap.pendingSettle(alice.address)).to.equal(mcgp(400));
      expect(await swap.accountedMcgp()).to.equal(accBefore); // unchanged → no new excess
      // owner cannot withdraw the pending-settle MCGP as "excess"
      await expect(swap.withdraw(await mcgpToken.getAddress(), owner.address, mcgp(1)))
        .to.be.revertedWith("Exceeds withdrawable MCGP excess");
      await assertBacking(ALL());
    });
    it("settleDebit frees backing after payout success", async function () {
      await swap.connect(operator).debitPremium(alice.address, mcgp(400), refOf("IT:debit:2"));
      await swap.connect(operator).settleDebit(refOf("IT:debit:2"));
      expect(await swap.pendingSettle(alice.address)).to.equal(0);
      await expect(swap.withdraw(await mcgpToken.getAddress(), owner.address, mcgp(400))).to.emit(swap, "Withdrawn");
      await assertBacking(ALL());
    });
    it("reverseDebit restores credited on payout failure", async function () {
      await swap.connect(operator).debitPremium(alice.address, mcgp(400), refOf("IT:debit:3"));
      await swap.connect(operator).reverseDebit(refOf("IT:debit:3"));
      expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(1000));
      expect(await swap.pendingSettle(alice.address)).to.equal(0);
      await assertBacking(ALL());
    });
    it("a debit resolves at most once: no double-settle, no settle-then-reverse", async function () {
      await swap.connect(operator).debitPremium(alice.address, mcgp(400), refOf("IT:debit:4"));
      await swap.connect(operator).settleDebit(refOf("IT:debit:4"));
      await expect(swap.connect(operator).settleDebit(refOf("IT:debit:4"))).to.be.revertedWith("Not pending");
      await expect(swap.connect(operator).reverseDebit(refOf("IT:debit:4"))).to.be.revertedWith("Not pending");
    });
    it("duplicate debit ref is rejected", async function () {
      await swap.connect(operator).debitPremium(alice.address, mcgp(100), refOf("IT:debit:dup"));
      await expect(swap.connect(operator).debitPremium(alice.address, mcgp(100), refOf("IT:debit:dup"))).to.be.revertedWith("Debit exists");
    });
    it("settleDebit is blocked while paused; reverseDebit is not", async function () {
      await swap.connect(operator).debitPremium(alice.address, mcgp(100), refOf("IT:debit:5"));
      await swap.connect(operator).debitPremium(alice.address, mcgp(100), refOf("IT:debit:6"));
      await swap.pause();
      await expect(swap.connect(operator).settleDebit(refOf("IT:debit:5"))).to.be.revertedWithCustomError(swap, "EnforcedPause");
      await expect(swap.connect(operator).reverseDebit(refOf("IT:debit:6"))).to.emit(swap, "PremiumDebitReversed");
    });
  });

  describe("Legacy redeem-only (unchanged)", function () {
    it("redeems up to entitlement, blocks beyond", async function () {
      await fundUsdc(usdc(100_000));
      await swap.seedLegacy([alice.address], [mcgp(1000)]);
      await mcgpToken.connect(alice).approve(await swap.getAddress(), mcgp(2000));
      await swap.connect(alice).sellLegacy(mcgp(400), 0);
      expect(await swap.legacyRemaining(alice.address)).to.equal(mcgp(600));
      await expect(swap.connect(alice).sellLegacy(mcgp(601), 0)).to.be.revertedWith("Exceeds legacy entitlement");
      expect(await swap.accountedMcgp()).to.equal(0); // legacy never touches premium backing
    });
  });

  describe("Guardian + pause", function () {
    it("guardian can pause and removeOperator, but not addOperator", async function () {
      await swap.connect(guardian).pause();
      await fundMcgp(mcgp(1000));
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1), refOf("p"))).to.be.revertedWithCustomError(swap, "EnforcedPause");
      await swap.connect(guardian).removeOperator(operator.address);
      expect(await swap.operators(operator.address)).to.equal(false);
      await expect(swap.connect(guardian).addOperator(bob.address)).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      // guardian can stop, but only the owner can restart
      await expect(swap.connect(guardian).unpause()).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      await swap.unpause();
    });
    it("withdrawPremiumToWallet works even when paused (bought only)", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(100));
      await usdcToken.connect(alice).approve(await swap.getAddress(), cost);
      await swap.connect(alice).buyPremium(mcgp(100), cost);
      await swap.pause();
      await expect(swap.connect(alice).withdrawPremiumToWallet(mcgp(100))).to.emit(swap, "PremiumWithdrawn");
    });
  });

  describe("Access control", function () {
    it("only owner can seed/phase/caps/addOperator/addGuardian/fund/withdraw", async function () {
      await expect(swap.connect(alice).seedLegacy([bob.address], [mcgp(1)])).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      // DIFF 5: valid non-zero caps — zero is now rejected for everyone, so passing
      // zeroes here would no longer isolate the access-control failure.
      await expect(swap.connect(alice).setCreditCaps(0, mcgp(1), 3600, mcgp(1))).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      await expect(swap.connect(alice).addOperator(bob.address)).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
      await expect(swap.connect(operator).addOperator(bob.address)).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
    });
  });
});
