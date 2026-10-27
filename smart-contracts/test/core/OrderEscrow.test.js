const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

// OrderEscrow + FeeConfig: the audit-ready order escrow (docs/specs/2026-10-27-contracts-audit-ready-redesign.md).

const DAY = 24 * 60 * 60;
const FEATURE = ethers.encodeBytes32String("order");
const usd = (n) => ethers.parseUnits(String(n), 6);
const id = (s) => ethers.id(s);
const coder = ethers.AbiCoder.defaultAbiCoder();
// Orders are stored under keccak256(buyer, orderId).
const K = (ctx, name, buyer = ctx.buyer) => ethers.keccak256(coder.encode(["address", "bytes32"], [buyer.address, id(name)]));
const MAXFLAT = ethers.MaxUint256;

const State = { None: 0, Funded: 1, Delivered: 2, Disputed: 3, Released: 4, Refunded: 5, Resolved: 6 };
const Reason = { Cancelled: 0, NotDelivered: 1, DisputeBackstop: 2 };

async function deployFeeConfig(admin, recipient) {
  return upgrades.deployProxy(await ethers.getContractFactory("FeeConfig"), [admin.address, recipient.address, { features: [], tokens: [], rates: [], limitTokens: [], limits: [] }], {
    kind: "uups",
  });
}

async function deployEscrow(admin, forwarder, feeConfig) {
  return upgrades.deployProxy(await ethers.getContractFactory("OrderEscrow"), [admin.address, [], []], {
    kind: "uups",
    constructorArgs: [forwarder, await feeConfig.getAddress()],
  });
}

async function setup({ bps = 200, flat = usd("0.10"), maxPerOrder = usd(1000), maxOwed = usd(10000) } = {}) {
  const [admin, buyer, seller, feeWallet, resolver, stranger, pauser] = await ethers.getSigners();
  const fc = await deployFeeConfig(admin, feeWallet);
  const forwarder = await (await ethers.getContractFactory("TSAForwarder")).deploy("TSAForwarder");
  const escrow = await deployEscrow(admin, await forwarder.getAddress(), fc);
  const token = await (await ethers.getContractFactory("MockERC20")).deploy("USD Coin", "USDC", 6);
  await token.mint(buyer.address, usd(100000));
  await token.connect(buyer).approve(await escrow.getAddress(), ethers.MaxUint256);
  await fc.setRate(FEATURE, await token.getAddress(), { bps, flatFee: flat, active: true });
  await fc.setLimits(await token.getAddress(), { maxPerOrder, maxOwed });
  await escrow.grantRole(await escrow.RESOLVER_ROLE(), resolver.address);
  await escrow.grantRole(await escrow.PAUSER_ROLE(), pauser.address);
  return { admin, buyer, seller, feeWallet, resolver, stranger, pauser, fc, forwarder, escrow, token };
}

async function create(ctx, name, { product = usd(100), shipping = usd(10), days = 14, maxFlat = MAXFLAT } = {}) {
  return ctx.escrow
    .connect(ctx.buyer)
    .createOrder(id(name), ctx.seller.address, await ctx.token.getAddress(), product, shipping, days, maxFlat);
}

const bal = (ctx, who) => ctx.token.balanceOf(who.address ?? who);

