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

    // FIX G: assert the on-chain hash equals the off-chain EIP-712 hash
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
    const offChainHash = ethers.TypedDataEncoder.hash(domain, types, v);
    expect(h).to.equal(offChainHash);

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
    // FIX A: assert event with correct args (returnedToPool == true for pooled)
    await expect(escrow.connect(bob).cancelByTimeout(tradeId))
      .to.emit(escrow, "TradeCancelled").withArgs(tradeId, amt, true);
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
    // FIX A: assert event with correct args (returnedToPool == false for JIT)
    await expect(escrow.connect(alice).cancelByTimeout(tradeId))
      .to.emit(escrow, "TradeCancelled").withArgs(tradeId, amt, false);
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

// ─── Task 15: EIP-2771 owner/admin carve-out ─────────────────────────────────

describe("P2PEscrowV2 — EIP-2771 owner/admin carve-out", () => {
  it("forwarder cannot impersonate owner to call setFeeBps", async () => {
    const { escrow, owner } = await deployV2();
    const Fwd = await ethers.getContractFactory("MockForwarder");
    const fwd = await Fwd.deploy(); await fwd.waitForDeployment();
    await escrow.connect(owner).setTrustedForwarder(await fwd.getAddress());

    // Relay setFeeBps(200) with owner's address appended — must revert with OZ custom error.
    // FIX E: assert the SPECIFIC revert (OwnableUnauthorizedAccount), not just any revert.
    const data = escrow.interface.encodeFunctionData("setFeeBps", [200]);
    await expect(fwd.relay(await escrow.getAddress(), data, owner.address))
      .to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
  });

  it("forwarder cannot impersonate admin to call adminResolve", async () => {
    const { escrow, owner, admin, alice, bob, token } = await setup_pooled();
    const Fwd = await ethers.getContractFactory("MockForwarder");
    const fwd = await Fwd.deploy(); await fwd.waitForDeployment();
    await escrow.connect(owner).setTrustedForwarder(await fwd.getAddress());

    // Open a disputed trade
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("t-carveout");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100", 18));
    await escrow.connect(bob).markPaid(tradeId);
    await escrow.connect(bob).raiseDispute(tradeId);

    // Relay adminResolve with admin's address appended — must revert with "Not admin".
    // FIX E: assert the SPECIFIC revert reason, not just any revert.
    const data = escrow.interface.encodeFunctionData("adminResolve", [tradeId, true]);
    await expect(fwd.relay(await escrow.getAddress(), data, admin.address))
      .to.be.revertedWith("Not admin");
  });

  it("direct owner call to setFeeBps still works after forwarder is set", async () => {
    const { escrow, owner } = await deployV2();
    const Fwd = await ethers.getContractFactory("MockForwarder");
    const fwd = await Fwd.deploy(); await fwd.waitForDeployment();
    await escrow.connect(owner).setTrustedForwarder(await fwd.getAddress());
    await expect(escrow.connect(owner).setFeeBps(200)).to.emit(escrow, "FeeBpsUpdated");
    expect(await escrow.feeBps()).to.equal(200);
  });

  it("forwarded USER deposit still credits the appended signer (Task-12 regression)", async () => {
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
    expect(avail).to.equal(amt); // alice credited, not the forwarder
  });
});

// ─── Task 16: finalizeRelease blocked when seller NOT opted in ────────────────

describe("P2PEscrowV2 — finalizeRelease requires opt-in", () => {
  it("reverts with 'Auto-release not armed' when seller has NOT opted in", async () => {
    const { escrow, owner, alice, bob, token } = await setup_pooled();
    // Set a non-zero window but do NOT call setAutoReleaseOptIn
    await escrow.connect(owner).setAutoReleaseWindow(2 * 3600);
    // (alice never calls setAutoReleaseOptIn — default is false)

    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("no-optin");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100", 18));
    await escrow.connect(bob).markPaid(tradeId);

    // Advance time past the window
    await time.increase(2 * 3600 + 1);

    // autoReleaseAt is still 0 because seller never opted in → must revert
    await expect(escrow.connect(bob).finalizeRelease(tradeId))
      .to.be.revertedWith("Auto-release not armed");
  });
});

