const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

// P2PEscrow: the redeployed P2P escrow (docs/specs/2026-10-27-contracts-audit-ready-redesign.md §6.3, §14).

const PLATFORM = ethers.encodeBytes32String("p2p_platform");
const GAS = ethers.encodeBytes32String("p2p_gas");
const MIN = 60;
const DAY = 24 * 60 * 60;
const usd = (n) => ethers.parseUnits(String(n), 6);
const id = (s) => ethers.id(s);
const coder = ethers.AbiCoder.defaultAbiCoder();
const keyOf = (opener, name) => ethers.keccak256(coder.encode(["address", "bytes32"], [opener.address, id(name)]));
const Status = { None: 0, Pending: 1, Paid: 2, Released: 3, Cancelled: 4, Disputed: 5, Resolved: 6 };

async function setup({ platformBps = 100, gasBps = 50, maxPerOrder = usd(5000), maxOwed = usd(100000), tokenKind = "MockERC20" } = {}) {
  const [admin, seller, buyer, feeWallet, resolver, stranger, pauser, releaser, merchant] = await ethers.getSigners();
  const fc = await upgrades.deployProxy(await ethers.getContractFactory("FeeConfig"), [admin.address, feeWallet.address, { features: [], tokens: [], rates: [], limitTokens: [], limits: [] }], { kind: "uups" });
  const forwarder = await (await ethers.getContractFactory("TSAForwarder")).deploy("TSAForwarder");
  const escrow = await upgrades.deployProxy(await ethers.getContractFactory("contracts/core/P2PEscrow.sol:P2PEscrow"), [admin.address, [], []], {
    kind: "uups",
    constructorArgs: [await forwarder.getAddress(), await fc.getAddress()],
  });
  const token = await (await ethers.getContractFactory(tokenKind)).deploy("USD Tether", "USDT", 6);
  const T = await token.getAddress();
  await fc.setRate(PLATFORM, T, { bps: platformBps, flatFee: 0, active: true });
  await fc.setRate(GAS, T, { bps: gasBps, flatFee: 0, active: true });
  await fc.setLimits(T, { maxPerOrder, maxOwed });
  for (const who of [seller, buyer]) {
    await token.mint(who.address, usd(100000));
    await token.connect(who).approve(await escrow.getAddress(), ethers.MaxUint256);
  }
  await escrow.grantRole(await escrow.RESOLVER_ROLE(), resolver.address);
  await escrow.grantRole(await escrow.PAUSER_ROLE(), pauser.address);
  return { admin, seller, buyer, feeWallet, resolver, stranger, pauser, releaser, merchant, fc, forwarder, escrow, token, T };
}

async function voucher(ctx, overrides = {}) {
  const v = {
    seller: ctx.seller.address,
    token: ctx.T,
    maxAmount: usd(1000),
    minPerTrade: usd(10),
    maxPerTrade: usd(500),
    deadline: (await time.latest()) + DAY,
    nonce: ethers.id(`n-${Math.random()}`),
    ...overrides,
  };
  const { chainId } = await ethers.provider.getNetwork();
  const domain = { name: "TSAConnectP2PEscrow", version: "3", chainId, verifyingContract: await ctx.escrow.getAddress() };
  const types = {
    AdVoucher: [
      { name: "seller", type: "address" }, { name: "token", type: "address" }, { name: "maxAmount", type: "uint256" },
      { name: "minPerTrade", type: "uint256" }, { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
      { name: "nonce", type: "bytes32" },
    ],
  };
  const signer = overrides.signer ?? ctx.seller;
  delete v.signer;
  return { v, sig: await signer.signTypedData(domain, types, v) };
}

async function openPooled(ctx, name, amount = usd(100)) {
  await ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(1000));
  const { v, sig } = await voucher(ctx);
  await ctx.escrow.connect(ctx.buyer).openTrade(id(name), v, sig, amount);
  return keyOf(ctx.buyer, name);
}

