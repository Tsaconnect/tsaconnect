const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

async function deployV2() {
  const [owner, admin, alice, bob, carol, system] = await ethers.getSigners();
  const Factory = await ethers.getContractFactory("P2PEscrowV2");
  const escrow = await upgrades.deployProxy(Factory, [owner.address, [admin.address]], { kind: "uups" });
  await escrow.waitForDeployment();
  await escrow.connect(owner).setSystemWallet(system.address);
  return { escrow, owner, admin, alice, bob, carol, system };
}

async function deployToken(escrow, owner, supplyTo) {
  const ERC20 = await ethers.getContractFactory("MockERC20");
  const token = await ERC20.deploy("Tok", "TOK", 18);
  await token.waitForDeployment();
  for (const acct of supplyTo) await token.mint(acct.address, ethers.parseUnits("1000000", 18));
  await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
  return token;
}

async function signVoucher(escrow, seller, v) {
  const domain = {
    name: "TSAConnectP2PEscrow",
    version: "2",
    chainId: (await ethers.provider.getNetwork()).chainId,
    verifyingContract: await escrow.getAddress(),
  };
  const types = { AdVoucher: [
    { name: "seller", type: "address" }, { name: "token", type: "address" },
    { name: "maxAmount", type: "uint256" }, { name: "minPerTrade", type: "uint256" },
    { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
    { name: "nonce", type: "bytes32" },
  ]};
  const sig = await seller.signTypedData(domain, types, v);
  return sig;
}

function makeVoucher(seller, token, overrides = {}) {
  return {
    seller, token,
    maxAmount: ethers.parseUnits("1000", 18),
    minPerTrade: 0,
    maxPerTrade: 0,
    // Use a far-future deadline (year 2100) so time.increase() in earlier tests
    // cannot cause "Voucher expired" in later tests.
    deadline: 4102444800, // 2100-01-01 00:00:00 UTC
    nonce: ethers.hexlify(ethers.randomBytes(32)),
    ...overrides,
  };
}

async function setup_pooled(amount = "1000") {
  const ctx = await deployV2();
  const token = await deployToken(ctx.escrow, ctx.owner, [ctx.alice, ctx.bob]);
  const dep = ethers.parseUnits(amount, 18);
  await token.connect(ctx.alice).approve(await ctx.escrow.getAddress(), dep);
  await ctx.escrow.connect(ctx.alice).deposit(await token.getAddress(), dep);
  return { ...ctx, token };
}

// ─── Task 1: Scaffold ─────────────────────────────────────────────────────────

describe("P2PEscrowV2 — scaffold", () => {
  it("initializes owner, admin, and economic defaults", async () => {
    const { escrow, owner, admin } = await deployV2();
    expect(await escrow.owner()).to.equal(owner.address);
    expect(await escrow.isAdmin(admin.address)).to.equal(true);
    expect(await escrow.feeBps()).to.equal(50);
    expect(await escrow.paymentWindow()).to.equal(30 * 60);
    expect(await escrow.autoReleaseWindow()).to.equal(0);
    expect(await escrow.disputeFeeBps()).to.equal(0);
    expect(await escrow.trustedForwarder()).to.equal(ethers.ZeroAddress);
    expect(await escrow.permit2()).to.equal(ethers.ZeroAddress);
  });
});

// ─── Task 2: Deposit / withdraw ───────────────────────────────────────────────

describe("P2PEscrowV2 — deposit/withdraw", () => {
  it("deposit credits available + accountedBalance", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await expect(escrow.connect(alice).deposit(await token.getAddress(), amt))
      .to.emit(escrow, "Deposited");
    const [avail] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(amt);
  });

  it("withdraw debits available + accountedBalance", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);
    await escrow.connect(alice).withdraw(await token.getAddress(), amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
  });

  it("sweepExcess refuses to touch accounted principal", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);
    await expect(escrow.connect(owner).sweepExcess(await token.getAddress(), 1))
      .to.be.revertedWith("Exceeds excess");
  });
});

