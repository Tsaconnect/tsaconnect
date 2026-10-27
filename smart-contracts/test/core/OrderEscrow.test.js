const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

// OrderEscrow + FeeConfig: the audit-ready order escrow (docs/specs/2026-10-27-contracts-audit-ready-redesign.md).

const DAY = 24 * 60 * 60;
const FEATURE = ethers.encodeBytes32String("order");
const usd = (n) => ethers.parseUnits(String(n), 6);
const id = (s) => ethers.id(s);

const State = { None: 0, Funded: 1, Delivered: 2, Disputed: 3, Released: 4, Refunded: 5, Resolved: 6 };
const Reason = { Cancelled: 0, NotDelivered: 1, DisputeBackstop: 2 };

async function deployFeeConfig(admin, recipient) {
  return upgrades.deployProxy(await ethers.getContractFactory("FeeConfig"), [admin.address, recipient.address], {
    kind: "uups",
  });
}

async function deployEscrow(admin, forwarder, feeConfig) {
  return upgrades.deployProxy(await ethers.getContractFactory("OrderEscrow"), [admin.address], {
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
  await fc.setRate(FEATURE, await token.getAddress(), { bps, flat, active: true });
  await fc.setLimits(await token.getAddress(), { maxPerOrder, maxOwed });
  await escrow.grantRole(await escrow.RESOLVER_ROLE(), resolver.address);
  await escrow.grantRole(await escrow.PAUSER_ROLE(), pauser.address);
  return { admin, buyer, seller, feeWallet, resolver, stranger, pauser, fc, forwarder, escrow, token };
}

async function create(ctx, orderId, { product = usd(100), shipping = usd(10), days = 14 } = {}) {
  return ctx.escrow
    .connect(ctx.buyer)
    .createOrder(orderId, ctx.seller.address, await ctx.token.getAddress(), product, shipping, days);
}

const bal = (ctx, who) => ctx.token.balanceOf(who.address ?? who);

describe("FeeConfig", () => {
  it("fails closed: an unset or inactive rate reverts, so an unconfigured token cannot be used", async () => {
    const ctx = await setup();
    const other = ethers.Wallet.createRandom().address;
    await expect(ctx.fc.rateFor(FEATURE, other)).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    await ctx.fc.setRate(FEATURE, other, { bps: 100, flat: 0, active: false });
    await expect(ctx.fc.rateFor(FEATURE, other)).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    await expect(ctx.fc.limitsFor(other)).to.be.revertedWithCustomError(ctx.fc, "LimitsNotConfigured");
  });

  it("caps rates, rejects zero limits and the zero address, and is role-gated", async () => {
    const ctx = await setup();
    const t = await ctx.token.getAddress();
    await expect(ctx.fc.setRate(FEATURE, t, { bps: 1001, flat: 0, active: true }))
      .to.be.revertedWithCustomError(ctx.fc, "RateTooHigh");
    await expect(ctx.fc.setLimits(t, { maxPerOrder: 0, maxOwed: 1 })).to.be.revertedWithCustomError(ctx.fc, "ZeroLimit");
    await expect(ctx.fc.setFeeRecipient(ethers.ZeroAddress)).to.be.revertedWithCustomError(ctx.fc, "ZeroAddress");
    await expect(ctx.fc.connect(ctx.stranger).setFeeRecipient(ctx.stranger.address))
      .to.be.revertedWithCustomError(ctx.fc, "AccessControlUnauthorizedAccount");
    await expect(ctx.fc.setFeeRecipient(ctx.stranger.address))
      .to.emit(ctx.fc, "FeeRecipientSet").withArgs(ctx.feeWallet.address, ctx.stranger.address);
  });

  it("batch setRates is all-or-nothing", async () => {
    const ctx = await setup();
    const a = ethers.Wallet.createRandom().address;
    const b = ethers.Wallet.createRandom().address;
    await expect(
      ctx.fc.setRates([FEATURE, FEATURE], [a, b], [
        { bps: 100, flat: 0, active: true },
        { bps: 5000, flat: 0, active: true },
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
      await expect(create(ctx, id("o1")))
        .to.emit(ctx.escrow, "OrderCreated");
      expect(before - (await bal(ctx, ctx.buyer))).to.equal(usd(110.1));
      expect(await bal(ctx, ctx.feeWallet)).to.equal(usd(0.1));
      expect(await ctx.escrow.owed(await ctx.token.getAddress())).to.equal(usd(110));
      const o = await ctx.escrow.getOrder(id("o1"));
      expect(o.state).to.equal(State.Funded);
      expect(o.merchantFee).to.equal(usd(2)); // 2% of the product price only, not shipping
    });

    it("rejects duplicates, self-dealing, zero amounts, bad handling days, unconfigured tokens", async () => {
      const ctx = await setup();
      await create(ctx, id("dup"));
      await expect(create(ctx, id("dup"))).to.be.revertedWithCustomError(ctx.escrow, "OrderExists");
      const t = await ctx.token.getAddress();
      await expect(ctx.escrow.connect(ctx.buyer).createOrder(id("x"), ctx.buyer.address, t, usd(1), 0, 14))
        .to.be.revertedWithCustomError(ctx.escrow, "InvalidSeller");
      await expect(ctx.escrow.connect(ctx.buyer).createOrder(id("x"), ctx.seller.address, t, 0, 0, 14))
        .to.be.revertedWithCustomError(ctx.escrow, "ZeroAmount");
      await expect(create(ctx, id("x"), { days: 0 })).to.be.revertedWithCustomError(ctx.escrow, "InvalidHandlingDays");
      await expect(create(ctx, id("x"), { days: 31 })).to.be.revertedWithCustomError(ctx.escrow, "InvalidHandlingDays");
      const other = await (await ethers.getContractFactory("MockERC20")).deploy("X", "X", 6);
      await expect(
        ctx.escrow.connect(ctx.buyer).createOrder(id("x"), ctx.seller.address, await other.getAddress(), usd(1), 0, 14),
      ).to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    });

    it("enforces the per-order and total owed limits", async () => {
      const ctx = await setup({ maxPerOrder: usd(100), maxOwed: usd(150) });
      await expect(create(ctx, id("big"), { product: usd(95), shipping: usd(10) }))
        .to.be.revertedWithCustomError(ctx.escrow, "OrderAboveLimit");
      await create(ctx, id("a"), { product: usd(90), shipping: 0 });
      await expect(create(ctx, id("b"), { product: usd(90), shipping: 0 }))
        .to.be.revertedWithCustomError(ctx.escrow, "OwedLimitExceeded");
    });

    it("refuses a fee above its own cap even if FeeConfig allows it", async () => {
      const ctx = await setup({ bps: 301 });
      await expect(create(ctx, id("o"))).to.be.revertedWithCustomError(ctx.escrow, "FeeAboveCap");
    });

    it("rejects fee-on-transfer tokens", async () => {
      const ctx = await setup();
      const fot = await (await ethers.getContractFactory("FeeOnTransferERC20")).deploy();
      const f = await fot.getAddress();
      await ctx.fc.setRate(FEATURE, f, { bps: 200, flat: 0, active: true });
      await ctx.fc.setLimits(f, { maxPerOrder: ethers.MaxUint256 >> 129n, maxOwed: ethers.MaxUint256 >> 129n });
      await fot.mint(ctx.buyer.address, ethers.parseEther("10"));
      await fot.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
      await expect(
        ctx.escrow.connect(ctx.buyer).createOrder(id("fot"), ctx.seller.address, f, ethers.parseEther("1"), 0, 14),
      ).to.be.revertedWithCustomError(ctx.escrow, "AmountMismatch");
    });

    it("snapshots the fee: a later rate change does not touch an open order", async () => {
      const ctx = await setup();
      await create(ctx, id("snap"));
      await ctx.fc.setRate(FEATURE, await ctx.token.getAddress(), { bps: 0, flat: 0, active: true });
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(id("snap"));
      expect(await bal(ctx, ctx.feeWallet)).to.equal(usd(0.1) + usd(2));
    });
  });

  describe("happy path and seller-pays fee", () => {
    it("deliver → confirm pays the seller price + shipping − 2% of price, and the fee to the fee wallet", async () => {
      const ctx = await setup();
      await create(ctx, id("h"));
      await expect(ctx.escrow.connect(ctx.seller).markDelivered(id("h"))).to.emit(ctx.escrow, "OrderDelivered");
      await expect(ctx.escrow.connect(ctx.buyer).confirmReceipt(id("h")))
        .to.emit(ctx.escrow, "OrderReleased")
        .withArgs(id("h"), ctx.seller.address, usd(108), usd(2), ctx.feeWallet.address);
      expect(await bal(ctx, ctx.seller)).to.equal(usd(108));
      expect(await ctx.escrow.owed(await ctx.token.getAddress())).to.equal(0);
      expect(await bal(ctx, await ctx.escrow.getAddress())).to.equal(0);
    });

    it("the fee is paid to whatever recipient is configured at settlement", async () => {
      const ctx = await setup();
      await create(ctx, id("r"));
      await ctx.fc.setFeeRecipient(ctx.stranger.address);
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(id("r"));
      expect(await bal(ctx, ctx.stranger)).to.equal(usd(2));
    });

    it("only the right party can act", async () => {
      const ctx = await setup();
      await create(ctx, id("p"));
      await expect(ctx.escrow.connect(ctx.buyer).markDelivered(id("p"))).to.be.revertedWithCustomError(ctx.escrow, "NotSeller");
      await expect(ctx.escrow.connect(ctx.seller).confirmReceipt(id("p"))).to.be.revertedWithCustomError(ctx.escrow, "NotBuyer");
      await expect(ctx.escrow.connect(ctx.buyer).cancel(id("p"))).to.be.revertedWithCustomError(ctx.escrow, "NotSeller");
      await expect(ctx.escrow.connect(ctx.stranger).confirmReceipt(id("none")))
        .to.be.revertedWithCustomError(ctx.escrow, "InvalidState");
    });
  });

  describe("cancellation and timeouts", () => {
    it("seller cancel before delivery refunds the full order (flat fee is not refunded)", async () => {
      const ctx = await setup();
      const before = await bal(ctx, ctx.buyer);
      await create(ctx, id("c"));
      await expect(ctx.escrow.connect(ctx.seller).cancel(id("c")))
        .to.emit(ctx.escrow, "OrderRefunded").withArgs(id("c"), ctx.buyer.address, usd(110), Reason.Cancelled);
      expect(before - (await bal(ctx, ctx.buyer))).to.equal(usd(0.1));
      await ctx.escrow.connect(ctx.seller).markDelivered(id("c")).then(
        () => expect.fail("delivered a refunded order"),
        () => {},
      );
    });

    it("not delivered by the deadline: anyone can refund the buyer, but not a moment before", async () => {
      const ctx = await setup();
      await create(ctx, id("nd"), { days: 3 });
      await expect(ctx.escrow.connect(ctx.stranger).claimTimeout(id("nd"))).to.be.revertedWithCustomError(ctx.escrow, "WindowOpen");
      await time.increase(3 * DAY + 1);
      await expect(ctx.escrow.connect(ctx.stranger).claimTimeout(id("nd")))
        .to.emit(ctx.escrow, "OrderRefunded").withArgs(id("nd"), ctx.buyer.address, usd(110), Reason.NotDelivered);
    });

    it("delivered and silent for 7 days: anyone can release to the seller (no 90/7/3 split)", async () => {
      const ctx = await setup();
      await create(ctx, id("sil"));
      await ctx.escrow.connect(ctx.seller).markDelivered(id("sil"));
      await time.increase(7 * DAY - 10);
      await expect(ctx.escrow.claimTimeout(id("sil"))).to.be.revertedWithCustomError(ctx.escrow, "WindowOpen");
      await time.increase(20);
      await ctx.escrow.connect(ctx.stranger).claimTimeout(id("sil"));
      expect(await bal(ctx, ctx.seller)).to.equal(usd(108));
    });

    it("a buyer cannot dispute after the confirm window", async () => {
      const ctx = await setup();
      await create(ctx, id("late"));
      await ctx.escrow.connect(ctx.seller).markDelivered(id("late"));
      await time.increase(7 * DAY + 1);
      await expect(ctx.escrow.connect(ctx.buyer).dispute(id("late"))).to.be.revertedWithCustomError(ctx.escrow, "WindowClosed");
    });
  });

  describe("disputes", () => {
    async function disputed(ctx, orderId) {
      await create(ctx, orderId);
      await ctx.escrow.connect(ctx.seller).markDelivered(orderId);
      await ctx.escrow.connect(ctx.buyer).dispute(orderId);
    }

    it("only a dispute stops the confirm-window release; resolve splits pro rata", async () => {
      const ctx = await setup();
      await disputed(ctx, id("d"));
      await time.increase(8 * DAY);
      await expect(ctx.escrow.claimTimeout(id("d"))).to.be.revertedWithCustomError(ctx.escrow, "WindowOpen");
      const buyerBefore = await bal(ctx, ctx.buyer);
      // Half back to the buyer: seller gross 55, fee 2 * 55/110 = 1, seller nets 54.
      await expect(ctx.escrow.connect(ctx.resolver).resolve(id("d"), usd(55)))
        .to.emit(ctx.escrow, "OrderResolved").withArgs(id("d"), ctx.resolver.address, usd(55), usd(54), usd(1));
      expect((await bal(ctx, ctx.buyer)) - buyerBefore).to.equal(usd(55));
      expect(await bal(ctx, await ctx.escrow.getAddress())).to.equal(0);
    });

    it("resolve is role-gated and bounded by the order total", async () => {
      const ctx = await setup();
      await disputed(ctx, id("d2"));
      await expect(ctx.escrow.connect(ctx.stranger).resolve(id("d2"), 0))
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
      await expect(ctx.escrow.connect(ctx.resolver).resolve(id("d2"), usd(111)))
        .to.be.revertedWithCustomError(ctx.escrow, "ToBuyerAboveTotal");
    });

    it("an unresolved dispute refunds the buyer after the 14-day backstop", async () => {
      const ctx = await setup();
      await disputed(ctx, id("bs"));
      await time.increase(14 * DAY + 1);
      await expect(ctx.escrow.claimTimeout(id("bs")))
        .to.emit(ctx.escrow, "OrderRefunded").withArgs(id("bs"), ctx.buyer.address, usd(110), Reason.DisputeBackstop);
    });
  });

  describe("pause never traps funds", () => {
    it("blocks new orders only; delivery, confirm, cancel, dispute, resolve and timeouts all still work", async () => {
      const ctx = await setup();
      await create(ctx, id("a"));
      await create(ctx, id("b"));
      await create(ctx, id("c"));
      await create(ctx, id("d"), { days: 1 });
      await ctx.escrow.connect(ctx.pauser).pause();
      await expect(create(ctx, id("new"))).to.be.revertedWithCustomError(ctx.escrow, "EnforcedPause");
      await ctx.escrow.connect(ctx.seller).markDelivered(id("a"));
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(id("a"));
      await ctx.escrow.connect(ctx.seller).cancel(id("b"));
      await ctx.escrow.connect(ctx.seller).markDelivered(id("c"));
      await ctx.escrow.connect(ctx.buyer).dispute(id("c"));
      await ctx.escrow.connect(ctx.resolver).resolve(id("c"), 0);
      await time.increase(DAY + 1);
      await ctx.escrow.claimTimeout(id("d"));
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
      await create(ctx, id("open"));
      await expect(ctx.escrow.rescue(await ctx.token.getAddress())).to.be.revertedWithCustomError(ctx.escrow, "NothingToRescue");
      await ctx.token.mint(await ctx.escrow.getAddress(), usd(5)); // a stray transfer
      const before = await bal(ctx, ctx.feeWallet);
      await expect(ctx.escrow.rescue(await ctx.token.getAddress()))
        .to.emit(ctx.escrow, "Rescued").withArgs(await ctx.token.getAddress(), ctx.feeWallet.address, usd(5));
      expect((await bal(ctx, ctx.feeWallet)) - before).to.equal(usd(5));
      // The open order still settles in full.
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(id("open"));
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
      await create(ctx, id("imp"));
      const data = ctx.escrow.interface.encodeFunctionData("confirmReceipt", [id("imp")]);
      const spoofed = data + ctx.buyer.address.slice(2); // what a forwarder would append
      await expect(ctx.stranger.sendTransaction({ to: await ctx.escrow.getAddress(), data: spoofed }))
        .to.be.revertedWithCustomError(ctx.escrow, "NotBuyer");
    });

    it("a buyer can create an order gaslessly through the forwarder", async () => {
      const ctx = await setup();
      const escrowAddr = await ctx.escrow.getAddress();
      const data = ctx.escrow.interface.encodeFunctionData("createOrder", [
        id("gl"), ctx.seller.address, await ctx.token.getAddress(), usd(10), 0, 14,
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
      expect((await ctx.escrow.getOrder(id("gl"))).buyer).to.equal(ctx.buyer.address);
    });

    it("the implementation cannot be initialized, the proxy cannot be re-initialized", async () => {
      const ctx = await setup();
      await expect(ctx.escrow.initialize(ctx.stranger.address)).to.be.revertedWithCustomError(ctx.escrow, "InvalidInitialization");
      const implAddr = await upgrades.erc1967.getImplementationAddress(await ctx.escrow.getAddress());
      const impl = await ethers.getContractAt("OrderEscrow", implAddr);
      await expect(impl.initialize(ctx.stranger.address)).to.be.revertedWithCustomError(impl, "InvalidInitialization");
    });

    it("only UPGRADER_ROLE can upgrade, and state survives an upgrade", async () => {
      const ctx = await setup();
      await create(ctx, id("keep"));
      const Factory = await ethers.getContractFactory("OrderEscrow");
      const newImpl = await Factory.deploy(await ctx.forwarder.getAddress(), await ctx.fc.getAddress());
      await expect(ctx.escrow.connect(ctx.stranger).upgradeToAndCall(await newImpl.getAddress(), "0x"))
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
      await ctx.escrow.upgradeToAndCall(await newImpl.getAddress(), "0x");
      expect((await ctx.escrow.getOrder(id("keep"))).state).to.equal(State.Funded);
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(id("keep"));
    });

    it("passes the OpenZeppelin upgrade-safety validation", async () => {
      const ctx = await setup();
      await upgrades.validateUpgrade(await ctx.escrow.getAddress(), await ethers.getContractFactory("OrderEscrow"), {
        kind: "uups",
        constructorArgs: [await ctx.forwarder.getAddress(), await ctx.fc.getAddress()],
      });
    });
  });

  it("blocks reentrancy through a malicious token", async () => {
    const ctx = await setup();
    const evil = await (await ethers.getContractFactory("ReentrantERC20")).deploy("Evil", "EVL", 6);
    const e = await evil.getAddress();
    await ctx.fc.setRate(FEATURE, e, { bps: 200, flat: 0, active: true });
    await ctx.fc.setLimits(e, { maxPerOrder: usd(1000), maxOwed: usd(1000) });
    await evil.mint(ctx.buyer.address, usd(1000));
    await evil.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), ethers.MaxUint256);
    await ctx.escrow.connect(ctx.buyer).createOrder(id("re"), ctx.seller.address, e, usd(100), 0, 14);
    // On the payout to the seller the token re-enters confirmReceipt; the guard must revert the whole tx.
    await evil.armAttack(await ctx.escrow.getAddress(), ctx.escrow.interface.encodeFunctionData("confirmReceipt", [id("re")]));
    await expect(ctx.escrow.connect(ctx.buyer).confirmReceipt(id("re"))).to.be.reverted;
    expect((await ctx.escrow.getOrder(id("re"))).state).to.equal(State.Funded);
  });
});