const bal = (ctx, who) => ctx.token.balanceOf(who.address ?? who);

describe("P2PEscrow", () => {
  describe("pool", () => {
    it("deposit and withdraw track owed; withdraw works while paused and needs no token config", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(500));
      expect(await ctx.escrow.owed(ctx.T)).to.equal(usd(500));
      await ctx.escrow.connect(ctx.pauser).pause();
      await ctx.fc.setRate(PLATFORM, ctx.T, { bps: 0, flatFee: 0, active: false }); // token delisted
      await ctx.escrow.connect(ctx.seller).withdraw(ctx.T, usd(500));
      expect(await ctx.escrow.owed(ctx.T)).to.equal(0);
    });

    it("refuses deposits of unconfigured tokens and over the owed limit", async () => {
      const ctx = await setup({ maxOwed: usd(100) });
      await expect(ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(101))).to.be.revertedWithCustomError(ctx.escrow, "OwedLimitExceeded");
      const other = await (await ethers.getContractFactory("MockERC20")).deploy("X", "X", 6);
      await expect(ctx.escrow.connect(ctx.seller).deposit(await other.getAddress(), 1))
        .to.be.revertedWithCustomError(ctx.fc, "FeeNotConfigured");
    });
  });

  describe("trades", () => {
    it("open → paid → seller release pays the buyer minus the snapshotted fee", async () => {
      const ctx = await setup();
      const k = await openPooled(ctx, "t1");
      await ctx.escrow.connect(ctx.buyer).markPaid(k);
      await ctx.fc.setRate(PLATFORM, ctx.T, { bps: 300, flatFee: 0, active: true }); // later change: ignored
      await expect(ctx.escrow.connect(ctx.seller).release(k))
        .to.emit(ctx.escrow, "TradeReleased").withArgs(k, ctx.buyer.address, usd(98.5), usd(1.5));
      expect(await bal(ctx, ctx.feeWallet)).to.equal(usd(1.5));
      expect(await ctx.escrow.owed(ctx.T)).to.equal(usd(900));
    });

    it("trade ids are per opener: nobody can take a buyer's id first", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(1000));
      const a = await voucher(ctx);
      await ctx.escrow.connect(ctx.stranger).openTrade(id("same"), a.v, a.sig, usd(10));
      const b = await voucher(ctx);
      await ctx.escrow.connect(ctx.buyer).openTrade(id("same"), b.v, b.sig, usd(10));
      expect((await ctx.escrow.getTrade(keyOf(ctx.buyer, "same"))).buyer).to.equal(ctx.buyer.address);
      expect(await ctx.escrow.tradeKey(ctx.buyer.address, id("same"))).to.equal(keyOf(ctx.buyer, "same"));
    });

    it("rejects bad vouchers, self-trades, over-capacity and over-limit trades", async () => {
      const ctx = await setup({ maxPerOrder: usd(200) });
      await ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(1000));
      const forged = await voucher(ctx, { signer: ctx.stranger });
      await expect(ctx.escrow.connect(ctx.buyer).openTrade(id("f"), forged.v, forged.sig, usd(10)))
        .to.be.revertedWithCustomError(ctx.escrow, "BadVoucherSignature");
      const ok = await voucher(ctx);
      await expect(ctx.escrow.connect(ctx.seller).openTrade(id("s"), ok.v, ok.sig, usd(10)))
        .to.be.revertedWithCustomError(ctx.escrow, "SelfTrade");
      await expect(ctx.escrow.connect(ctx.buyer).openTrade(id("big"), ok.v, ok.sig, usd(300)))
        .to.be.revertedWithCustomError(ctx.escrow, "AmountAboveLimit");
      const small = await voucher(ctx, { maxAmount: usd(50), maxPerTrade: usd(50) });
      await ctx.escrow.connect(ctx.buyer).openTrade(id("c1"), small.v, small.sig, usd(40));
      await expect(ctx.escrow.connect(ctx.buyer).openTrade(id("c2"), small.v, small.sig, usd(20)))
        .to.be.revertedWithCustomError(ctx.escrow, "ExceedsVoucherCapacity");
    });

    it("refuses a fee snapshot above the escrow's own caps", async () => {
      const ctx = await setup({ platformBps: 300, gasBps: 250 });
      await ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(1000));
      const { v, sig } = await voucher(ctx);
      await expect(ctx.escrow.connect(ctx.buyer).openTrade(id("x"), v, sig, usd(10))).to.be.revertedWithCustomError(ctx.escrow, "FeeAboveCap");
    });

    it("unpaid trades cancel back to the pool after the snapshotted 30-minute window", async () => {
      const ctx = await setup();
      const k = await openPooled(ctx, "c");
      await expect(ctx.escrow.connect(ctx.stranger).cancelByTimeout(k)).to.be.revertedWithCustomError(ctx.escrow, "TimeoutNotElapsed");
      await time.increase(30 * MIN);
      await expect(ctx.escrow.connect(ctx.stranger).cancelByTimeout(k)).to.emit(ctx.escrow, "TradeCancelled").withArgs(k, usd(100), true);
      const [available, locked] = await ctx.escrow.balanceOf(ctx.seller.address, ctx.T);
      expect(available).to.equal(usd(1000));
      expect(locked).to.equal(0);
    });

    it("a buy-ad deposit trade releases to the merchant, or cancels back to the depositing user", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.seller).openTradeWithDeposit(id("bd"), ctx.T, usd(100), ctx.merchant.address);
      const k = keyOf(ctx.seller, "bd");
      expect(await ctx.escrow.owed(ctx.T)).to.equal(usd(100));
      await ctx.escrow.connect(ctx.merchant).markPaid(k);
      await ctx.escrow.connect(ctx.seller).release(k);
      expect(await bal(ctx, ctx.merchant)).to.equal(usd(98.5));

      await ctx.escrow.connect(ctx.seller).openTradeWithDeposit(id("bd2"), ctx.T, usd(100), ctx.merchant.address);
      const before = await bal(ctx, ctx.seller);
      await time.increase(30 * MIN);
      await expect(ctx.escrow.cancelByTimeout(keyOf(ctx.seller, "bd2"))).to.emit(ctx.escrow, "TradeCancelled").withArgs(keyOf(ctx.seller, "bd2"), usd(100), false);
      expect((await bal(ctx, ctx.seller)) - before).to.equal(usd(100));
      expect(await ctx.escrow.owed(ctx.T)).to.equal(0);
    });
  });

  describe("no global release power", () => {
    it("only the seller or the seller's named releaser can release; admins cannot", async () => {
      const ctx = await setup();
      const k = await openPooled(ctx, "r");
      await expect(ctx.escrow.connect(ctx.admin).release(k)).to.be.revertedWithCustomError(ctx.escrow, "NotReleaser");
      await expect(ctx.escrow.connect(ctx.releaser).release(k)).to.be.revertedWithCustomError(ctx.escrow, "NotReleaser");
      expect(ctx.escrow.interface.fragments.some((f) => f.name === "adminRelease")).to.equal(false);
      await ctx.escrow.connect(ctx.seller).setReleaser(ctx.releaser.address);
      await ctx.escrow.connect(ctx.releaser).release(k);
      expect((await ctx.escrow.getTrade(k)).status).to.equal(Status.Released);
    });

    it("a releaser named by one seller cannot touch another seller's trades", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.stranger).setReleaser(ctx.releaser.address);
      const k = await openPooled(ctx, "other");
      await expect(ctx.escrow.connect(ctx.releaser).release(k)).to.be.revertedWithCustomError(ctx.escrow, "NotReleaser");
    });
  });

  describe("auto-release is the seller's own choice", () => {
    it("arms only when the seller opted in, and anyone can finalize after the window", async () => {
      const ctx = await setup();
      const k1 = await openPooled(ctx, "a1");
      await ctx.escrow.connect(ctx.buyer).markPaid(k1);
      await expect(ctx.escrow.finalizeRelease(k1)).to.be.revertedWithCustomError(ctx.escrow, "AutoReleaseNotArmed");

      await ctx.escrow.connect(ctx.seller).setAutoReleaseWindow(DAY);
      const { v, sig } = await voucher(ctx);
      await ctx.escrow.connect(ctx.buyer).openTrade(id("a2"), v, sig, usd(50));
      const k2 = keyOf(ctx.buyer, "a2");
      await ctx.escrow.connect(ctx.buyer).markPaid(k2);
      await expect(ctx.escrow.finalizeRelease(k2)).to.be.revertedWithCustomError(ctx.escrow, "TimeoutNotElapsed");
      await time.increase(DAY);
      await expect(ctx.escrow.connect(ctx.stranger).finalizeRelease(k2)).to.emit(ctx.escrow, "TradeAutoReleased");
      await expect(ctx.escrow.connect(ctx.seller).setAutoReleaseWindow(8 * DAY)).to.be.revertedWithCustomError(ctx.escrow, "WindowOutOfBounds");
    });
  });

  describe("disputes", () => {
    it("can only be raised on a paid trade, by a party", async () => {
      const ctx = await setup();
      const k = await openPooled(ctx, "d");
      await expect(ctx.escrow.connect(ctx.buyer).raiseDispute(k)).to.be.revertedWithCustomError(ctx.escrow, "InvalidStatus");
      await ctx.escrow.connect(ctx.buyer).markPaid(k);
      await expect(ctx.escrow.connect(ctx.stranger).raiseDispute(k)).to.be.revertedWithCustomError(ctx.escrow, "NotAParty");
      await expect(ctx.escrow.connect(ctx.seller).raiseDispute(k)).to.emit(ctx.escrow, "TradeDisputed").withArgs(k, ctx.seller.address);
    });

    it("the resolver releases to the buyer or returns to the seller's pool; role-gated", async () => {
      const ctx = await setup();
      const k = await openPooled(ctx, "res");
      await ctx.escrow.connect(ctx.buyer).markPaid(k);
      await ctx.escrow.connect(ctx.buyer).raiseDispute(k);
      await expect(ctx.escrow.connect(ctx.stranger).adminResolve(k, true))
        .to.be.revertedWithCustomError(ctx.escrow, "AccessControlUnauthorizedAccount");
      await expect(ctx.escrow.connect(ctx.resolver).adminResolve(k, true))
        .to.emit(ctx.escrow, "TradeResolved").withArgs(k, true, usd(98.5), usd(1.5));
    });

    it("an unresolved dispute returns the crypto to the seller after the 14-day backstop", async () => {
      const ctx = await setup();
      const k = await openPooled(ctx, "bs");
      await ctx.escrow.connect(ctx.buyer).markPaid(k);
      await ctx.escrow.connect(ctx.buyer).raiseDispute(k);
      await expect(ctx.escrow.claimDisputeBackstop(k)).to.be.revertedWithCustomError(ctx.escrow, "TimeoutNotElapsed");
      await time.increase(14 * DAY + 1);
      await ctx.escrow.connect(ctx.stranger).claimDisputeBackstop(k);
      const [available] = await ctx.escrow.balanceOf(ctx.seller.address, ctx.T);
      expect(available).to.equal(usd(1000));
    });
  });

  describe("pause blocks new exposure only", () => {
    it("blocks deposit and opening; mark paid, release, dispute, resolve, cancel and withdraw all still work", async () => {
      const ctx = await setup();
      const k1 = await openPooled(ctx, "p1");
      const { v, sig } = await voucher(ctx);
      await ctx.escrow.connect(ctx.buyer).openTrade(id("p2"), v, sig, usd(50));
      const k2 = keyOf(ctx.buyer, "p2");
      const third = await voucher(ctx);
      await ctx.escrow.connect(ctx.buyer).openTrade(id("p3"), third.v, third.sig, usd(20));
      await ctx.escrow.connect(ctx.pauser).pause();

      await expect(ctx.escrow.connect(ctx.seller).deposit(ctx.T, usd(1))).to.be.revertedWithCustomError(ctx.escrow, "EnforcedPause");
      const again = await voucher(ctx);
      await expect(ctx.escrow.connect(ctx.buyer).openTrade(id("p4"), again.v, again.sig, usd(10))).to.be.revertedWithCustomError(ctx.escrow, "EnforcedPause");

      await ctx.escrow.connect(ctx.buyer).markPaid(k1); // a buyer who already paid fiat is never stuck
      await ctx.escrow.connect(ctx.seller).release(k1);
      await ctx.escrow.connect(ctx.buyer).markPaid(k2);
      await ctx.escrow.connect(ctx.buyer).raiseDispute(k2);
      await ctx.escrow.connect(ctx.resolver).adminResolve(k2, false);
      await time.increase(30 * MIN);
      await ctx.escrow.cancelByTimeout(keyOf(ctx.buyer, "p3"));
      const [available] = await ctx.escrow.balanceOf(ctx.seller.address, ctx.T);
      await ctx.escrow.connect(ctx.seller).withdraw(ctx.T, available);
      expect(await ctx.escrow.owed(ctx.T)).to.equal(0);
    });
  });

  describe("hostile recipients and rescue", () => {
    it("a blacklisted buyer cannot block the seller's release: the payout is deferred", async () => {
      const ctx = await setup({ tokenKind: "BlacklistERC20" });
      const k = await openPooled(ctx, "bl");
      await ctx.escrow.connect(ctx.buyer).markPaid(k);
      await ctx.token.setBlacklisted(ctx.buyer.address, true);
      await expect(ctx.escrow.connect(ctx.seller).release(k)).to.emit(ctx.escrow, "PayoutDeferred").withArgs(ctx.T, ctx.buyer.address, usd(98.5));
      await ctx.escrow.connect(ctx.buyer).claim(ctx.T, ctx.stranger.address);
      expect(await bal(ctx, ctx.stranger)).to.equal(usd(98.5));
    });

    it("rescue moves only what is above owed, to the fee wallet", async () => {
      const ctx = await setup();
      await openPooled(ctx, "res");
      await expect(ctx.escrow.rescue(ctx.T)).to.be.revertedWithCustomError(ctx.escrow, "NothingToRescue");
      await ctx.token.mint(await ctx.escrow.getAddress(), usd(7));
      await ctx.escrow.rescue(ctx.T);
      expect(await bal(ctx, ctx.feeWallet)).to.equal(usd(7));
      expect(await bal(ctx, await ctx.escrow.getAddress())).to.equal(usd(1000));
    });
  });

  describe("upgrades and forwarder", () => {
    it("no forwarder, Permit2, fee manager or system wallet setters exist", async () => {
      const ctx = await setup();
      const names = ctx.escrow.interface.fragments.map((f) => f.name);
      for (const n of ["setTrustedForwarder", "setPermit2", "setFeeManager", "setSystemWallet", "sweepExcess", "initializeV3"]) {
        expect(names.includes(n), n).to.equal(false);
      }
      expect(await ctx.escrow.trustedForwarder()).to.equal(await ctx.forwarder.getAddress());
    });

    it("cannot be initialized twice, and passes OpenZeppelin upgrade validation", async () => {
      const ctx = await setup();
      await expect(ctx.escrow.initialize(ctx.stranger.address, [], [])).to.be.revertedWithCustomError(ctx.escrow, "InvalidInitialization");
      await upgrades.validateUpgrade(await ctx.escrow.getAddress(), await ethers.getContractFactory("contracts/core/P2PEscrow.sol:P2PEscrow"), {
        kind: "uups",
        constructorArgs: [await ctx.forwarder.getAddress(), await ctx.fc.getAddress()],
      });
    });
  });
});