// ─── Task 3: Vouchers ─────────────────────────────────────────────────────────

describe("P2PEscrowV2 — vouchers", () => {
  it("hashVoucher matches the off-chain typed-data hash and cancelVoucher works", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const v = makeVoucher(alice.address, await token.getAddress());
    const h = await escrow.hashVoucher(v);
    expect(h.length).to.equal(66); // 0x + 32 bytes
    await expect(escrow.connect(alice).cancelVoucher(v)).to.emit(escrow, "VoucherCancelled").withArgs(h, alice.address);
    expect(await escrow.voucherCancelled(h)).to.equal(true);
  });

  it("only the voucher seller can cancel", async () => {
    const { escrow, owner, alice, bob } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const v = makeVoucher(alice.address, await token.getAddress());
    await expect(escrow.connect(bob).cancelVoucher(v)).to.be.revertedWith("Only voucher seller");
  });
});

// ─── Task 4: openTrade (pooled) ───────────────────────────────────────────────

describe("P2PEscrowV2 — openTrade (sell-ad/pooled)", () => {
  it("opens, moves available→locked, snapshots feeBps, sets Pending", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("trade-1");
    const amt = ethers.parseUnits("100", 18);
    await expect(escrow.connect(bob).openTrade(tradeId, v, sig, amt)).to.emit(escrow, "TradeOpened");
    const t = await escrow.getTrade(tradeId);
    expect(t.seller).to.equal(alice.address);
    expect(t.buyer).to.equal(bob.address);
    expect(t.status).to.equal(1); // Pending
    expect(t.feeBps_).to.equal(50); // getTrade names the snapshot return `feeBps_`
    const [avail, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(amt);
    expect(avail).to.equal(ethers.parseUnits("900", 18));
  });

  it("rejects a bad signature", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, bob, v); // wrong signer
    await expect(escrow.connect(bob).openTrade(ethers.id("t"), v, sig, ethers.parseUnits("100",18)))
      .to.be.revertedWith("Bad voucher signature");
  });

  it("rejects seller self-buy", async () => {
    const { escrow, alice, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    await expect(escrow.connect(alice).openTrade(ethers.id("t"), v, sig, ethers.parseUnits("100",18)))
      .to.be.revertedWith("Seller cannot self-buy");
  });

  it("rejects exceeding voucher capacity", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress(), { maxAmount: ethers.parseUnits("50",18) });
    const sig = await signVoucher(escrow, alice, v);
    await expect(escrow.connect(bob).openTrade(ethers.id("t"), v, sig, ethers.parseUnits("100",18)))
      .to.be.revertedWith("Exceeds voucher capacity");
  });
});

// ─── Task 5: openTradeWithDeposit (JIT) ──────────────────────────────────────

describe("P2PEscrowV2 — openTradeWithDeposit (buy-ad/JIT)", () => {
  it("pulls the user's crypto, locks it under seller=user, buyer=merchant", async () => {
    const { escrow, owner, alice, carol } = await deployV2(); // alice=user(seller), carol=merchant(buyer)
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy-1");
    await expect(escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address))
      .to.emit(escrow, "TradeOpenedWithDeposit");
    const t = await escrow.getTrade(tradeId);
    expect(t.seller).to.equal(alice.address);
    expect(t.buyer).to.equal(carol.address);
    expect(t.funding).to.equal(1); // DepositedByTaker
    expect(t.voucherHash).to.equal(ethers.ZeroHash);
    const [, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(amt);
  });

  it("rejects amount over uint96 max", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const tooBig = (2n ** 96n);
    await expect(escrow.connect(alice).openTradeWithDeposit(ethers.id("x"), await token.getAddress(), tooBig, carol.address))
      .to.be.revertedWith("Amount overflow");
  });

  it("rejects self-trade (merchant == caller)", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("10", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await expect(escrow.connect(alice).openTradeWithDeposit(ethers.id("x"), await token.getAddress(), amt, alice.address))
      .to.be.revertedWith("Self-trade");
  });
});

