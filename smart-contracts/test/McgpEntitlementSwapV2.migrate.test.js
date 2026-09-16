// One-time import of premium balances from the V1 proxy.
//
// V2 is a separate proxy with an empty ledger, and `buyPremium` — the only
// writer of `premiumBought` — charges USDC. Without an admin path, moving a
// user's already-paid-for bought premium would mean charging them twice. That
// makes `migratePremium` a function that mints entitlement out of nothing, so
// the tests here are about the fences around it:
//
//   1. It cannot run twice for a user (a repeat inflates accountedMcgp and
//      silently underbacks every other holder).
//   2. It cannot leave the contract underbacked.
//   3. It can be sealed, closing the mint path in this implementation.
//   4. Migrated balances behave EXACTLY like natively-created ones — bought
//      stays self-exitable, credited stays spend-only.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const mcgp = (n) => ethers.parseUnits(String(n), 18);
const usdc = (n) => ethers.parseUnits(String(n), 6);

const DEFAULT_CAPS = [0, mcgp(1e9), 3600, mcgp(1e9), mcgp(1e9), 3600, mcgp(1e9)];

describe("McgpEntitlementSwapV2 — premium migration from V1", function () {
  let swap, mcgpToken, usdcToken;
  let owner, alice, bob, carol, operator;

  beforeEach(async function () {
    [owner, alice, bob, carol, operator] = await ethers.getSigners();
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await mcgpToken.waitForDeployment();
    await usdcToken.waitForDeployment();
    await mcgpToken.mint(owner.address, mcgp(1_000_000));
    await usdcToken.mint(owner.address, usdc(1_000_000));

    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    swap = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
      { kind: "uups" },
    );
    await swap.waitForDeployment();
    await swap.addPhase(PREMIUM_BUY, 20000, "buy");   // $0.02
    await swap.addPhase(PREMIUM_SELL, 18000, "sell"); // $0.018
    await swap.addOperator(operator.address);
  });

  async function fund(amount) {
    await mcgpToken.approve(swap.target, amount);
    await swap.fund(await mcgpToken.getAddress(), amount);
  }

  it("imports both buckets and accounts for them", async function () {
    await fund(mcgp(1000));
    await expect(
      swap.migratePremium([alice.address, bob.address], [mcgp(229), 0], [mcgp(17), mcgp(14)]),
    )
      .to.emit(swap, "PremiumMigrated")
      .withArgs(alice.address, mcgp(229), mcgp(17));

    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(229));
    expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(17));
    expect(await swap.premiumBalanceOf(alice.address)).to.equal(mcgp(246));
    expect(await swap.premiumBought(bob.address)).to.equal(0);
    expect(await swap.premiumCredited(bob.address)).to.equal(mcgp(14));
    expect(await swap.accountedMcgp()).to.equal(mcgp(260));

    const [held, owed] = await swap.backing();
    expect(held).to.equal(mcgp(1000));
    expect(owed).to.equal(mcgp(260));
  });

  it("refuses to migrate the same user twice", async function () {
    await fund(mcgp(1000));
    await swap.migratePremium([alice.address], [mcgp(10)], [mcgp(5)]);
    await expect(
      swap.migratePremium([alice.address], [mcgp(10)], [mcgp(5)]),
    ).to.be.revertedWith("Already migrated");
    // A duplicate inside one batch is caught too.
    await expect(
      swap.migratePremium([bob.address, bob.address], [mcgp(1), mcgp(1)], [0, 0]),
    ).to.be.revertedWith("Already migrated");
    expect(await swap.accountedMcgp()).to.equal(mcgp(15));
  });

  it("refuses to leave the contract underbacked", async function () {
    await fund(mcgp(100));
    await expect(
      swap.migratePremium([alice.address], [mcgp(200)], [0]),
    ).to.be.revertedWith("Underbacked: fund MCGP first");
    // Nothing was committed.
    expect(await swap.accountedMcgp()).to.equal(0);
    expect(await swap.premiumMigrated(alice.address)).to.equal(false);
  });

  it("checks backing on the batch as a whole, not per user", async function () {
    await fund(mcgp(300));
    // Each user is under the total, and together they exactly exhaust backing.
    await swap.migratePremium(
      [alice.address, bob.address, carol.address],
      [mcgp(100), mcgp(100), mcgp(100)],
      [0, 0, 0],
    );
    expect(await swap.accountedMcgp()).to.equal(mcgp(300));
  });

  it("is owner-only", async function () {
    await fund(mcgp(1000));
    await expect(
      swap.connect(alice).migratePremium([alice.address], [mcgp(1)], [0]),
    ).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
    await expect(
      swap.connect(operator).migratePremium([alice.address], [mcgp(1)], [0]),
    ).to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
  });

  it("validates batch shape", async function () {
    await fund(mcgp(1000));
    await expect(swap.migratePremium([alice.address], [mcgp(1), mcgp(2)], [0])).to.be.revertedWith("Length mismatch");
    await expect(swap.migratePremium([alice.address], [mcgp(1)], [0, 0])).to.be.revertedWith("Length mismatch");
    await expect(swap.migratePremium([], [], [])).to.be.revertedWith("Empty batch");
    await expect(swap.migratePremium([ethers.ZeroAddress], [mcgp(1)], [0])).to.be.revertedWith("Invalid user");
    await expect(swap.migratePremium([alice.address], [0], [0])).to.be.revertedWith("Nothing to migrate");
  });

  it("seals migration in this implementation, and the seal cannot be lifted from its ABI", async function () {
    await fund(mcgp(1000));
    await swap.migratePremium([alice.address], [mcgp(10)], [0]);
    await expect(swap.closeMigration()).to.emit(swap, "MigrationClosed");
    expect(await swap.migrationClosed()).to.equal(true);

    await expect(
      swap.migratePremium([bob.address], [mcgp(1)], [0]),
    ).to.be.revertedWith("Migration closed");
    await expect(swap.closeMigration()).to.be.revertedWith("Already closed");

    // The seal is what restores the invariant: after it, bought premium can
    // only come from a real USDC payment.
    await usdcToken.mint(bob.address, usdc(1000));
    await usdcToken.connect(bob).approve(swap.target, usdc(1000));
    await swap.connect(bob).buyPremium(mcgp(5), usdc(1000));
    expect(await swap.premiumBought(bob.address)).to.equal(mcgp(5));
  });

  it("migrated bought premium stays self-exitable", async function () {
    await fund(mcgp(1000));
    await usdcToken.approve(swap.target, usdc(1_000_000));
    await swap.fund(await usdcToken.getAddress(), usdc(1000));
    await swap.migratePremium([alice.address], [mcgp(100)], [0]);

    // Withdraw to wallet — only bought premium may do this.
    await swap.connect(alice).withdrawPremiumToWallet(mcgp(40));
    expect(await mcgpToken.balanceOf(alice.address)).to.equal(mcgp(40));
    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(60));

    // Sell back for USDC — likewise bought-only.
    await swap.connect(alice).sellPremium(mcgp(10), 0);
    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(50));
    expect(await swap.accountedMcgp()).to.equal(mcgp(50));
  });

  it("migrated credited premium stays spend-only", async function () {
    await fund(mcgp(1000));
    await swap.migratePremium([alice.address], [0], [mcgp(100)]);

    await expect(swap.connect(alice).withdrawPremiumToWallet(mcgp(1))).to.be.reverted;
    await expect(swap.connect(alice).sellPremium(mcgp(1), 0)).to.be.reverted;

    // But it can be spent, and the recipient receives the same bucket.
    await swap.connect(alice).spend(bob.address, mcgp(10), ethers.keccak256(ethers.toUtf8Bytes("x")));
    expect(await swap.premiumCredited(bob.address)).to.equal(mcgp(10));
    expect(await swap.premiumBought(bob.address)).to.equal(0);
  });

  it("migrated balances are debitable by the operator (the bills path)", async function () {
    await fund(mcgp(1000));
    await swap.migratePremium([alice.address], [mcgp(50)], [mcgp(50)]);

    const ref = ethers.keccak256(ethers.toUtf8Bytes("mcgp-bill:1"));
    await swap.connect(operator).debitSpendable(alice.address, mcgp(60), mcgp(60), ref);
    // Credited is drawn first, so 50 credited + 10 bought.
    expect(await swap.premiumCredited(alice.address)).to.equal(0);
    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(40));

    await swap.connect(operator).reverseDebit(ref);
    // Restored to the exact buckets it came from.
    expect(await swap.premiumCredited(alice.address)).to.equal(mcgp(50));
    expect(await swap.premiumBought(alice.address)).to.equal(mcgp(50));
  });

  it("reproduces the real V1 cutover totals exactly", async function () {
    // The 13 live holders on V1 (0xF27F…8E52) as of 2026-08-04, read from chain
    // in WEI. These were first transcribed from formatted output and were wrong
    // by 2,075 wei — which this test caught. Migration input must always come
    // from a raw on-chain read, never from a human-readable table.
    const HOLDERS = [
      ["0x8468922134272f99933e8B9ffA7256b0f5588D74", "229107550000000000000", "17901897601145721446"],
      ["0x09124A9E3c1B5ACf4207e33828195D00846e5ea8", "8000000000000000000", "14111480652646068503"],
      ["0x6D321115A1e84f7316CE6eddF30Cba46238aDE07", "0", "17901897601145721446"],
      ["0xd9839a20D2d7F1888C2cB7F7E41eb8BEb5114189", "0", "17507194244604316546"],
      ["0xB1f3ffaDebC9C271Df42866421CC6A4FF6Fa1eCD", "0", "12513494062612450521"],
      ["0xCbEcD72481EED51cAFef33737967828A446182D2", "0", "10519452449567723342"],
      ["0x84aD9525BDE4d2c1b6b511e8aA08d6993D1aE8C7", "0", "10504316546762589928"],
      ["0x5D246d9E01e6D0E119D76F44EcB187bdD5C44a38", "0", "10500539374325782092"],
      ["0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce", "10000000000000000000", "0"],
      ["0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96", "2000000000000000000", "7953321364452423698"],
      ["0xA151A6234BCf7295787E6946b0E04c4DF2Be1a78", "0", "7178750897343862167"],
      ["0x25e4D0C2f12618C01B205375642e14f2d4f6B41c", "0", "6982783357245337159"],
      ["0x2Fb00b5B78faf9a1Cb1813FCA4F0dC0ebBF1A9E3", "0", "2564108906809149227"],
    ];
    await fund(mcgp(500));
    await swap.migratePremium(
      HOLDERS.map((h) => h[0]),
      HOLDERS.map((h) => h[1]),
      HOLDERS.map((h) => h[2]),
    );
    // Must equal V1's accountedMcgp to the wei, or somebody's balance moved.
    expect(await swap.accountedMcgp()).to.equal(385246787058661146075n);
    const [held, owed] = await swap.backing();
    expect(held).to.be.gte(owed);
  });
});