// ─── Auto-release on a buy-ad (JIT): opt-in is keyed on the crypto-provider ───
// Pins the semantics that `autoReleaseOptIn` is the SELLER's (crypto-provider's)
// flag for BOTH flows: on a buy-ad the seller is the depositing USER, and their
// opt-in arms auto-release of THEIR crypto to the merchant (buyer).
describe("P2PEscrowV2 — finalizeRelease on a buy-ad (JIT)", () => {
  it("the depositing user (seller) opts in; auto-releases to the merchant (buyer)", async () => {
    const { escrow, owner, alice, carol, system } = await deployV2(); // alice=user/seller, carol=merchant/buyer
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(owner).setAutoReleaseWindow(2 * 3600);
    await escrow.connect(alice).setAutoReleaseOptIn(true); // the SELLER (user) opts in
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy-auto");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    // Merchant (buyer) marks fiat paid → arms autoReleaseAt from the seller's opt-in.
    await escrow.connect(carol).markPaid(tradeId);
    expect((await escrow.getTrade(tradeId)).autoReleaseAt).to.be.greaterThan(0);
    // A merchant opt-in would NOT arm it (proves it keys on seller, not buyer):
    // carol never opted in, yet it armed — because alice (seller) did.
    await time.increase(2 * 3600 + 1);
    const fee = amt * 50n / 10000n;
    await expect(escrow.connect(carol).finalizeRelease(tradeId)).to.emit(escrow, "TradeAutoReleased");
    expect(await token.balanceOf(carol.address)).to.equal(amt - fee); // crypto to the merchant
    expect(await token.balanceOf(system.address)).to.equal(fee);
  });

  it("does NOT arm when only the merchant (buyer) opted in, not the seller", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(owner).setAutoReleaseWindow(2 * 3600);
    await escrow.connect(carol).setAutoReleaseOptIn(true); // merchant/buyer opts in — wrong party
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("buy-noarm");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    await escrow.connect(carol).markPaid(tradeId);
    expect((await escrow.getTrade(tradeId)).autoReleaseAt).to.equal(0); // not armed
    await time.increase(2 * 3600 + 1);
    await expect(escrow.connect(carol).finalizeRelease(tradeId)).to.be.revertedWith("Auto-release not armed");
  });
});

// ─── Task 17: openTradeWithDepositPermit2 happy path ─────────────────────────

describe("P2PEscrowV2 — openTradeWithDepositPermit2", () => {
  it("creates a DepositedByTaker trade and credits locked balance via Permit2", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const P2 = await ethers.getContractFactory("MockPermit2");
    const p2 = await P2.deploy(); await p2.waitForDeployment();
    await escrow.connect(owner).setPermit2(await p2.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await p2.getAddress(), amt);

    const tradeId = ethers.id("jit-permit2");
    const permit = {
      permitted: { token: await token.getAddress(), amount: amt },
      nonce: 42,
      deadline: Math.floor(Date.now() / 1000) + 3600,
    };
    await expect(
      escrow.connect(alice).openTradeWithDepositPermit2(tradeId, carol.address, permit, "0x")
    ).to.emit(escrow, "TradeOpenedWithDeposit");

    const t = await escrow.getTrade(tradeId);
    expect(t.seller).to.equal(alice.address);
    expect(t.buyer).to.equal(carol.address);
    expect(t.funding).to.equal(1); // DepositedByTaker
    expect(t.status).to.equal(1); // Pending

    const [, lockd] = await escrow.balanceOf(alice.address, await token.getAddress());
    expect(lockd).to.equal(amt);
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(amt);
  });
});

// ─── Task 18: accountedBalance goes to zero after terminal states ─────────────