describe("FeeConfig", () => {
  it("fails closed: an unset or inactive rate reverts, so an unconfigured token cannot be used", async () => {
    const ctx = await setup();
    const other = ethers.Wallet.createRandom().address;
    await expect(ctx.fc.rateFor(FEATURE, other)).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    await ctx.fc.setRate(FEATURE, other, { bps: 100, flatFee: 0, active: false });
    await expect(ctx.fc.rateFor(FEATURE, other)).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    await expect(ctx.fc.limitsFor(other)).to.be.revertedWithCustomError(ctx.fc, "LimitsNotConfigured");
  });

  it("caps rates, rejects zero limits and the zero address, and is role-gated", async () => {
    const ctx = await setup();
    const t = await ctx.token.getAddress();
    await expect(ctx.fc.setRate(FEATURE, t, { bps: 1001, flatFee: 0, active: true }))
      .to.be.revertedWithCustomError(ctx.fc, "RateTooHigh");
    await expect(ctx.fc.setLimits(t, { maxPerOrder: 0, maxOwed: 1 })).to.be.revertedWithCustomError(ctx.fc, "ZeroLimit");
    await expect(ctx.fc.setFeeRecipient(ethers.ZeroAddress)).to.be.revertedWithCustomError(ctx.fc, "ZeroAddress");
    await expect(ctx.fc.connect(ctx.stranger).setFeeRecipient(ctx.stranger.address))
      .to.be.revertedWithCustomError(ctx.fc, "AccessControlUnauthorizedAccount");
    await expect(ctx.fc.setFeeRecipient(ctx.stranger.address))
      .to.emit(ctx.fc, "FeeRecipientSet").withArgs(ctx.feeWallet.address, ctx.stranger.address);
  });

  it("takes its initial rates and limits in the initializer, so the admin owns it from block one", async () => {
    const [admin, deployer, recipient] = await ethers.getSigners();
    const t = ethers.Wallet.createRandom().address;
    const fc = await upgrades.deployProxy(
      (await ethers.getContractFactory("FeeConfig")).connect(deployer),
      [admin.address, recipient.address, { features: [FEATURE], tokens: [t], rates: [{ bps: 200, flatFee: 100000n, active: true }], limitTokens: [t], limits: [{ maxPerOrder: 5n, maxOwed: 9n }] }],
      { kind: "uups" },
    );
    expect((await fc.rateFor(FEATURE, t)).bps).to.equal(200);
    expect((await fc.limitsFor(t)).maxOwed).to.equal(9);
    for (const role of [await fc.DEFAULT_ADMIN_ROLE(), await fc.CONFIG_ROLE(), await fc.UPGRADER_ROLE()]) {
      expect(await fc.hasRole(role, deployer.address)).to.equal(false);
      expect(await fc.hasRole(role, admin.address)).to.equal(true);
    }
    // A bad initial row reverts the whole deployment.
    await expect(
      upgrades.deployProxy(await ethers.getContractFactory("FeeConfig"),
        [admin.address, recipient.address, { features: [FEATURE], tokens: [t], rates: [{ bps: 5000, flatFee: 0, active: true }], limitTokens: [], limits: [] }], { kind: "uups" }),
    ).to.be.reverted;
  });

  it("batch setRates is all-or-nothing", async () => {
    const ctx = await setup();
    const a = ethers.Wallet.createRandom().address;
    const b = ethers.Wallet.createRandom().address;
    await expect(
      ctx.fc.setRates([FEATURE, FEATURE], [a, b], [
        { bps: 100, flatFee: 0, active: true },
        { bps: 5000, flatFee: 0, active: true },
      ]),
    ).to.be.revertedWithCustomError(ctx.fc, "RateTooHigh");
    await expect(ctx.fc.rateFor(FEATURE, a)).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
  });
});