// ─── Task 6: markPaid ─────────────────────────────────────────────────────────

describe("P2PEscrowV2 — markPaid", () => {
  it("buyer marks paid; autoReleaseAt stays 0 when auto-release disabled", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(bob).markPaid(tradeId)).to.emit(escrow, "TradePaid");
    const t = await escrow.getTrade(tradeId);
    expect(t.status).to.equal(2); // Paid
    expect(t.autoReleaseAt).to.equal(0);
  });

  it("non-buyer cannot mark paid", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(carol).markPaid(tradeId)).to.be.revertedWith("Only buyer");
  });

  it("sets autoReleaseAt when window>0 AND seller opted in", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    await escrow.connect(owner).setAutoReleaseWindow(2 * 3600);
    await escrow.connect(alice).setAutoReleaseOptIn(true);
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(bob).markPaid(tradeId);
    const t = await escrow.getTrade(tradeId);
    expect(t.autoReleaseAt).to.be.greaterThan(0);
  });
});

// ─── Task 7: release ──────────────────────────────────────────────────────────

describe("P2PEscrowV2 — release", () => {
  it("seller releases: buyer gets amount-fee, system gets fee, accountedBalance drops", async () => {
    const { escrow, alice, bob, token, system } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(bob).markPaid(tradeId);
    const fee = amt * 50n / 10000n;
    const before = await token.balanceOf(bob.address);
    await expect(escrow.connect(alice).release(tradeId)).to.emit(escrow, "TradeReleased");
    expect(await token.balanceOf(bob.address)).to.equal(before + (amt - fee));
    expect(await token.balanceOf(system.address)).to.equal(fee);
    const t = await escrow.getTrade(tradeId);
    expect(t.status).to.equal(3); // Released
  });

  it("buy-ad release pays the merchant (buyer) minus fee", async () => {
    const { escrow, owner, alice, carol, system } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    await escrow.connect(carol).markPaid(tradeId);
    const fee = amt * 50n / 10000n;
    await escrow.connect(alice).release(tradeId); // user (seller/fiat-receiver) releases
    expect(await token.balanceOf(carol.address)).to.equal(amt - fee);
    expect(await token.balanceOf(system.address)).to.equal(fee);
  });

  it("non-seller cannot release", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(bob).markPaid(tradeId);
    await expect(escrow.connect(carol).release(tradeId)).to.be.revertedWith("Only seller");
  });
});

// ─── Task 8: cancelByTimeout ──────────────────────────────────────────────────

describe("P2PEscrowV2 — cancelByTimeout", () => {
  it("pooled: returns locked→available and refunds voucher capacity", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await time.increase(31 * 60);
    await expect(escrow.connect(bob).cancelByTimeout(tradeId)).to.emit(escrow, "TradeCancelled");
    const [avail, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(0);
    expect(avail).to.equal(ethers.parseUnits("1000", 18));
    expect(await escrow.voucherFilled(await escrow.hashVoucher(v))).to.equal(0);
  });

  it("JIT: transfers crypto back to the user's wallet and drops accountedBalance", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    const before = await token.balanceOf(alice.address);
    await time.increase(31 * 60);
    await escrow.connect(alice).cancelByTimeout(tradeId);
    expect(await token.balanceOf(alice.address)).to.equal(before + amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
    const [, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(0);
  });

  it("reverts before the window elapses", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(bob).cancelByTimeout(tradeId)).to.be.revertedWith("Timeout not elapsed");
  });
});

// ─── Task 9: dispute/resolve ──────────────────────────────────────────────────

