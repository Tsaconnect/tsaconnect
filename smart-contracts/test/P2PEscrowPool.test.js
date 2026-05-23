const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const {
  loadFixture,
  time,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("P2PEscrowPool", function () {
  const THIRTY_MIN = 30 * 60;
  const ONE_DAY = 24 * 60 * 60;

  // --- Fixture & helpers -----------------------------------------------------

  async function deployFixture() {
    const [owner, seller, buyer, buyer2, operator, system, other] =
      await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);
    const mcgp = await MockERC20.deploy("MCGP Token", "MCGP", 18);

    const Pool = await ethers.getContractFactory("P2PEscrowPool");
    const pool = await upgrades.deployProxy(
      Pool,
      [owner.address, [operator.address]],
      { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
    );
    await pool.waitForDeployment();

    await pool.setTokenAcceptance(await usdt.getAddress(), true);
    await pool.setTokenAcceptance(await mcgp.getAddress(), true);
    await pool.setMcgpToken(await mcgp.getAddress());
    await pool.setSystemWallet(system.address);

    await usdt.mint(seller.address, 100_000_000000n);
    await mcgp.mint(seller.address, ethers.parseEther("100000"));

    return { pool, usdt, mcgp, owner, seller, buyer, buyer2, operator, system, other };
  }

  async function depositUsdt(pool, usdt, seller, amount) {
    await usdt.connect(seller).approve(await pool.getAddress(), amount);
    await pool.connect(seller).deposit(await usdt.getAddress(), amount);
  }

  // Build + sign an AdVoucher. Returns { voucher, sig, voucherHash } so tests
  // can mutate the voucher object before signing.
  async function signVoucher(pool, seller, overrides = {}) {
    const v = {
      seller: seller.address,
      token: overrides.token,
      maxAmount: overrides.maxAmount ?? 10_000_000000n,
      minPerTrade: overrides.minPerTrade ?? 0n,
      maxPerTrade: overrides.maxPerTrade ?? 0n,
      deadline: overrides.deadline ?? (await time.latest()) + ONE_DAY,
      nonce: overrides.nonce ?? ethers.id(`ad-${Math.random()}`),
    };

    const domain = {
      name: "TSAConnectP2PEscrowPool",
      version: "1",
      chainId: (await ethers.provider.getNetwork()).chainId,
      verifyingContract: await pool.getAddress(),
    };
    const types = {
      AdVoucher: [
        { name: "seller", type: "address" },
        { name: "token", type: "address" },
        { name: "maxAmount", type: "uint256" },
        { name: "minPerTrade", type: "uint256" },
        { name: "maxPerTrade", type: "uint256" },
        { name: "deadline", type: "uint256" },
        { name: "nonce", type: "bytes32" },
      ],
    };
    const sig = await seller.signTypedData(domain, types, v);
    const voucherHash = await pool.hashVoucher(v);
    return { voucher: v, sig, voucherHash };
  }

  // --- Initialization & config ----------------------------------------------

  describe("Initialization", function () {
    it("seeds owner and admins", async function () {
      const { pool, owner, operator } = await loadFixture(deployFixture);
      expect(await pool.owner()).to.equal(owner.address);
      expect(await pool.isAdmin(owner.address)).to.equal(true);
      expect(await pool.isAdmin(operator.address)).to.equal(true);
    });

    it("rejects re-initialization", async function () {
      const { pool, owner } = await loadFixture(deployFixture);
      await expect(pool.initialize(owner.address, [])).to.be.reverted;
    });
  });

  // --- Deposit / withdraw ----------------------------------------------------

  describe("Deposit", function () {
    it("credits available balance and pulls tokens", async function () {
      const { pool, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);

      const [avail, locked] = await pool.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(5000_000000n);
      expect(locked).to.equal(0n);
      expect(await usdt.balanceOf(await pool.getAddress())).to.equal(5000_000000n);
    });

    it("rejects non-whitelisted token", async function () {
      const { pool, seller } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const random = await MockERC20.deploy("RAND", "RAND", 18);
      await random.mint(seller.address, ethers.parseEther("1"));
      await random.connect(seller).approve(await pool.getAddress(), ethers.parseEther("1"));
      await expect(
        pool.connect(seller).deposit(await random.getAddress(), ethers.parseEther("1"))
      ).to.be.revertedWith("Token not accepted");
    });

    it("rejects zero amount", async function () {
      const { pool, usdt, seller } = await loadFixture(deployFixture);
      await expect(
        pool.connect(seller).deposit(await usdt.getAddress(), 0)
      ).to.be.revertedWith("Amount must be > 0");
    });

    it("rejects deposits while paused", async function () {
      const { pool, usdt, seller, owner } = await loadFixture(deployFixture);
      await pool.connect(owner).pause();
      await usdt.connect(seller).approve(await pool.getAddress(), 100_000000n);
      await expect(
        pool.connect(seller).deposit(await usdt.getAddress(), 100_000000n)
      ).to.be.reverted;
    });
  });

  describe("Withdraw", function () {
    it("debits available and transfers", async function () {
      const { pool, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const balBefore = await usdt.balanceOf(seller.address);

      await pool.connect(seller).withdraw(await usdt.getAddress(), 2000_000000n);

      const [avail] = await pool.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(3000_000000n);
      expect(await usdt.balanceOf(seller.address)).to.equal(balBefore + 2000_000000n);
    });

    it("reverts when amount exceeds available", async function () {
      const { pool, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 1000_000000n);
      await expect(
        pool.connect(seller).withdraw(await usdt.getAddress(), 2000_000000n)
      ).to.be.revertedWith("Insufficient available");
    });

    it("works even while paused (recovery during incident response)", async function () {
      const { pool, usdt, seller, owner } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 1000_000000n);
      await pool.connect(owner).pause();
      await expect(
        pool.connect(seller).withdraw(await usdt.getAddress(), 1000_000000n)
      ).to.not.be.reverted;
    });

    it("cannot touch locked balance", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 1000_000000n);

      const { voucher, sig } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      const tradeId = ethers.id("t1");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 800_000000n);

      // 800 locked, 200 available → withdrawing 500 must fail
      await expect(
        pool.connect(seller).withdraw(await usdt.getAddress(), 500_000000n)
      ).to.be.revertedWith("Insufficient available");
    });
  });

  // --- Voucher / openTrade ---------------------------------------------------

  describe("openTrade", function () {
    it("debits available, credits locked, records trade", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        maxAmount: 5000_000000n,
      });

      const tradeId = ethers.id("trade-1");
      await expect(pool.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n))
        .to.emit(pool, "TradeOpened")
        .withArgs(tradeId, voucherHash, buyer.address, seller.address, await usdt.getAddress(), 1000_000000n);

      const [avail, locked] = await pool.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(4000_000000n);
      expect(locked).to.equal(1000_000000n);
      expect(await pool.voucherFilled(voucherHash)).to.equal(1000_000000n);
    });

    it("rejects bad signature", async function () {
      const { pool, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher } = await signVoucher(pool, seller, { token: await usdt.getAddress() });

      // sign with the wrong key
      const domain = {
        name: "TSAConnectP2PEscrowPool",
        version: "1",
        chainId: (await ethers.provider.getNetwork()).chainId,
        verifyingContract: await pool.getAddress(),
      };
      const types = {
        AdVoucher: [
          { name: "seller", type: "address" },
          { name: "token", type: "address" },
          { name: "maxAmount", type: "uint256" },
          { name: "minPerTrade", type: "uint256" },
          { name: "maxPerTrade", type: "uint256" },
          { name: "deadline", type: "uint256" },
          { name: "nonce", type: "bytes32" },
        ],
      };
      const badSig = await other.signTypedData(domain, types, voucher);

      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-bad"), voucher, badSig, 100_000000n)
      ).to.be.revertedWith("Bad voucher signature");
    });

    it("rejects expired voucher", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        deadline: (await time.latest()) + 60,
      });
      await time.increase(120);
      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-exp"), voucher, sig, 100_000000n)
      ).to.be.revertedWith("Voucher expired");
    });

    it("enforces per-trade min and max", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        minPerTrade: 100_000000n,
        maxPerTrade: 500_000000n,
      });

      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-min"), voucher, sig, 50_000000n)
      ).to.be.revertedWith("Below voucher min");

      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-max"), voucher, sig, 600_000000n)
      ).to.be.revertedWith("Above voucher max");

      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-ok"), voucher, sig, 300_000000n)
      ).to.not.be.reverted;
    });

    it("accumulates fills across multiple trades and caps at maxAmount", async function () {
      const { pool, usdt, seller, buyer, buyer2 } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });

      await pool.connect(buyer).openTrade(ethers.id("t1"), voucher, sig, 600_000000n);
      await pool.connect(buyer2).openTrade(ethers.id("t2"), voucher, sig, 400_000000n);
      expect(await pool.voucherFilled(voucherHash)).to.equal(1000_000000n);

      await expect(
        pool.connect(buyer).openTrade(ethers.id("t3"), voucher, sig, 1n)
      ).to.be.revertedWith("Exceeds voucher capacity");
    });

    it("reverts when seller's available is insufficient", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 500_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        maxAmount: 5000_000000n, // voucher allows more than deposited
      });
      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-bal"), voucher, sig, 1000_000000n)
      ).to.be.revertedWith("Seller balance insufficient");
    });

    it("rejects self-buy", async function () {
      const { pool, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 1000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      await expect(
        pool.connect(seller).openTrade(ethers.id("t-self"), voucher, sig, 100_000000n)
      ).to.be.revertedWith("Seller cannot self-buy");
    });

    it("rejects duplicate tradeId", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-dup");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 100_000000n);
      await expect(
        pool.connect(buyer).openTrade(tradeId, voucher, sig, 100_000000n)
      ).to.be.revertedWith("Trade already exists");
    });
  });

  describe("cancelVoucher", function () {
    it("only the seller can cancel", async function () {
      const { pool, usdt, seller, other } = await loadFixture(deployFixture);
      const { voucher } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      await expect(pool.connect(other).cancelVoucher(voucher)).to.be.revertedWith("Only voucher seller");
    });

    it("blocks subsequent openTrade", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      await pool.connect(seller).cancelVoucher(voucher);
      await expect(
        pool.connect(buyer).openTrade(ethers.id("t-cancel"), voucher, sig, 100_000000n)
      ).to.be.revertedWith("Voucher cancelled");
    });

    it("does not affect in-flight trades", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });

      const tradeId = ethers.id("t-inflight");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 500_000000n);
      await pool.connect(seller).cancelVoucher(voucher);
      // Trade can still be released normally
      await pool.connect(buyer).markPaid(tradeId);
      await expect(pool.connect(seller).release(tradeId)).to.not.be.reverted;
    });
  });

  // --- Release & fees --------------------------------------------------------

  describe("release", function () {
    async function openedTrade(amount = 1000_000000n) {
      const fx = await loadFixture(deployFixture);
      await depositUsdt(fx.pool, fx.usdt, fx.seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(fx.pool, fx.seller, {
        token: await fx.usdt.getAddress(),
      });
      const tradeId = ethers.id(`t-${Math.random()}`);
      await fx.pool.connect(fx.buyer).openTrade(tradeId, voucher, sig, amount);
      return { ...fx, tradeId, amount };
    }

    it("transfers amount-fee to buyer and fee to system", async function () {
      const { pool, usdt, seller, buyer, system, tradeId, amount } = await openedTrade(1000_000000n);
      const fee = (amount * 50n) / 10000n; // FEE_BPS = 50
      const toBuyer = amount - fee;

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);

      await expect(pool.connect(seller).release(tradeId))
        .to.emit(pool, "TradeReleased").withArgs(tradeId, buyer.address, toBuyer, fee);

      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + toBuyer);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);

      const [, locked] = await pool.balanceOf(seller.address, await usdt.getAddress());
      expect(locked).to.equal(0n);
    });

    it("is fee-free for MCGP", async function () {
      const { pool, mcgp, seller, buyer, system } = await loadFixture(deployFixture);
      const mcgpAddr = await mcgp.getAddress();
      const amount = ethers.parseEther("100");

      await mcgp.connect(seller).approve(await pool.getAddress(), amount);
      await pool.connect(seller).deposit(mcgpAddr, amount);
      const { voucher, sig } = await signVoucher(pool, seller, { token: mcgpAddr, maxAmount: amount });

      const tradeId = ethers.id("t-mcgp");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, amount);

      const buyerBefore = await mcgp.balanceOf(buyer.address);
      const systemBefore = await mcgp.balanceOf(system.address);
      await pool.connect(seller).release(tradeId);
      expect(await mcgp.balanceOf(buyer.address)).to.equal(buyerBefore + amount);
      expect(await mcgp.balanceOf(system.address)).to.equal(systemBefore); // no fee
    });

    it("only seller (or admin) can release", async function () {
      const { pool, buyer, other, operator, tradeId } = await openedTrade();
      await expect(pool.connect(buyer).release(tradeId)).to.be.revertedWith("Only seller");
      await expect(pool.connect(other).release(tradeId)).to.be.revertedWith("Only seller");
      await expect(pool.connect(operator).adminRelease(tradeId)).to.not.be.reverted;
    });

    it("works from both Pending and Paid", async function () {
      // Pending → release
      const a = await openedTrade();
      await a.pool.connect(a.seller).release(a.tradeId);

      // Paid → release
      const b = await openedTrade();
      await b.pool.connect(b.buyer).markPaid(b.tradeId);
      await b.pool.connect(b.seller).release(b.tradeId);
    });
  });

  // --- Cancel by timeout -----------------------------------------------------

  describe("cancelByTimeout", function () {
    it("returns locked to available after TIMEOUT (Pending only)", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-timeout");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);

      await time.increase(THIRTY_MIN + 1);
      await expect(pool.cancelByTimeout(tradeId))
        .to.emit(pool, "TradeCancelled").withArgs(tradeId, 1000_000000n);

      const [avail, locked] = await pool.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(5000_000000n);
      expect(locked).to.equal(0n);
    });

    it("reverts before timeout", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-early");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await expect(pool.cancelByTimeout(tradeId)).to.be.revertedWith("Timeout not elapsed");
    });

    it("rejects already-paid trades", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-paid-timeout");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await pool.connect(buyer).markPaid(tradeId);
      await time.increase(THIRTY_MIN + 1);
      await expect(pool.cancelByTimeout(tradeId)).to.be.revertedWith("Not pending");
    });
  });

  // --- Dispute ---------------------------------------------------------------

  describe("Dispute & adminResolve", function () {
    async function disputedTrade(by = "buyer") {
      const fx = await loadFixture(deployFixture);
      await depositUsdt(fx.pool, fx.usdt, fx.seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(fx.pool, fx.seller, {
        token: await fx.usdt.getAddress(),
      });
      const tradeId = ethers.id(`disp-${Math.random()}`);
      await fx.pool.connect(fx.buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      const raiser = by === "seller" ? fx.seller : fx.buyer;
      await fx.pool.connect(raiser).raiseDispute(tradeId);
      return { ...fx, tradeId };
    }

    it("either party can raise; non-parties cannot", async function () {
      const { pool, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-disp-party");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await expect(pool.connect(other).raiseDispute(tradeId)).to.be.revertedWith("Not a party");
      await expect(pool.connect(seller).raiseDispute(tradeId)).to.not.be.reverted;
    });

    it("adminResolve(true) pays buyer with fee", async function () {
      const { pool, usdt, operator, buyer, system, tradeId } = await disputedTrade();
      const amount = 1000_000000n;
      const fee = (amount * 50n) / 10000n;
      const toBuyer = amount - fee;
      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);
      await pool.connect(operator).adminResolve(tradeId, true);
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + toBuyer);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);
    });

    it("adminResolve(false) refunds to seller's available", async function () {
      const { pool, usdt, seller, operator, tradeId } = await disputedTrade();
      await pool.connect(operator).adminResolve(tradeId, false);
      const [avail, locked] = await pool.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(5000_000000n);
      expect(locked).to.equal(0n);
    });

    it("admin operations work while paused", async function () {
      const { pool, owner, operator, tradeId } = await disputedTrade();
      await pool.connect(owner).pause();
      await expect(pool.connect(operator).adminResolve(tradeId, true)).to.not.be.reverted;
    });

    it("non-admin cannot adminResolve", async function () {
      const { pool, other, tradeId } = await disputedTrade();
      await expect(pool.connect(other).adminResolve(tradeId, true)).to.be.revertedWith("Not admin");
    });
  });

  // --- Misc invariants -------------------------------------------------------

  describe("Invariants", function () {
    it("contract token balance equals sum of available + locked", async function () {
      const { pool, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(pool, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(pool, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-inv");
      await pool.connect(buyer).openTrade(tradeId, voucher, sig, 1500_000000n);

      const [avail, locked] = await pool.balanceOf(seller.address, await usdt.getAddress());
      const contractBal = await usdt.balanceOf(await pool.getAddress());
      expect(avail + locked).to.equal(contractBal);
    });
  });
});