describe("OrderEscrow", () => {
  describe("creation", () => {
    it("pulls product + shipping + flat, pays the flat fee straight to the fee wallet, tracks owed", async () => {
      const ctx = await setup();
      const before = await bal(ctx, ctx.buyer);
      await expect(create(ctx, "o1"))
        .to.emit(ctx.escrow, "OrderCreated");
      expect(before - (await bal(ctx, ctx.buyer))).to.equal(usd(110.1));
      expect(await bal(ctx, ctx.feeWallet)).to.equal(usd(0.1));
      expect(await ctx.escrow.owed(await ctx.token.getAddress())).to.equal(usd(110));
      const o = await ctx.escrow.getOrder(K(ctx, "o1"));
      expect(o.state).to.equal(State.Funded);
      expect(o.merchantFee).to.equal(usd(2)); // 2% of the product price only, not shipping
    });

    it("rejects duplicates, self-dealing, zero amounts, bad handling days, unconfigured tokens", async () => {
      const ctx = await setup();
      await create(ctx, "dup");
      await expect(create(ctx, "dup")).to.be.revertedWithCustomError(ctx.escrow, "OrderExists");
      const t = await ctx.token.getAddress();
      await expect(ctx.escrow.connect(ctx.buyer).createOrder(id("x"), ctx.buyer.address, t, usd(1), 0, 14, MAXFLAT))
        .to.be.revertedWithCustomError(ctx.escrow, "InvalidSeller");
      await expect(ctx.escrow.connect(ctx.buyer).createOrder(id("x"), ctx.seller.address, t, 0, 0, 14, MAXFLAT))
        .to.be.revertedWithCustomError(ctx.escrow, "ZeroAmount");
      await expect(create(ctx, "x", { days: 0 })).to.be.revertedWithCustomError(ctx.escrow, "InvalidHandlingDays");
      await expect(create(ctx, "x", { days: 31 })).to.be.revertedWithCustomError(ctx.escrow, "InvalidHandlingDays");
      const other = await (await ethers.getContractFactory("MockERC20")).deploy("X", "X", 6);
      await expect(
        ctx.escrow.connect(ctx.buyer).createOrder(id("x"), ctx.seller.address, await other.getAddress(), usd(1), 0, 14, MAXFLAT),
      ).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    });

    it("enforces the per-order and total owed limits", async () => {
      const ctx = await setup({ maxPerOrder: usd(100), maxOwed: usd(150) });
      await expect(create(ctx, "big", { product: usd(95), shipping: usd(10) }))
        .to.be.revertedWithCustomError(ctx.escrow, "OrderAboveLimit");
      await create(ctx, "a", { product: usd(90), shipping: 0 });
      await expect(create(ctx, "b", { product: usd(90), shipping: 0 }))
        .to.be.revertedWithCustomError(ctx.escrow, "OwedLimitExceeded");
    });

    it("refuses a fee above its own cap even if FeeConfig allows it", async () => {
      const ctx = await setup({ bps: 301 });
      await expect(create(ctx, "o")).to.be.revertedWithCustomError(ctx.escrow, "FeeAboveCap");
    });

    it("rejects fee-on-transfer tokens", async () => {
      const ctx = await setup();
      const fot = await (await ethers.getContractFactory("FeeOnTransferERC20")).deploy();
      const f = await fot.getAddress();
      await ctx.fc.setRate(FEATURE, f, { bps: 200, flatFee: 0, active: true });
      await ctx.fc.setLimits(f, { maxPerOrder: ethers.MaxUint256 >> 129n, maxOwed: ethers.MaxUint256 >> 129n });
      await fot.mint(ctx.buyer.address, ethers.parseEther("10"));
      await fot.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
      await expect(
        ctx.escrow.connect(ctx.buyer).createOrder(id("fot"), ctx.seller.address, f, ethers.parseEther("1"), 0, 14, MAXFLAT),
      ).to.be.revertedWithCustomError(ctx.escrow, "AmountMismatch");
    });

    it("snapshots the fee: a later rate change does not touch an open order", async () => {
      const ctx = await setup();
      await create(ctx, "snap");
      await ctx.fc.setRate(FEATURE, await ctx.token.getAddress(), { bps: 0, flatFee: 0, active: true });
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "snap"));
      expect(await bal(ctx, ctx.feeWallet)).to.equal(usd(0.1) + usd(2));
    });
  });

  describe("happy path and seller-pays fee", () => {
    it("deliver → confirm pays the seller price + shipping − 2% of price, and the fee to the fee wallet", async () => {
      const ctx = await setup();
      await create(ctx, "h");
      await expect(ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "h"))).to.emit(ctx.escrow, "OrderDelivered");
      await expect(ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "h")))
        .to.emit(ctx.escrow, "OrderReleased")
        .withArgs(K(ctx, "h"), ctx.seller.address, usd(108), usd(2), ctx.feeWallet.address);
      expect(await bal(ctx, ctx.seller)).to.equal(usd(108));
      expect(await ctx.escrow.owed(await ctx.token.getAddress())).to.equal(0);
      expect(await bal(ctx, await ctx.escrow.getAddress())).to.equal(0);
    });

    it("the fee is paid to whatever recipient is configured at settlement", async () => {
      const ctx = await setup();
      await create(ctx, "r");
      await ctx.fc.setFeeRecipient(ctx.stranger.address);
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "r"));
      expect(await bal(ctx, ctx.stranger)).to.equal(usd(2));
    });

    it("only the right party can act", async () => {
      const ctx = await setup();
      await create(ctx, "p");
      await expect(ctx.escrow.connect(ctx.buyer).markDelivered(K(ctx, "p"))).to.be.revertedWithCustomError(ctx.escrow, "NotSeller");
      await expect(ctx.escrow.connect(ctx.seller).confirmReceipt(K(ctx, "p"))).to.be.revertedWithCustomError(ctx.escrow, "NotBuyer");
      await expect(ctx.escrow.connect(ctx.buyer).cancel(K(ctx, "p"))).to.be.revertedWithCustomError(ctx.escrow, "NotSeller");
      await expect(ctx.escrow.connect(ctx.stranger).confirmReceipt(K(ctx, "none")))
        .to.be.revertedWithCustomError(ctx.escrow, "InvalidState");
    });
  });

  describe("cancellation and timeouts", () => {
    it("seller cancel before delivery refunds the full order (flat fee is not refunded)", async () => {
      const ctx = await setup();
      const before = await bal(ctx, ctx.buyer);
      await create(ctx, "c");
      await expect(ctx.escrow.connect(ctx.seller).cancel(K(ctx, "c")))
        .to.emit(ctx.escrow, "OrderRefunded").withArgs(K(ctx, "c"), ctx.buyer.address, usd(110), Reason.Cancelled);
      expect(before - (await bal(ctx, ctx.buyer))).to.equal(usd(0.1));
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "c")).then(
        () => expect.fail("delivered a refunded order"),
        () => {},
      );
    });

    it("not delivered by the deadline: anyone can refund the buyer, but not a moment before", async () => {
      const ctx = await setup();
      await create(ctx, "nd", { days: 3 });
      await expect(ctx.escrow.connect(ctx.stranger).claimTimeout(K(ctx, "nd"))).to.be.revertedWithCustomError(ctx.escrow, "WindowOpen");
      await time.increase(3 * DAY + 1);
      await expect(ctx.escrow.connect(ctx.stranger).claimTimeout(K(ctx, "nd")))
        .to.emit(ctx.escrow, "OrderRefunded").withArgs(K(ctx, "nd"), ctx.buyer.address, usd(110), Reason.NotDelivered);
    });

    it("delivered and silent for 7 days: anyone can release to the seller (no 90/7/3 split)", async () => {
      const ctx = await setup();
      await create(ctx, "sil");
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "sil"));
      await time.increase(7 * DAY - 10);
      await expect(ctx.escrow.claimTimeout(K(ctx, "sil"))).to.be.revertedWithCustomError(ctx.escrow, "WindowOpen");
      await time.increase(20);
      await ctx.escrow.connect(ctx.stranger).claimTimeout(K(ctx, "sil"));
      expect(await bal(ctx, ctx.seller)).to.equal(usd(108));
    });

    it("a buyer cannot dispute after the confirm window", async () => {
      const ctx = await setup();
      await create(ctx, "late");
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "late"));
      await time.increase(7 * DAY + 1);
      await expect(ctx.escrow.connect(ctx.buyer).dispute(K(ctx, "late"))).to.be.revertedWithCustomError(ctx.escrow, "WindowClosed");
    });
  });

  describe("disputes", () => {
    async function disputed(ctx, name) {
      await create(ctx, name);
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, name));
      await ctx.escrow.connect(ctx.buyer).dispute(K(ctx, name));
    }

    it("only a dispute stops the confirm-window release; resolve splits pro rata", async () => {
      const ctx = await setup();
      await disputed(ctx, "d");
      await time.increase(8 * DAY);
      await expect(ctx.escrow.claimTimeout(K(ctx, "d"))).to.be.revertedWithCustomError(ctx.escrow, "WindowOpen");
      const buyerBefore = await bal(ctx, ctx.buyer);
      // 55 back to the buyer: 10 from shipping, 45 from the price. The seller keeps 55 of the
      // 100 price, so the fee is 2 * 55/100 = 1.1 and the seller nets 55 - 1.1 = 53.9.
      await expect(ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "d"), usd(55)))
        .to.emit(ctx.escrow, "OrderResolved").withArgs(K(ctx, "d"), ctx.resolver.address, usd(55), usd(53.9), usd(1.1));
      expect((await bal(ctx, ctx.buyer)) - buyerBefore).to.equal(usd(55));
      expect(await bal(ctx, await ctx.escrow.getAddress())).to.equal(0);
    });

    it("resolve is role-gated and bounded by the order total", async () => {
      const ctx = await setup();
      await disputed(ctx, "d2");
      await expect(ctx.escrow.connect(ctx.stranger).resolve(K(ctx, "d2"), 0))
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
      await expect(ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "d2"), usd(111)))
        .to.be.revertedWithCustomError(ctx.escrow, "ToBuyerAboveTotal");
    });

    it("an unresolved dispute refunds the buyer after the 14-day backstop", async () => {
      const ctx = await setup();
      await disputed(ctx, "bs");
      await time.increase(14 * DAY + 1);
      await expect(ctx.escrow.claimTimeout(K(ctx, "bs")))
        .to.emit(ctx.escrow, "OrderRefunded").withArgs(K(ctx, "bs"), ctx.buyer.address, usd(110), Reason.DisputeBackstop);
    });
  });

  describe("pause never traps funds", () => {
    it("blocks new orders only; delivery, confirm, cancel, dispute, resolve and timeouts all still work", async () => {
      const ctx = await setup();
      await create(ctx, "a");
      await create(ctx, "b");
      await create(ctx, "c");
      await create(ctx, "d", { days: 1 });
      await ctx.escrow.connect(ctx.pauser).pause();
      await expect(create(ctx, "new")).to.be.revertedWithCustomError(ctx.escrow, "EnforcedPause");
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "a"));
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "a"));
      await ctx.escrow.connect(ctx.seller).cancel(K(ctx, "b"));
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "c"));
      await ctx.escrow.connect(ctx.buyer).dispute(K(ctx, "c"));
      await ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "c"), 0);
      await time.increase(DAY + 1);
      await ctx.escrow.claimTimeout(K(ctx, "d"));
      expect(await ctx.escrow.owed(await ctx.token.getAddress())).to.equal(0);
    });

    it("a pauser cannot unpause; only the default admin can", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.pauser).pause();
      await expect(ctx.escrow.connect(ctx.pauser).unpause())
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
      await ctx.escrow.unpause();
    });
  });

  describe("rescue is bounded to the excess", () => {
    it("can only move tokens above what open orders are owed, and pays the fee wallet", async () => {
      const ctx = await setup();
      await create(ctx, "open");
      await expect(ctx.escrow.rescue(await ctx.token.getAddress())).to.be.revertedWithCustomError(ctx.escrow, "NothingToRescue");
      await ctx.token.mint(await ctx.escrow.getAddress(), usd(5)); // a stray transfer
      const before = await bal(ctx, ctx.feeWallet);
      await expect(ctx.escrow.rescue(await ctx.token.getAddress()))
        .to.emit(ctx.escrow, "Rescued").withArgs(await ctx.token.getAddress(), ctx.feeWallet.address, usd(5));
      expect((await bal(ctx, ctx.feeWallet)) - before).to.equal(usd(5));
      // The open order still settles in full.
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "open"));
      expect(await bal(ctx, ctx.seller)).to.equal(usd(108));
    });

    it("is role-gated", async () => {
      const ctx = await setup();
      await expect(ctx.escrow.connect(ctx.stranger).rescue(await ctx.token.getAddress()))
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
    });
  });

  describe("forwarder and upgrades", () => {
    it("the trusted forwarder is fixed in the implementation and has no setter", async () => {
      const ctx = await setup();
      expect(await ctx.escrow.trustedForwarder()).to.equal(await ctx.forwarder.getAddress());
      expect(ctx.escrow.interface.fragments.some((f) => f.name === "setTrustedForwarder")).to.equal(false);
    });

    it("appending an address to calldata from an ordinary caller does not impersonate anyone", async () => {
      const ctx = await setup();
      await create(ctx, "imp");
      const data = ctx.escrow.interface.encodeFunctionData("confirmReceipt", [K(ctx, "imp")]);
      const spoofed = data + ctx.buyer.address.slice(2); // what a forwarder would append
      await expect(ctx.stranger.sendTransaction({ to: await ctx.escrow.getAddress(), data: spoofed }))
        .to.be.revertedWithCustomError(ctx.escrow, "NotBuyer");
    });

    it("a buyer can create an order gaslessly through the forwarder", async () => {
      const ctx = await setup();
      const escrowAddr = await ctx.escrow.getAddress();
      const data = ctx.escrow.interface.encodeFunctionData("createOrder", [
        id("gl"), ctx.seller.address, await ctx.token.getAddress(), usd(10), 0, 14, MAXFLAT,
      ]);
      const { chainId } = await ethers.provider.getNetwork();
      const domain = { name: "TSAForwarder", version: "1", chainId, verifyingContract: await ctx.forwarder.getAddress() };
      const types = {
        ForwardRequest: [
          { name: "from", type: "address" }, { name: "to", type: "address" }, { name: "value", type: "uint256" },
          { name: "gas", type: "uint256" }, { name: "nonce", type: "uint256" }, { name: "deadline", type: "uint48" },
          { name: "data", type: "bytes" },
        ],
      };
      const deadline = (await time.latest()) + 600;
      const req = { from: ctx.buyer.address, to: escrowAddr, value: 0, gas: 500000, nonce: await ctx.forwarder.nonces(ctx.buyer.address), deadline, data };
      const signature = await ctx.buyer.signTypedData(domain, types, req);
      await ctx.forwarder.connect(ctx.stranger).execute({ ...req, signature });
      expect((await ctx.escrow.getOrder(K(ctx, "gl"))).buyer).to.equal(ctx.buyer.address);
    });

    it("the implementation cannot be initialized, the proxy cannot be re-initialized", async () => {
      const ctx = await setup();
      await expect(ctx.escrow.initialize(ctx.stranger.address, [], [])).to.be.revertedWithCustomError(ctx.escrow, "InvalidInitialization");
      const implAddr = await upgrades.erc1967.getImplementationAddress(await ctx.escrow.getAddress());
      const impl = await ethers.getContractAt("OrderEscrow", implAddr);
      await expect(impl.initialize(ctx.stranger.address, [], [])).to.be.revertedWithCustomError(impl, "InvalidInitialization");
    });

    it("only UPGRADER_ROLE can upgrade, and state survives an upgrade", async () => {
      const ctx = await setup();
      await create(ctx, "keep");
      const Factory = await ethers.getContractFactory("OrderEscrow");
      const newImpl = await Factory.deploy(await ctx.forwarder.getAddress(), await ctx.fc.getAddress());
      await expect(ctx.escrow.connect(ctx.stranger).upgradeToAndCall(await newImpl.getAddress(), "0x"))
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
      await ctx.escrow.upgradeToAndCall(await newImpl.getAddress(), "0x");
      expect((await ctx.escrow.getOrder(K(ctx, "keep"))).state).to.equal(State.Funded);
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "keep"));
    });

    it("passes the OpenZeppelin upgrade-safety validation", async () => {
      const ctx = await setup();
      await upgrades.validateUpgrade(await ctx.escrow.getAddress(), await ethers.getContractFactory("OrderEscrow"), {
        kind: "uups",
        constructorArgs: [await ctx.forwarder.getAddress(), await ctx.fc.getAddress()],
      });
    });
  });

  it("the reentrancy guard stops a token re-entering a permissionless call mid-payout", async () => {
    const ctx = await setup();
    const evil = await (await ethers.getContractFactory("ReentrantERC20")).deploy("Evil", "EVL", 6);
    const e = await evil.getAddress();
    await ctx.fc.setRate(FEATURE, e, { bps: 200, flatFee: 0, active: true });
    await ctx.fc.setLimits(e, { maxPerOrder: usd(1000), maxOwed: usd(1000) });
    await evil.mint(ctx.buyer.address, usd(1000));
    await evil.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
    await ctx.escrow.connect(ctx.buyer).createOrder(id("re1"), ctx.seller.address, e, usd(100), 0, 14, MAXFLAT);
    await ctx.escrow.connect(ctx.buyer).createOrder(id("re2"), ctx.seller.address, e, usd(100), 0, 1, MAXFLAT);
    await time.increase(DAY + 1); // re2's delivery window has passed: claimTimeout(re2) is open to anyone

    // On the payout to the seller for re1, the token re-enters claimTimeout(re2). Without the guard
    // that inner call would refund re2 mid-payout; with it, the inner call reverts, the token's
    // transfer fails, and the payout is deferred to a claimable balance instead.
    await evil.armAttack(await ctx.escrow.getAddress(), ctx.escrow.interface.encodeFunctionData("claimTimeout", [K(ctx, "re2")]));
    await expect(ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "re1")))
      .to.emit(ctx.escrow, "PayoutDeferred").withArgs(e, ctx.seller.address, usd(98));
    expect((await ctx.escrow.getOrder(K(ctx, "re2"))).state).to.equal(State.Funded);
    expect(await ctx.escrow.claimable(e, ctx.seller.address)).to.equal(usd(98));
  });
});

