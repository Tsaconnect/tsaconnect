// GUARD SUITE — the negative tests V1's suite never had.
//
// Every `require` in V1 that no test exercised is a branch a port could delete
// while staying green. These cover them, plus the four behaviours V2 changes on
// purpose (split ref namespaces, mandatory credit caps, disabled renounce,
// seedLegacy floor).

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const LEGACY_SELL = 2;

const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));
const ZERO = "0x0000000000000000000000000000000000000000";
const ZERO_REF = "0x" + "00".repeat(32);

// minCredit, maxCreditPerTx, creditWindow, creditWindowCap, maxDebitPerTx, debitWindow, debitWindowCap
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000), mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — guards", function () {
  let swap, mcgpToken, usdcToken;
  let owner, alice, bob, operator, guardian;

  async function deploy(caps = DEFAULT_CAPS) {
    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    const s = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...caps],
      { kind: "uups" },
    );
    await s.waitForDeployment();
    return s;
  }

  beforeEach(async function () {
    [owner, alice, bob, operator, guardian] = await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await mcgpToken.waitForDeployment();
    await usdcToken.waitForDeployment();

    swap = await deploy();
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
    await mcgpToken.approve(await swap.getAddress(), amount);
    await swap.fund(await mcgpToken.getAddress(), amount);
  }
  async function fundUsdc(amount) {
    await usdcToken.approve(await swap.getAddress(), amount);
    await swap.fund(await usdcToken.getAddress(), amount);
  }
  async function giveBought(signer, amount) {
    await fundMcgp(amount);
    const cost = await swap.quote(PREMIUM_BUY, amount);
    await usdcToken.mint(signer.address, cost);
    await usdcToken.connect(signer).approve(await swap.getAddress(), cost);
    await swap.connect(signer).buyPremium(amount, cost);
  }

  // ── T1b: pricing ────────────────────────────────────────────────────
  describe("pricing", function () {
    // The Go backend calls Quote(kind uint8) with a raw integer. Renumbering the
    // enum would silently return a different price, so pin the numbering.
    it("PriceKind numbering is frozen at PremiumBuy=0, PremiumSell=1, LegacySell=2", async function () {
      expect(await swap.quote(0, mcgp(1))).to.equal(20000n);
      expect(await swap.quote(1, mcgp(1))).to.equal(18000n);
      expect(await swap.quote(2, mcgp(1))).to.equal(10000n);
    });

    it("reverts when no phase is configured for a kind", async function () {
      const fresh = await deploy();
      await expect(fresh.quote(PREMIUM_BUY, mcgp(1))).to.be.revertedWith("No phase configured");
    });

    it("setActivePhase changes the quote", async function () {
      await swap.addPhase(PREMIUM_BUY, 40000, "phase 2");
      await swap.setActivePhase(PREMIUM_BUY, 1);
      expect(await swap.quote(PREMIUM_BUY, mcgp(1))).to.equal(40000n);
      expect(await swap.activePhase(PREMIUM_BUY)).to.equal(1n);
    });

    it("updatePhasePrice changes the quote in place", async function () {
      await swap.updatePhasePrice(PREMIUM_BUY, 0, 25000);
      expect(await swap.quote(PREMIUM_BUY, mcgp(1))).to.equal(25000n);
    });

    it("rejects an out-of-range phase index on activate and update", async function () {
      await expect(swap.setActivePhase(PREMIUM_BUY, 99)).to.be.revertedWith("Invalid phase index");
      await expect(swap.updatePhasePrice(PREMIUM_BUY, 99, 1)).to.be.revertedWith("Invalid phase index");
    });

    it("rejects prices of zero or above MAX_PRICE", async function () {
      const max = await swap.MAX_PRICE();
      await expect(swap.addPhase(PREMIUM_BUY, 0, "zero")).to.be.revertedWith("Invalid price");
      await expect(swap.addPhase(PREMIUM_BUY, max + 1n, "huge")).to.be.revertedWith("Invalid price");
      await expect(swap.updatePhasePrice(PREMIUM_BUY, 0, 0)).to.be.revertedWith("Invalid price");
      await expect(swap.updatePhasePrice(PREMIUM_BUY, 0, max + 1n)).to.be.revertedWith("Invalid price");
    });

    it("getPhases returns what was added", async function () {
      const phases = await swap.getPhases(PREMIUM_BUY);
      expect(phases.length).to.equal(1);
      expect(phases[0].price).to.equal(20000n);
      expect(phases[0].name).to.equal("premium buy");
    });
  });

  // ── T1c: slippage guards — every call site in V1's suite passed the exact
  //    quote or 0, so deleting these requires would not have failed anything.
  describe("slippage guards", function () {
    it("buyPremium reverts when the cost exceeds maxUsdc", async function () {
      await fundMcgp(mcgp(1000));
      const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
      await usdcToken.connect(alice).approve(await swap.getAddress(), cost);
      await expect(swap.connect(alice).buyPremium(mcgp(500), cost - 1n)).to.be.revertedWith("Slippage exceeded");
    });

    it("sellPremium reverts when the proceeds are below minUsdc", async function () {
      await giveBought(alice, mcgp(100));
      await fundUsdc(usdc(10_000));
      const proceeds = await swap.quote(PREMIUM_SELL, mcgp(100));
      await expect(swap.connect(alice).sellPremium(mcgp(100), proceeds + 1n)).to.be.revertedWith("Slippage exceeded");
    });

    it("sellLegacy reverts when the proceeds are below minUsdc", async function () {
      await fundUsdc(usdc(10_000));
      await swap.seedLegacy([alice.address], [mcgp(1000)]);
      await mcgpToken.connect(alice).approve(await swap.getAddress(), mcgp(1000));
      const proceeds = await swap.quote(LEGACY_SELL, mcgp(100));
      await expect(swap.connect(alice).sellLegacy(mcgp(100), proceeds + 1n)).to.be.revertedWith("Slippage exceeded");
    });

    it("sellPremium and sellLegacy revert when the contract lacks USDC", async function () {
      await giveBought(alice, mcgp(100));
      // buyPremium paid USDC *into* the contract, so drain it first — otherwise
      // the purchase itself funds the sell and this asserts nothing.
      const held = await usdcToken.balanceOf(await swap.getAddress());
      await swap.withdraw(await usdcToken.getAddress(), owner.address, held);
      expect(await usdcToken.balanceOf(await swap.getAddress())).to.equal(0n);

      await expect(swap.connect(alice).sellPremium(mcgp(100), 0)).to.be.revertedWith("Insufficient USDC");
      await swap.seedLegacy([alice.address], [mcgp(100)]);
      await mcgpToken.connect(alice).approve(await swap.getAddress(), mcgp(100));
      await expect(swap.connect(alice).sellLegacy(mcgp(100), 0)).to.be.revertedWith("Insufficient USDC");
    });
  });

  // ── T1c: spend guards — V1's suite only ever called spend on the happy path.
  describe("spend guards", function () {
    beforeEach(async function () {
      await giveBought(alice, mcgp(1000));
    });

    it("rejects the zero recipient", async function () {
      await expect(swap.connect(alice).spend(ZERO, mcgp(1), refOf("s"))).to.be.revertedWith("Invalid recipient");
    });
    it("rejects spending to self", async function () {
      await expect(swap.connect(alice).spend(alice.address, mcgp(1), refOf("s"))).to.be.revertedWith("Cannot spend to self");
    });
    it("rejects a zero amount", async function () {
      await expect(swap.connect(alice).spend(bob.address, 0, refOf("s"))).to.be.revertedWith("Amount must be > 0");
    });
    it("rejects a zero ref", async function () {
      await expect(swap.connect(alice).spend(bob.address, mcgp(1), ZERO_REF)).to.be.revertedWith("Invalid ref");
    });
    it("rejects a replayed ref — the guard the relay path depends on", async function () {
      await swap.connect(alice).spend(bob.address, mcgp(1), refOf("dup"));
      await expect(swap.connect(alice).spend(bob.address, mcgp(1), refOf("dup"))).to.be.revertedWith("Ref already used");
    });
    it("rejects spending more than the premium balance", async function () {
      await expect(swap.connect(alice).spend(bob.address, mcgp(1001), refOf("s"))).to.be.revertedWith("Insufficient premium balance");
    });
  });

  // ── T1e: the ref-namespace split ────────────────────────────────────
  describe("ref namespaces are separate (V2 change)", function () {
    it("a spend ref does not burn the identical credit ref, and vice versa", async function () {
      await giveBought(alice, mcgp(100));
      await fundMcgp(mcgp(1000));
      const shared = refOf("collide");

      // In V1 both lived in one `usedRef` mapping, so this spend would have
      // permanently blocked the operator's credit with the same bytes32 — a free
      // griefing vector once spends are gasless.
      await swap.connect(alice).spend(bob.address, mcgp(1), shared);
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1), shared))
        .to.emit(swap, "PremiumCredited");

      expect(await swap.usedSpendRef(shared)).to.equal(true);
      expect(await swap.usedCreditRef(shared)).to.equal(true);
    });

    it("each namespace still rejects its own replay", async function () {
      await fundMcgp(mcgp(1000));
      const r = refOf("own-replay");
      await swap.connect(operator).creditPremium(alice.address, mcgp(1), r);
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1), r)).to.be.revertedWith("Ref already used");
    });
  });

  // ── T1d: credit caps are mandatory (V2 change) ──────────────────────
  describe("credit caps", function () {
    it("initialize rejects a zero per-tx cap, window, or window cap", async function () {
      const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
      const args = (caps) => [owner.address, mcgpToken.target, usdcToken.target, ...caps];

      const DEBIT_OK = [mcgp(1), 3600, mcgp(1)];
      for (const [caps, reason] of [
        [[0, 0, 3600, mcgp(1), ...DEBIT_OK], "maxCreditPerTx must be > 0"],
        [[0, mcgp(1), 0, mcgp(1), ...DEBIT_OK], "creditWindow must be > 0"],
        [[0, mcgp(1), 3600, 0, ...DEBIT_OK], "creditWindowCap must be > 0"],
        // The debit caps get the same treatment: zero is never "unlimited".
        [[0, mcgp(1), 3600, mcgp(1), 0, 3600, mcgp(1)], "maxDebitPerTx must be > 0"],
        [[0, mcgp(1), 3600, mcgp(1), mcgp(1), 0, mcgp(1)], "debitWindow must be > 0"],
        [[0, mcgp(1), 3600, mcgp(1), mcgp(1), 3600, 0], "debitWindowCap must be > 0"],
      ]) {
        await expect(upgrades.deployProxy(Swap, args(caps), { kind: "uups" })).to.be.revertedWith(reason);
      }
    });

    it("initialize rejects a minCredit above the per-tx cap", async function () {
      const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
      await expect(
        upgrades.deployProxy(
          Swap,
          [owner.address, mcgpToken.target, usdcToken.target, mcgp(10), mcgp(1), 3600, mcgp(100), mcgp(1), 3600, mcgp(1)],
          { kind: "uups" },
        ),
      ).to.be.revertedWith("minCredit exceeds per-tx cap");
    });

    it("setCreditCaps rejects zeroes too — the caps cannot be turned off later", async function () {
      await expect(swap.setCreditCaps(0, 0, 3600, mcgp(1))).to.be.revertedWith("maxCreditPerTx must be > 0");
      await expect(swap.setCreditCaps(0, mcgp(1), 0, mcgp(1))).to.be.revertedWith("creditWindow must be > 0");
      await expect(swap.setCreditCaps(0, mcgp(1), 3600, 0)).to.be.revertedWith("creditWindowCap must be > 0");
    });

    it("the rolling window resets once the window elapses", async function () {
      await fundMcgp(mcgp(10_000));
      await swap.setCreditCaps(0, mcgp(1000), 3600, mcgp(1000));
      await swap.connect(operator).creditPremium(alice.address, mcgp(1000), refOf("w1"));
      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1), refOf("w2")))
        .to.be.revertedWith("Over window cap");

      await ethers.provider.send("evm_increaseTime", [3601]);
      await ethers.provider.send("evm_mine", []);

      await expect(swap.connect(operator).creditPremium(alice.address, mcgp(1000), refOf("w3")))
        .to.emit(swap, "PremiumCredited");
    });
  });

  // ── withdraw paths V1 never tested ──────────────────────────────────
  describe("withdraw", function () {
    it("withdraws USDC with no excess cap (only MCGP is backing-constrained)", async function () {
      await fundUsdc(usdc(1000));
      await expect(swap.withdraw(await usdcToken.getAddress(), owner.address, usdc(1000))).to.emit(swap, "Withdrawn");
    });
    it("rejects an unknown token, the zero recipient, and a zero amount", async function () {
      await expect(swap.withdraw(bob.address, owner.address, 1)).to.be.revertedWith("Invalid token");
      await expect(swap.withdraw(await usdcToken.getAddress(), ZERO, 1)).to.be.revertedWith("Invalid recipient");
      await expect(swap.withdraw(await usdcToken.getAddress(), owner.address, 0)).to.be.revertedWith("Amount must be > 0");
    });
    it("rejects funding an unknown token", async function () {
      await expect(swap.fund(bob.address, 1)).to.be.revertedWith("Invalid token");
    });
  });

  // ── seedLegacy floor (V2 change) ────────────────────────────────────
  describe("seedLegacy", function () {
    it("rejects an entitlement below what the user already redeemed", async function () {
      await fundUsdc(usdc(10_000));
      await swap.seedLegacy([alice.address], [mcgp(1000)]);
      await mcgpToken.connect(alice).approve(await swap.getAddress(), mcgp(1000));
      await swap.connect(alice).sellLegacy(mcgp(400), 0);

      // V1 would have accepted this and wedged legacyRemaining at 0 with the
      // user still owed 600.
      await expect(swap.seedLegacy([alice.address], [mcgp(100)])).to.be.revertedWith("Below redeemed");
    });
    it("rejects mismatched array lengths", async function () {
      await expect(swap.seedLegacy([alice.address, bob.address], [mcgp(1)])).to.be.revertedWith("Length mismatch");
    });
  });

  // ── T1a: proxy hygiene ──────────────────────────────────────────────
  describe("proxy hygiene", function () {
    it("initialize cannot be called twice on the proxy", async function () {
      await expect(
        swap.initialize(owner.address, mcgpToken.target, usdcToken.target, ...DEFAULT_CAPS),
      ).to.be.revertedWithCustomError(swap, "InvalidInitialization");
    });

    it("the implementation behind the proxy has its initializers disabled", async function () {
      const implAddr = await upgrades.erc1967.getImplementationAddress(await swap.getAddress());
      const impl = await ethers.getContractAt("McgpEntitlementSwapV2", implAddr);
      await expect(
        impl.initialize(owner.address, mcgpToken.target, usdcToken.target, ...DEFAULT_CAPS),
      ).to.be.revertedWithCustomError(impl, "InvalidInitialization");
    });

    it("renounceOwnership is disabled — it would brick upgradeability", async function () {
      await expect(swap.renounceOwnership()).to.be.revertedWith("Ownership cannot be renounced");
      expect(await swap.owner()).to.equal(owner.address);
    });

    it("only the owner can authorize an upgrade", async function () {
      const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
      const nextImpl = await Swap.deploy();
      await nextImpl.waitForDeployment();
      await expect(
        swap.connect(alice).upgradeToAndCall(await nextImpl.getAddress(), "0x"),
      ).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
    });
  });
});
