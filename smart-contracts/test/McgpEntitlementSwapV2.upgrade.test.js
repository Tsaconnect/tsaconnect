// Upgrade safety.
//
// V2 is the first upgradeable version, so there is no legacy layout to stay
// compatible with — what matters is that the NEXT upgrade cannot silently
// corrupt the ledger. These tests fail loudly if the storage layout stops being
// append-safe, and prove a real upgrade preserves every per-wallet balance.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);
const refOf = (s) => ethers.keccak256(ethers.toUtf8Bytes(s));
// minCredit, maxCreditPerTx, creditWindow, creditWindowCap, maxDebitPerTx, debitWindow, debitWindowCap
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000), mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — upgrade safety", function () {
  let swap, mcgpToken, usdcToken;
  let owner, alice, bob, operator;

  beforeEach(async function () {
    [owner, alice, bob, operator] = await ethers.getSigners();

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
    await swap.addOperator(operator.address);

    await mcgpToken.mint(owner.address, mcgp(10_000_000));
    await usdcToken.mint(alice.address, usdc(1_000_000));
  });

  it("validateUpgrade accepts an append-only V3 (storage-layout safe)", async function () {
    const V2 = await ethers.getContractFactory("McgpEntitlementSwapV2");
    const V3 = await ethers.getContractFactory("McgpEntitlementSwapV3Test");
    // Throws if the layout is incompatible; resolves otherwise.
    await upgrades.validateUpgrade(V2, V3, {
      kind: "uups",
      // Same allowance the repo already uses for FeeManagerV2Test: the test-only
      // successor has no initializer of its own and inherits V2's constructor.
      unsafeAllow: ["constructor", "missing-initializer"],
    });
  });

  it("a real upgrade preserves every ledger, the caps, and the forwarder", async function () {
    // Seed state across all the buckets before upgrading.
    await mcgpToken.approve(swap.target, mcgp(10_000));
    await swap.fund(await mcgpToken.getAddress(), mcgp(10_000));

    const cost = await swap.quote(PREMIUM_BUY, mcgp(500));
    await usdcToken.connect(alice).approve(swap.target, cost);
    await swap.connect(alice).buyPremium(mcgp(500), cost);
    await swap.connect(operator).creditPremium(bob.address, mcgp(300), refOf("pre-upgrade"));
    await swap.connect(operator).debitPremium(bob.address, mcgp(100), refOf("pre-upgrade:debit"));
    await swap.seedLegacy([alice.address], [mcgp(750)]);
    await swap.setTrustedForwarder(bob.address);

    const before = {
      bought: await swap.premiumBought(alice.address),
      credited: await swap.premiumCredited(bob.address),
      pending: await swap.pendingSettle(bob.address),
      legacy: await swap.legacyEntitlement(alice.address),
      accounted: await swap.accountedMcgp(),
      maxPerTx: await swap.maxCreditPerTx(),
      forwarder: await swap.trustedForwarder(),
      spendRefUsed: await swap.usedCreditRef(refOf("pre-upgrade")),
    };

    const V3 = await ethers.getContractFactory("McgpEntitlementSwapV3Test");
    const upgraded = await upgrades.upgradeProxy(await swap.getAddress(), V3, {
      unsafeAllow: ["constructor", "missing-initializer"],
    });

    expect(await upgraded.getAddress()).to.equal(await swap.getAddress());
    expect(await upgraded.version()).to.equal("v3-test");

    expect(await upgraded.premiumBought(alice.address)).to.equal(before.bought);
    expect(await upgraded.premiumCredited(bob.address)).to.equal(before.credited);
    expect(await upgraded.pendingSettle(bob.address)).to.equal(before.pending);
    expect(await upgraded.legacyEntitlement(alice.address)).to.equal(before.legacy);
    expect(await upgraded.accountedMcgp()).to.equal(before.accounted);
    expect(await upgraded.maxCreditPerTx()).to.equal(before.maxPerTx);
    expect(await upgraded.trustedForwarder()).to.equal(before.forwarder);
    expect(await upgraded.usedCreditRef(refOf("pre-upgrade"))).to.equal(before.spendRefUsed);

    // The appended slot starts zero and does not collide with anything above it.
    expect(await upgraded.appendedSlot()).to.equal(0n);
    await upgraded.setAppendedSlot(42);
    expect(await upgraded.appendedSlot()).to.equal(42n);
    expect(await upgraded.accountedMcgp()).to.equal(before.accounted); // still intact
  });

  it("a pending debit still settles correctly after an upgrade", async function () {
    await mcgpToken.approve(swap.target, mcgp(10_000));
    await swap.fund(await mcgpToken.getAddress(), mcgp(10_000));
    await swap.connect(operator).creditPremium(bob.address, mcgp(300), refOf("u:credit"));
    await swap.connect(operator).debitPremium(bob.address, mcgp(100), refOf("u:debit"));

    const V3 = await ethers.getContractFactory("McgpEntitlementSwapV3Test");
    const upgraded = await upgrades.upgradeProxy(await swap.getAddress(), V3, {
      unsafeAllow: ["constructor", "missing-initializer"],
    });

    // The `debits` mapping survived, so settle still binds to the right debit.
    await upgraded.connect(operator).settleDebit(refOf("u:debit"));
    expect(await upgraded.pendingSettle(bob.address)).to.equal(0n);
    expect(await upgraded.premiumCredited(bob.address)).to.equal(mcgp(200));
  });

  it("the __gap shrank 1:1 with the slots appended after it", async function () {
    // The gap started at 50. Every slot appended below the original storage must
    // come OUT of the gap, or the next upgrade's appended state collides with
    // whatever the gap was reserving. Eight slots have been added since: six for
    // operator debit (maxDebitPerTx, debitWindow, debitWindowCap,
    // debitWindowStart, debitedInWindow, _debitFromBought) and two for the V1
    // migration (premiumMigrated, migrationClosed), so the gap must now read 42.
    const layout = await upgrades.erc1967.getImplementationAddress(await swap.getAddress());
    expect(layout).to.properAddress;

    const source = require("fs").readFileSync(
      require("path").join(__dirname, "../contracts/McgpEntitlementSwapV2.sol"),
      "utf8",
    );
    const GAP_ORIGINAL = 50;
    const APPENDED = 8;
    expect(source).to.match(new RegExp(`uint256\\[${GAP_ORIGINAL - APPENDED}\\] private __gap;`));
  });
});