describe("OrderEscrow: review follow-ups", () => {
  async function setupBlacklist() {
    const ctx = await setup();
    const bl = await (await ethers.getContractFactory("BlacklistERC20")).deploy("USD Tether", "USDT", 6);
    const b = await bl.getAddress();
    await ctx.fc.setRate(FEATURE, b, { bps: 200, flatFee: usd(0.1), active: true });
    await ctx.fc.setLimits(b, { maxPerOrder: usd(1000), maxOwed: usd(10000) });
    await bl.mint(ctx.buyer.address, usd(10000));
    await bl.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
    const make = (name, days = 14) =>
      ctx.escrow.connect(ctx.buyer).createOrder(id(name), ctx.seller.address, b, usd(100), usd(10), days, MAXFLAT);
    return { ...ctx, bl, b, make };
  }

  it("a blacklisted seller cannot freeze settlement: the payout waits as claimable and can be sent elsewhere", async () => {
    const ctx = await setupBlacklist();
    await ctx.make("bl1");
    await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "bl1"));
    await ctx.bl.setBlacklisted(ctx.seller.address, true);
    await time.increase(7 * DAY + 1);
    await expect(ctx.escrow.connect(ctx.stranger).claimTimeout(K(ctx, "bl1")))
      .to.emit(ctx.escrow, "PayoutDeferred").withArgs(ctx.b, ctx.seller.address, usd(108));
    expect(await ctx.escrow.owed(ctx.b)).to.equal(usd(108)); // still owed until claimed
    await expect(ctx.escrow.rescue(ctx.b)).to.be.revertedWithCustomError(ctx.escrow, "NothingToRescue");
    await ctx.escrow.connect(ctx.seller).claim(ctx.b, ctx.stranger.address);
    expect(await ctx.bl.balanceOf(ctx.stranger.address)).to.equal(usd(108));
    expect(await ctx.escrow.owed(ctx.b)).to.equal(0);
    await expect(ctx.escrow.connect(ctx.seller).claim(ctx.b, ctx.stranger.address))
      .to.be.revertedWithCustomError(ctx.escrow, "NothingToClaim");
  });

  it("a blacklisted fee recipient does not block releases or new orders", async () => {
    const ctx = await setupBlacklist();
    await ctx.bl.setBlacklisted(ctx.feeWallet.address, true);
    await ctx.make("fr");
    await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "fr"));
    expect(await ctx.bl.balanceOf(ctx.seller.address)).to.equal(usd(108));
    expect(await ctx.escrow.claimable(ctx.b, ctx.feeWallet.address)).to.equal(usd(2) + usd(0.1));
  });

  it("a late delivery mark is refused, so the seller cannot front-run the buyer's refund", async () => {
    const ctx = await setup();
    await create(ctx, "late", { days: 2 });
    await time.increase(2 * DAY + 1);
    await expect(ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "late")))
      .to.be.revertedWithCustomError(ctx.escrow, "WindowClosed");
    await ctx.escrow.connect(ctx.stranger).claimTimeout(K(ctx, "late"));
    expect((await ctx.escrow.getOrder(K(ctx, "late"))).state).to.equal(State.Refunded);
  });

  it("nobody can take a buyer's order id first: the same id from another account is a different order", async () => {
    const ctx = await setup();
    const t = await ctx.token.getAddress();
    await ctx.token.mint(ctx.stranger.address, usd(100));
    await ctx.token.connect(ctx.stranger).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
    await ctx.escrow.connect(ctx.stranger).createOrder(id("same"), ctx.seller.address, t, usd(1), 0, 1, MAXFLAT);
    await create(ctx, "same");
    expect((await ctx.escrow.getOrder(K(ctx, "same"))).buyer).to.equal(ctx.buyer.address);
    expect((await ctx.escrow.getOrder(K(ctx, "same", ctx.stranger))).buyer).to.equal(ctx.stranger.address);
    expect(await ctx.escrow.orderKey(ctx.buyer.address, id("same"))).to.equal(K(ctx, "same"));
  });

  it("the buyer's flat-fee ceiling protects a signed request from a later fee rise", async () => {
    const ctx = await setup();
    await ctx.fc.setRate(FEATURE, await ctx.token.getAddress(), { bps: 200, flatFee: usd(5), active: true });
    await expect(create(ctx, "ff", { maxFlat: usd(0.1) })).to.be.revertedWithCustomError(ctx.escrow, "FlatFeeAboveMax");
    await create(ctx, "ff", { maxFlat: usd(5) });
  });

  describe("resolve boundaries", () => {
    async function disputedOrder(ctx, name, opts) {
      await create(ctx, name, opts);
      await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, name));
      await ctx.escrow.connect(ctx.buyer).dispute(K(ctx, name));
    }

    it("all to the seller: seller nets price + shipping − full fee", async () => {
      const ctx = await setup();
      await disputedOrder(ctx, "r0");
      await expect(ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "r0"), 0))
        .to.emit(ctx.escrow, "OrderResolved").withArgs(K(ctx, "r0"), ctx.resolver.address, 0, usd(108), usd(2));
    });

    it("all to the buyer: no fee", async () => {
      const ctx = await setup();
      await disputedOrder(ctx, "rall");
      await expect(ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "rall"), usd(110)))
        .to.emit(ctx.escrow, "OrderResolved").withArgs(K(ctx, "rall"), ctx.resolver.address, usd(110), 0, 0);
    });

    it("refunding only the shipping keeps the full fee on the price", async () => {
      const ctx = await setup();
      await disputedOrder(ctx, "rship");
      await expect(ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "rship"), usd(10)))
        .to.emit(ctx.escrow, "OrderResolved").withArgs(K(ctx, "rship"), ctx.resolver.address, usd(10), usd(98), usd(2));
    });

    it("odd amounts: the three parts always add up to the order total", async () => {
      const ctx = await setup();
      for (const [i, toBuyer] of [1n, 7n, 33_333n, 50_000_001n, 109_999_999n].entries()) {
        const name = `odd${i}`;
        await disputedOrder(ctx, name, { product: 100_000_003n, shipping: 9_999_997n });
        const before = [await bal(ctx, ctx.buyer), await bal(ctx, ctx.seller), await bal(ctx, ctx.feeWallet)];
        await ctx.escrow.connect(ctx.resolver).resolve(K(ctx, name), toBuyer);
        const after = [await bal(ctx, ctx.buyer), await bal(ctx, ctx.seller), await bal(ctx, ctx.feeWallet)];
        const moved = after.reduce((a, v, j) => a + (v - before[j]), 0n);
        expect(moved).to.equal(110_000_000n);
        const fee = after[2] - before[2];
        expect(fee <= (await ctx.escrow.getOrder(K(ctx, name))).merchantFee).to.equal(true);
      }
      expect(await bal(ctx, await ctx.escrow.getAddress())).to.equal(0);
    });

    it("the resolver can still act after the backstop date, until someone claims the backstop", async () => {
      const ctx = await setup();
      await disputedOrder(ctx, "rlate");
      await time.increase(15 * DAY);
      await ctx.escrow.connect(ctx.resolver).resolve(K(ctx, "rlate"), 0);
      expect((await ctx.escrow.getOrder(K(ctx, "rlate"))).state).to.equal(State.Resolved);
    });
  });

  it("a dispute in the last second of the confirm window is accepted", async () => {
    const ctx = await setup();
    await create(ctx, "edge");
    await ctx.escrow.connect(ctx.seller).markDelivered(K(ctx, "edge"));
    const confirmBy = (await ctx.escrow.getOrder(K(ctx, "edge"))).confirmBy;
    await time.setNextBlockTimestamp(confirmBy);
    await ctx.escrow.connect(ctx.buyer).dispute(K(ctx, "edge"));
  });

  it("18-decimal tokens with large amounts work, and an oversized fee is refused", async () => {
    const ctx = await setup();
    const big = await (await ethers.getContractFactory("MockERC20")).deploy("BSC USDT", "USDT", 18);
    const g = await big.getAddress();
    const max128 = (1n << 128n) - 1n;
    await ctx.fc.setRate(FEATURE, g, { bps: 300, flatFee: ethers.parseEther("0.1"), active: true });
    await ctx.fc.setLimits(g, { maxPerOrder: max128, maxOwed: max128 });
    await big.mint(ctx.buyer.address, max128);
    await big.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
    const ok = ethers.parseEther("1000000"); // $1m in 18 decimals
    await ctx.escrow.connect(ctx.buyer).createOrder(id("big"), ctx.seller.address, g, ok, 0, 14, MAXFLAT);
    expect((await ctx.escrow.getOrder(K(ctx, "big"))).merchantFee).to.equal(ethers.parseEther("30000"));
    // 3% of 2^102 wei (about 1.5e29) exceeds the uint96 fee field (about 7.9e28).
    await expect(ctx.escrow.connect(ctx.buyer).createOrder(id("huge"), ctx.seller.address, g, 1n << 102n, 0, 14, MAXFLAT))
      .to.be.revertedWithCustomError(ctx.escrow, "AmountTooLarge");
  });

  it("lowering the limits below what is already owed never blocks an exit", async () => {
    const ctx = await setup();
    await create(ctx, "lim1");
    await create(ctx, "lim2");
    await ctx.fc.setLimits(await ctx.token.getAddress(), { maxPerOrder: 1n, maxOwed: 1n });
    await ctx.escrow.connect(ctx.buyer).confirmReceipt(K(ctx, "lim1"));
    await ctx.escrow.connect(ctx.seller).cancel(K(ctx, "lim2"));
    await expect(create(ctx, "lim3")).to.be.revertedWithCustomError(ctx.escrow, "OrderAboveLimit");
  });

  it("the admin role moves only by the two-step handover, never by grantRole", async () => {
    const ctx = await setup();
    const ADMIN = await ctx.escrow.DEFAULT_ADMIN_ROLE();
    await expect(ctx.escrow.grantRole(ADMIN, ctx.stranger.address))
      .to.be.revertedWithCustomError(ctx.escrow, "AccessControlEnforcedDefaultAdminRules");
    await ctx.escrow.beginDefaultAdminTransfer(ctx.stranger.address);
    await time.increase(1);
    await ctx.escrow.connect(ctx.stranger).acceptDefaultAdminTransfer();
    expect(await ctx.escrow.defaultAdmin()).to.equal(ctx.stranger.address);
  });
});