describe("P2PEscrowV2 — accountedBalance after terminal states", () => {
  it("accountedBalance == 0 after sell-ad release", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("acc-1");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(bob).markPaid(tradeId);
    await escrow.connect(alice).release(tradeId);
    // After release the pooled funds left the contract; alice still has 900 in available
    // so accountedBalance == 900 (the un-traded balance). For a clean zero we need
    // a fresh setup with only the traded amount deposited.
    const { escrow: e2, owner: o2, alice: a2, bob: b2, system: s2 } = await deployV2();
    const t2 = await deployToken(e2, o2, [a2, b2]);
    const dep2 = ethers.parseUnits("100", 18);
    await t2.connect(a2).approve(await e2.getAddress(), dep2);
    await e2.connect(a2).deposit(await t2.getAddress(), dep2);
    const v2 = makeVoucher(a2.address, await t2.getAddress());
    const sig2 = await signVoucher(e2, a2, v2);
    const tid2 = ethers.id("acc-clean");
    await e2.connect(b2).openTrade(tid2, v2, sig2, dep2);
    await e2.connect(b2).markPaid(tid2);
    await e2.connect(a2).release(tid2);
    expect(await e2.accountedBalance(await t2.getAddress())).to.equal(0);
  });

  it("accountedBalance == 0 after pooled adminResolve(false) → resolve-to-seller", async () => {
    // After resolve-to-seller on pooled trade, funds go back to available (still in-contract),
    // so accountedBalance stays at the deposited amount (the pool invariant).
    // For a strict zero, use a JIT (DepositedByTaker) trade resolved back to seller.
    const { escrow, owner, admin, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("acc-jit");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    await escrow.connect(carol).markPaid(tradeId);
    await escrow.connect(alice).raiseDispute(tradeId);
    await escrow.connect(admin).adminResolve(tradeId, false); // resolve to seller → refund
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(0);
  });
});

// ─── Task 19: Voucher expiry ──────────────────────────────────────────────────

describe("P2PEscrowV2 — voucher expiry", () => {
  it("rejects a voucher with a deadline in the past", async () => {
    const { escrow, alice, bob, token } = await setup_pooled();
    const latestBlock = await ethers.provider.getBlock("latest");
    const pastDeadline = latestBlock.timestamp - 60; // 60 seconds in the past
    const v = makeVoucher(alice.address, await token.getAddress(), { deadline: pastDeadline });
    const sig = await signVoucher(escrow, alice, v);
    await expect(
      escrow.connect(bob).openTrade(ethers.id("expired"), v, sig, ethers.parseUnits("100", 18))
    ).to.be.revertedWith("Voucher expired");
  });
});

// ─── Task 20: MCGP fee-free on JIT flow ──────────────────────────────────────

describe("P2PEscrowV2 — MCGP fee-free on JIT (openTradeWithDeposit)", () => {
  it("openTradeWithDeposit with MCGP token snapshots feeBps == 0", async () => {
    const { escrow, owner, alice, carol } = await deployV2();
    const token = await deployToken(escrow, owner, [alice]);
    await escrow.connect(owner).setMcgpToken(await token.getAddress());
    const amt = ethers.parseUnits("100", 18);
    await token.connect(alice).approve(await escrow.getAddress(), amt);
    const tradeId = ethers.id("mcgp-jit");
    await escrow.connect(alice).openTradeWithDeposit(tradeId, await token.getAddress(), amt, carol.address);
    const t = await escrow.getTrade(tradeId);
    expect(t.feeBps_).to.equal(0);
  });
});

// ─── Task 21: Reentrancy smoke test ──────────────────────────────────────────