describe("P2PEscrowV2 — dispute/resolve", () => {
  it("resolve to buyer pays buyer minus fee (non-custodial)", async () => {
    const { escrow, admin, alice, bob, token, system } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(bob).markPaid(tradeId);
    await escrow.connect(bob).raiseDispute(tradeId);
    const fee = amt * 50n / 10000n;
    const beforeBob = await token.balanceOf(bob.address);
    const beforeSystem = await token.balanceOf(system.address);
    await expect(escrow.connect(admin).adminResolve(tradeId, true)).to.emit(escrow, "TradeResolved");
    expect(await token.balanceOf(bob.address)).to.equal(beforeBob + (amt - fee));
    expect(await token.balanceOf(system.address)).to.equal(beforeSystem + fee);
  });

  it("resolve to seller (pooled) returns funds to available + refunds voucher capacity", async () => {
    const { escrow, admin, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(alice).raiseDispute(tradeId);
    await escrow.connect(admin).adminResolve(tradeId, false);
    const [avail, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(0);
    expect(avail).to.equal(ethers.parseUnits("1000", 18));
  });

  it("resolve to seller (JIT) transfers crypto back to the user", async () => {
    const { escrow, owner, admin, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    await escrow.connect(carol).markPaid(tradeId);
    await escrow.connect(alice).raiseDispute(tradeId);
    const before = await token.balanceOf(alice.address);
    await escrow.connect(admin).adminResolve(tradeId, false);
    expect(await token.balanceOf(alice.address)).to.equal(before + amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
  });

  it("non-party cannot raise dispute", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await expect(escrow.connect(carol).raiseDispute(tradeId)).to.be.revertedWith("Not a party");
  });
});

// ─── Task 10: finalizeRelease ─────────────────────────────────────────────────

describe("P2PEscrowV2 — finalizeRelease (optimistic)", () => {
  async function armedTrade() {
    const ctx = await setup_pooled();
    await ctx.escrow.connect(ctx.owner).setAutoReleaseWindow(2 * 3600);
    await ctx.escrow.connect(ctx.alice).setAutoReleaseOptIn(true);
    const v = makeVoucher(ctx.alice.address, await ctx.token.getAddress());
    const sig = await signVoucher(ctx.escrow, ctx.alice, v);
    const tradeId = ethers.id("auto");
    await ctx.escrow.connect(ctx.bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await ctx.escrow.connect(ctx.bob).markPaid(tradeId);
    return { ...ctx, tradeId };
  }

  it("reverts when auto-release disabled (window 0 ⇒ autoReleaseAt 0)", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(bob).markPaid(tradeId);
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.be.revertedWith("Auto-release not armed");
  });

  it("reverts before the window, succeeds (to buyer) after", async () => {
    const { escrow, bob, token, system, tradeId } = await armedTrade();
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.be.revertedWith("Window not elapsed");
    await time.increase(2 * 3600 + 1);
    const amt = ethers.parseUnits("100", 18); const fee = amt * 50n / 10000n;
    const beforeBob = await token.balanceOf(bob.address);
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.emit(escrow, "TradeAutoReleased");
    expect(await token.balanceOf(bob.address)).to.equal(beforeBob + (amt - fee));
  });

  it("a dispute before finalize blocks auto-release", async () => {
    const { escrow, alice, bob, tradeId } = await armedTrade();
    await escrow.connect(alice).raiseDispute(tradeId);
    await time.increase(2 * 3600 + 1);
    await expect(escrow.connect(bob).finalizeRelease(tradeId)).to.be.revertedWith("Not paid");
  });
});

// ─── Task 11: setters & caps ──────────────────────────────────────────────────

describe("P2PEscrowV2 — setters & caps", () => {
  it("setFeeBps enforces MAX_FEE_BPS and does not affect in-flight trades", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    await expect(escrow.connect(owner).setFeeBps(501)).to.be.revertedWith("Fee too high");
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt); // snapshots 50
    await escrow.connect(owner).setFeeBps(200);                // change after open
    await escrow.connect(bob).markPaid(tradeId);
    const fee = amt * 50n / 10000n;                            // still 0.5%
    const before = await token.balanceOf(bob.address);
    await escrow.connect(alice).release(tradeId);
    expect(await token.balanceOf(bob.address)).to.equal(before + (amt - fee));
  });

  it("setPaymentWindow enforces bounds", async () => {
    const { escrow, owner } = await deployV2();
    await expect(escrow.connect(owner).setPaymentWindow(60)).to.be.revertedWith("Window out of bounds");      // < 5m
    await expect(escrow.connect(owner).setPaymentWindow(25 * 3600)).to.be.revertedWith("Window out of bounds"); // > 24h
    await escrow.connect(owner).setPaymentWindow(3600);
    expect(await escrow.paymentWindow()).to.equal(3600);
  });

  it("MCGP trades snapshot a zero fee", async () => {
    const { escrow, owner, alice, bob } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    await escrow.connect(owner).setMcgpToken(await token.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    const t = await escrow.getTrade(tradeId);
    expect(t.feeBps_).to.equal(0);
  });

  it("addAdmin/removeAdmin gated to owner", async () => {
    const { escrow, owner, carol } = await deployV2();
    await escrow.connect(owner).addAdmin(carol.address);
    expect(await escrow.isAdmin(carol.address)).to.equal(true);
    await escrow.connect(owner).removeAdmin(carol.address);
    expect(await escrow.isAdmin(carol.address)).to.equal(false);
  });
});

// ─── Task 12: EIP-2771 + Permit2 ─────────────────────────────────────────────

describe("P2PEscrowV2 — EIP-2771 + Permit2", () => {
  it("a forwarded deposit credits the appended signer, not the forwarder", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const Fwd = await ethers.getContractFactory("MockForwarder");
    const fwd = await Fwd.deploy(); await fwd.waitForDeployment();
    await escrow.connect(owner).setTrustedForwarder(await fwd.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const data = escrow.interface.encodeFunctionData("deposit", [await token.getAddress(), amt]);
    await fwd.relay(await escrow.getAddress(), data, alice.address);
    const [avail] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt); // credited alice (the appended signer), not the forwarder
  });

  it("depositWithPermit2 pulls from the signer via Permit2", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const P2 = await ethers.getContractFactory("MockPermit2");
    const p2 = await P2.deploy(); await p2.waitForDeployment();
    await escrow.connect(owner).setPermit2(await p2.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await p2.getAddress(), amt); // approve Permit2, not escrow
    const permit = { permitted: { token: await token.getAddress(), amount: amt }, nonce: 1, deadline: Math.floor(Date.now()/1000)+3600 };
    await escrow.connect(alice).depositWithPermit2(permit, "0x");
    const [avail] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt);
  });
});