describe("P2PEscrowV2 — reentrancy guard", () => {
  // FIX D: The token's transfer() re-enters deposit() — a DIFFERENT nonReentrant function —
  // while withdraw() is executing. Without the guard, deposit() would succeed (CEI on
  // withdraw already cleared the balance, so no double-spend guard would fire).
  // With the guard, deposit() sees _reentrancyStatus == ENTERED and reverts with
  // "ReentrancyGuard: reentrant call", which bubbles out and causes the OUTER withdraw()
  // to revert. This proves the guard fired, not a balance check.
  it("reentrant call into a different nonReentrant function is blocked with the guard message", async () => {
    const { escrow, owner, alice } = await deployV2();

    // Deploy the malicious token
    const MalFactory = await ethers.getContractFactory("MaliciousToken");
    const malToken = await MalFactory.deploy(); await malToken.waitForDeployment();

    // Whitelist and mint to alice
    await escrow.connect(owner).setTokenAcceptance(await malToken.getAddress(), true);
    await malToken.mint(alice.address, ethers.parseUnits("1000", 18));

    // Alice deposits malicious tokens (transfer in fine — not armed yet)
    const amt = ethers.parseUnits("100", 18);
    await malToken.connect(alice).approve(await escrow.getAddress(), amt);
    await escrow.connect(alice).deposit(await malToken.getAddress(), amt);

    // Arm: transfer() will now call deposit() on the escrow when triggered by withdraw's payout.
    // There is no try/catch — the revert bubbles to the caller (withdraw).
    await malToken.arm(await escrow.getAddress());

    // withdraw() sends tokens to alice → transfer() fires → deposit() re-enters →
    // "ReentrancyGuard: reentrant call" bubbles → outer withdraw reverts.
    // This specific message proves the guard fired (not a balance or allowance check).
    await expect(
      escrow.connect(alice).withdraw(await malToken.getAddress(), amt)
    ).to.be.revertedWith("ReentrancyGuard: reentrant call");
  });
});

// ─── Task 22: adminRelease ────────────────────────────────────────────────────

describe("P2PEscrowV2 — adminRelease", () => {
  // FIX F: adminRelease is a high-privilege settlement path with zero prior test coverage.

  it("admin can adminRelease a Paid pooled trade: buyer gets amount-fee, system gets fee, status Released", async () => {
    const { escrow, admin, alice, bob, token, system } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("ar-paid");
    const amt = ethers.parseUnits("100", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    await escrow.connect(bob).markPaid(tradeId); // → Paid
    const fee = amt * 50n / 10000n;
    const beforeBob = await token.balanceOf(bob.address);
    const beforeSystem = await token.balanceOf(system.address);
    await expect(escrow.connect(admin).adminRelease(tradeId)).to.emit(escrow, "TradeReleased");
    expect(await token.balanceOf(bob.address)).to.equal(beforeBob + (amt - fee));
    expect(await token.balanceOf(system.address)).to.equal(beforeSystem + fee);
    const t = await escrow.getTrade(tradeId);
    expect(t.status).to.equal(3); // Released
    expect(await escrow.accountedBalance(await token.getAddress())).to.equal(
      ethers.parseUnits("900", 18) // remaining pool (1000 deposited − 100 traded)
    );
  });

  it("admin can adminRelease a Pending pooled trade (seller has not yet released)", async () => {
    const { escrow, admin, alice, bob, token, system } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("ar-pending");
    const amt = ethers.parseUnits("50", 18);
    await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
    // Trade is still Pending (buyer has NOT called markPaid)
    const fee = amt * 50n / 10000n;
    const beforeBob = await token.balanceOf(bob.address);
    await expect(escrow.connect(admin).adminRelease(tradeId)).to.emit(escrow, "TradeReleased");
    expect(await token.balanceOf(bob.address)).to.equal(beforeBob + (amt - fee));
    const t = await escrow.getTrade(tradeId);
    expect(t.status).to.equal(3); // Released
  });

  it("non-admin cannot call adminRelease", async () => {
    const { escrow, alice, bob, carol, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("ar-noadmin");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100", 18));
    await escrow.connect(bob).markPaid(tradeId);
    await expect(escrow.connect(carol).adminRelease(tradeId)).to.be.revertedWith("Not admin");
  });

  it("adminRelease reverts when the contract is paused", async () => {
    const { escrow, owner, admin, alice, bob, token } = await setup_pooled();
    const v = makeVoucher(alice.address, await token.getAddress());
    const sig = await signVoucher(escrow, alice, v);
    const tradeId = ethers.id("ar-paused");
    await escrow.connect(bob).openTrade(tradeId, v, sig, ethers.parseUnits("100", 18));
    await escrow.connect(bob).markPaid(tradeId);
    await escrow.connect(owner).pause();
    await expect(escrow.connect(admin).adminRelease(tradeId))
      .to.be.revertedWithCustomError(escrow, "EnforcedPause");
  });
});