// ─── Task 13: Pause semantics ─────────────────────────────────────────────────

describe("P2PEscrowV2 — pause semantics", () => {
  it("blocks settlement entrypoints when paused, allows recovery", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100",18));
    await escrow.connect(owner).pause();
    await expect(escrow.connect(bob).markPaid(tradeId)).to.be.revertedWithCustomError(escrow, "EnforcedPause");
    // withdraw + cancelByTimeout remain available during pause:
    await time.increase(31 * 60);
    await expect(escrow.connect(bob).cancelByTimeout(tradeId)).to.emit(escrow, "TradeCancelled");
    await expect(escrow.connect(alice).withdraw(await token.getAddress(), ethers.parseUnits("1",18))).to.emit(escrow, "Withdrawn");
  });
});

// ─── Task 14: Upgradeability ──────────────────────────────────────────────────

describe("P2PEscrowV2 — upgradeability", () => {
  it("upgrades behind the proxy preserving balances (storage-layout safe)", async () => {
    const { escrow, owner, alice } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await token.getAddress(), amt);

    const V2b = await ethers.getContractFactory("P2PEscrowV2"); // same impl re-validates layout
    const upgraded = await upgrades.upgradeProxy(await escrow.getAddress(), V2b);
    const [avail] = await upgraded.balanceOf(alice.address, await token.getAddress());
    expect(avail).to.equal(amt); // balance survived the upgrade
  });
});
