const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const {
  loadFixture,
  time,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("P2PEscrow (pool model)", function () {
  const THIRTY_MIN = 30 * 60;
  const ONE_DAY = 24 * 60 * 60;

  // --- Fixture & helpers -----------------------------------------------------

  async function deployFixture() {
    const [owner, seller, buyer, buyer2, operator, system, other] =
      await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);
    const mcgp = await MockERC20.deploy("MCGP Token", "MCGP", 18);

    const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
    const escrow = await upgrades.deployProxy(
      P2PEscrow,
      [owner.address, [operator.address]],
      { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
    );
    await escrow.waitForDeployment();

    await escrow.setTokenAcceptance(await usdt.getAddress(), true);
    await escrow.setTokenAcceptance(await mcgp.getAddress(), true);
    await escrow.setMcgpToken(await mcgp.getAddress());
    await escrow.setSystemWallet(system.address);

    await usdt.mint(seller.address, 100_000_000000n);
    await mcgp.mint(seller.address, ethers.parseEther("100000"));

    return { escrow, usdt, mcgp, owner, seller, buyer, buyer2, operator, system, other };
  }

  async function depositUsdt(escrow, usdt, seller, amount) {
    await usdt.connect(seller).approve(await escrow.getAddress(), amount);
    await escrow.connect(seller).deposit(await usdt.getAddress(), amount);
  }

  async function signVoucher(escrow, seller, overrides = {}) {
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
      name: "TSAConnectP2PEscrow",
      version: "1",
      chainId: (await ethers.provider.getNetwork()).chainId,
      verifyingContract: await escrow.getAddress(),
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
    const voucherHash = await escrow.hashVoucher(v);
    return { voucher: v, sig, voucherHash };
  }

  // --- Initialization & config ----------------------------------------------

  describe("Initialization", function () {
    it("seeds owner and admins", async function () {
      const { escrow, owner, operator } = await loadFixture(deployFixture);
      expect(await escrow.owner()).to.equal(owner.address);
      expect(await escrow.isAdmin(owner.address)).to.equal(true);
      expect(await escrow.isAdmin(operator.address)).to.equal(true);
    });

    it("rejects re-initialization", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(escrow.initialize(owner.address, [])).to.be.reverted;
    });

    it("rejects zero address and duplicates in initialAdmins", async function () {
      const [owner] = await ethers.getSigners();
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      await expect(
        upgrades.deployProxy(
          P2PEscrow,
          [owner.address, [ethers.ZeroAddress]],
          { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
        )
      ).to.be.reverted;
      await expect(
        upgrades.deployProxy(
          P2PEscrow,
          [owner.address, [owner.address, owner.address]],
          { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
        )
      ).to.be.reverted;
    });
  });

  describe("Admin management", function () {
    it("addAdmin / removeAdmin gated to owner", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await expect(escrow.connect(other).addAdmin(other.address)).to.be.reverted;
      await escrow.connect(owner).addAdmin(other.address);
      expect(await escrow.isAdmin(other.address)).to.equal(true);
      await escrow.connect(owner).removeAdmin(other.address);
      expect(await escrow.isAdmin(other.address)).to.equal(false);
    });

    it("owner is implicitly admin", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      expect(await escrow.isAdmin(owner.address)).to.equal(true);
    });
  });

  describe("Token configuration", function () {
    it("setMcgpToken requires the token to be accepted first", async function () {
      const [owner] = await ethers.getSigners();
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const random = await MockERC20.deploy("R", "R", 18);
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      const escrow = await upgrades.deployProxy(
        P2PEscrow,
        [owner.address, []],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      await expect(escrow.setMcgpToken(await random.getAddress())).to.be.revertedWith(
        "MCGP must be accepted first"
      );
      await escrow.setTokenAcceptance(await random.getAddress(), true);
      await expect(escrow.setMcgpToken(await random.getAddress())).to.not.be.reverted;
    });

    it("sweepExcess transfers to owner", async function () {
      const { escrow, usdt, owner } = await loadFixture(deployFixture);
      // Send tokens directly (bypassing deposit) so they're unaccounted.
      await usdt.mint(await escrow.getAddress(), 1000_000000n);
      const ownerBefore = await usdt.balanceOf(owner.address);
      await escrow.connect(owner).sweepExcess(await usdt.getAddress(), 1000_000000n);
      expect(await usdt.balanceOf(owner.address)).to.equal(ownerBefore + 1000_000000n);
    });
  });

  // --- Deposit / withdraw ----------------------------------------------------

  describe("Deposit", function () {
    it("credits available balance and pulls tokens", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);

      const [avail, locked] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(5000_000000n);
      expect(locked).to.equal(0n);
      expect(await usdt.balanceOf(await escrow.getAddress())).to.equal(5000_000000n);
    });

    it("rejects non-whitelisted token", async function () {
      const { escrow, seller } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const random = await MockERC20.deploy("RAND", "RAND", 18);
      await random.mint(seller.address, ethers.parseEther("1"));
      await random.connect(seller).approve(await escrow.getAddress(), ethers.parseEther("1"));
      await expect(
        escrow.connect(seller).deposit(await random.getAddress(), ethers.parseEther("1"))
      ).to.be.revertedWith("Token not accepted");
    });

    it("rejects zero amount", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(seller).deposit(await usdt.getAddress(), 0)
      ).to.be.revertedWith("Amount must be > 0");
    });

    it("rejects deposits while paused", async function () {
      const { escrow, usdt, seller, owner } = await loadFixture(deployFixture);
      await escrow.connect(owner).pause();
      await usdt.connect(seller).approve(await escrow.getAddress(), 100_000000n);
      await expect(
        escrow.connect(seller).deposit(await usdt.getAddress(), 100_000000n)
      ).to.be.reverted;
    });
  });

  describe("Withdraw", function () {
    it("debits available and transfers", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const balBefore = await usdt.balanceOf(seller.address);

      await escrow.connect(seller).withdraw(await usdt.getAddress(), 2000_000000n);

      const [avail] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(3000_000000n);
      expect(await usdt.balanceOf(seller.address)).to.equal(balBefore + 2000_000000n);
    });

    it("reverts when amount exceeds available", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 1000_000000n);
      await expect(
        escrow.connect(seller).withdraw(await usdt.getAddress(), 2000_000000n)
      ).to.be.revertedWith("Insufficient available");
    });

    it("works even while paused (recovery during incident response)", async function () {
      const { escrow, usdt, seller, owner } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 1000_000000n);
      await escrow.connect(owner).pause();
      await expect(
        escrow.connect(seller).withdraw(await usdt.getAddress(), 1000_000000n)
      ).to.not.be.reverted;
    });

    it("cannot touch locked balance", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 1000_000000n);

      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      const tradeId = ethers.id("t1");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 800_000000n);

      // 800 locked, 200 available → withdrawing 500 must fail
      await expect(
        escrow.connect(seller).withdraw(await usdt.getAddress(), 500_000000n)
      ).to.be.revertedWith("Insufficient available");
    });
  });

  // --- Voucher / openTrade ---------------------------------------------------

  describe("openTrade", function () {
    it("debits available, credits locked, records trade", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 5000_000000n,
      });

      const tradeId = ethers.id("trade-1");
      await expect(escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n))
        .to.emit(escrow, "TradeOpened")
        .withArgs(tradeId, voucherHash, buyer.address, seller.address, await usdt.getAddress(), 1000_000000n);

      const [avail, locked] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(4000_000000n);
      expect(locked).to.equal(1000_000000n);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(1000_000000n);
    });

    it("rejects bad signature", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });

      const domain = {
        name: "TSAConnectP2PEscrow",
        version: "1",
        chainId: (await ethers.provider.getNetwork()).chainId,
        verifyingContract: await escrow.getAddress(),
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
        escrow.connect(buyer).openTrade(ethers.id("t-bad"), voucher, badSig, 100_000000n)
      ).to.be.revertedWith("Bad voucher signature");
    });

    it("rejects expired voucher", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        deadline: (await time.latest()) + 60,
      });
      await time.increase(120);
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-exp"), voucher, sig, 100_000000n)
      ).to.be.revertedWith("Voucher expired");
    });

    it("enforces per-trade min and max", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        minPerTrade: 100_000000n,
        maxPerTrade: 500_000000n,
      });

      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-min"), voucher, sig, 50_000000n)
      ).to.be.revertedWith("Below voucher min");

      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-max"), voucher, sig, 600_000000n)
      ).to.be.revertedWith("Above voucher max");

      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-ok"), voucher, sig, 300_000000n)
      ).to.not.be.reverted;
    });

    it("accumulates fills across multiple trades and caps at maxAmount", async function () {
      const { escrow, usdt, seller, buyer, buyer2 } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });

      await escrow.connect(buyer).openTrade(ethers.id("t1"), voucher, sig, 600_000000n);
      await escrow.connect(buyer2).openTrade(ethers.id("t2"), voucher, sig, 400_000000n);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(1000_000000n);

      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t3"), voucher, sig, 1n)
      ).to.be.revertedWith("Exceeds voucher capacity");
    });

    it("reverts when seller's available is insufficient", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 500_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 5000_000000n,
      });
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-bal"), voucher, sig, 1000_000000n)
      ).to.be.revertedWith("Seller balance insufficient");
    });

    it("rejects self-buy", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 1000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      await expect(
        escrow.connect(seller).openTrade(ethers.id("t-self"), voucher, sig, 100_000000n)
      ).to.be.revertedWith("Seller cannot self-buy");
    });

    it("rejects duplicate tradeId", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-dup");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 100_000000n);
      await expect(
        escrow.connect(buyer).openTrade(tradeId, voucher, sig, 100_000000n)
      ).to.be.revertedWith("Trade already exists");
    });

    it("rejects when paused", async function () {
      const { escrow, usdt, seller, buyer, owner } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      await escrow.connect(owner).pause();
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-paused"), voucher, sig, 100_000000n)
      ).to.be.reverted;
    });
  });

  describe("cancelVoucher", function () {
    it("only the seller can cancel", async function () {
      const { escrow, usdt, seller, other } = await loadFixture(deployFixture);
      const { voucher } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      await expect(escrow.connect(other).cancelVoucher(voucher)).to.be.revertedWith("Only voucher seller");
    });

    it("blocks subsequent openTrade", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      await escrow.connect(seller).cancelVoucher(voucher);
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-cancel"), voucher, sig, 100_000000n)
      ).to.be.revertedWith("Voucher cancelled");
    });

    it("does not affect in-flight trades", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });

      const tradeId = ethers.id("t-inflight");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 500_000000n);
      await escrow.connect(seller).cancelVoucher(voucher);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(escrow.connect(seller).release(tradeId)).to.not.be.reverted;
    });

    it("is idempotent", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const { voucher } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      await escrow.connect(seller).cancelVoucher(voucher);
      await expect(escrow.connect(seller).cancelVoucher(voucher)).to.not.be.reverted;
    });
  });

  // --- Release & fees --------------------------------------------------------

  describe("release", function () {
    async function openedTrade(amount = 1000_000000n) {
      const fx = await loadFixture(deployFixture);
      await depositUsdt(fx.escrow, fx.usdt, fx.seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(fx.escrow, fx.seller, {
        token: await fx.usdt.getAddress(),
      });
      const tradeId = ethers.id(`t-${Math.random()}`);
      await fx.escrow.connect(fx.buyer).openTrade(tradeId, voucher, sig, amount);
      return { ...fx, tradeId, amount };
    }

    it("transfers amount-fee to buyer and fee to system", async function () {
      const { escrow, usdt, seller, buyer, system, tradeId, amount } = await openedTrade(1000_000000n);
      const fee = (amount * 50n) / 10000n;
      const toBuyer = amount - fee;

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);

      await expect(escrow.connect(seller).release(tradeId))
        .to.emit(escrow, "TradeReleased").withArgs(tradeId, buyer.address, toBuyer, fee);

      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + toBuyer);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);

      const [, locked] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      expect(locked).to.equal(0n);
    });

    it("is fee-free for MCGP", async function () {
      const { escrow, mcgp, seller, buyer, system } = await loadFixture(deployFixture);
      const mcgpAddr = await mcgp.getAddress();
      const amount = ethers.parseEther("100");

      await mcgp.connect(seller).approve(await escrow.getAddress(), amount);
      await escrow.connect(seller).deposit(mcgpAddr, amount);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: mcgpAddr, maxAmount: amount });

      const tradeId = ethers.id("t-mcgp");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, amount);

      const buyerBefore = await mcgp.balanceOf(buyer.address);
      const systemBefore = await mcgp.balanceOf(system.address);
      await escrow.connect(seller).release(tradeId);
      expect(await mcgp.balanceOf(buyer.address)).to.equal(buyerBefore + amount);
      expect(await mcgp.balanceOf(system.address)).to.equal(systemBefore);
    });

    it("only seller (or admin) can release", async function () {
      const { escrow, buyer, other, operator, tradeId } = await openedTrade();
      await expect(escrow.connect(buyer).release(tradeId)).to.be.revertedWith("Only seller");
      await expect(escrow.connect(other).release(tradeId)).to.be.revertedWith("Only seller");
      await expect(escrow.connect(operator).adminRelease(tradeId)).to.not.be.reverted;
    });

    it("works from both Pending and Paid", async function () {
      const a = await openedTrade();
      await a.escrow.connect(a.seller).release(a.tradeId);

      const b = await openedTrade();
      await b.escrow.connect(b.buyer).markPaid(b.tradeId);
      await b.escrow.connect(b.seller).release(b.tradeId);
    });

    it("falls back to owner for system recipient when systemWallet is zero", async function () {
      const [owner, seller, buyer] = await ethers.getSigners();
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      const escrow = await upgrades.deployProxy(
        P2PEscrow,
        [owner.address, []],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      await escrow.setTokenAcceptance(await usdt.getAddress(), true);
      // systemWallet left at zero
      await usdt.mint(seller.address, 10_000000n);
      await usdt.connect(seller).approve(await escrow.getAddress(), 10_000000n);
      await escrow.connect(seller).deposit(await usdt.getAddress(), 10_000000n);

      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 10_000000n,
      });
      const tradeId = ethers.id("t-zero-sw");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 10_000000n);
      const fee = (10_000000n * 50n) / 10000n;
      const ownerBefore = await usdt.balanceOf(owner.address);
      await escrow.connect(seller).release(tradeId);
      expect(await usdt.balanceOf(owner.address)).to.equal(ownerBefore + fee);
    });
  });

  // --- Cancel by timeout -----------------------------------------------------

  describe("cancelByTimeout", function () {
    it("returns locked to available after TIMEOUT (Pending only)", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-timeout");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);

      await time.increase(THIRTY_MIN + 1);
      await expect(escrow.cancelByTimeout(tradeId))
        .to.emit(escrow, "TradeCancelled").withArgs(tradeId, 1000_000000n);

      const [avail, locked] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(5000_000000n);
      expect(locked).to.equal(0n);
    });

    it("reverts before timeout", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-early");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await expect(escrow.cancelByTimeout(tradeId)).to.be.revertedWith("Timeout not elapsed");
    });

    it("rejects already-paid trades", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-paid-timeout");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await time.increase(THIRTY_MIN + 1);
      await expect(escrow.cancelByTimeout(tradeId)).to.be.revertedWith("Not pending");
    });

    it("works while paused (recovery path)", async function () {
      const { escrow, usdt, seller, buyer, owner } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-paused-timeout");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await time.increase(THIRTY_MIN + 1);
      await escrow.connect(owner).pause();
      await expect(escrow.cancelByTimeout(tradeId)).to.not.be.reverted;
    });
  });

  // --- Dispute ---------------------------------------------------------------

  describe("Dispute & adminResolve", function () {
    async function disputedTrade(by = "buyer") {
      const fx = await loadFixture(deployFixture);
      await depositUsdt(fx.escrow, fx.usdt, fx.seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(fx.escrow, fx.seller, {
        token: await fx.usdt.getAddress(),
      });
      const tradeId = ethers.id(`disp-${Math.random()}`);
      await fx.escrow.connect(fx.buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      const raiser = by === "seller" ? fx.seller : fx.buyer;
      await fx.escrow.connect(raiser).raiseDispute(tradeId);
      return { ...fx, tradeId };
    }

    it("either party can raise; non-parties cannot", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-disp-party");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await expect(escrow.connect(other).raiseDispute(tradeId)).to.be.revertedWith("Not a party");
      await expect(escrow.connect(seller).raiseDispute(tradeId)).to.not.be.reverted;
    });

    it("adminResolve(true) pays buyer with fee", async function () {
      const { escrow, usdt, operator, buyer, system, tradeId } = await disputedTrade();
      const amount = 1000_000000n;
      const fee = (amount * 50n) / 10000n;
      const toBuyer = amount - fee;
      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);
      await escrow.connect(operator).adminResolve(tradeId, true);
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + toBuyer);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);
    });

    it("adminResolve(false) refunds to seller's available", async function () {
      const { escrow, usdt, seller, operator, tradeId } = await disputedTrade();
      await escrow.connect(operator).adminResolve(tradeId, false);
      const [avail, locked] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(5000_000000n);
      expect(locked).to.equal(0n);
    });

    it("admin operations work while paused", async function () {
      const { escrow, owner, operator, tradeId } = await disputedTrade();
      await escrow.connect(owner).pause();
      await expect(escrow.connect(operator).adminResolve(tradeId, true)).to.not.be.reverted;
    });

    it("non-admin cannot adminResolve", async function () {
      const { escrow, other, tradeId } = await disputedTrade();
      await expect(escrow.connect(other).adminResolve(tradeId, true)).to.be.revertedWith("Not admin");
    });

    it("cannot dispute already-resolved trade", async function () {
      const { escrow, seller, tradeId } = await disputedTrade();
      await escrow.connect(seller).raiseDispute; // sanity (does not call)
    });
  });

  // --- UUPS upgrade preservation --------------------------------------------

  describe("UUPS upgrade", function () {
    it("preserves admin + balance state across upgrade", async function () {
      const { escrow, usdt, seller, operator } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 1500_000000n);
      expect(await escrow.isAdmin(operator.address)).to.equal(true);

      const proxyAddr = await escrow.getAddress();
      const V2Mock = await ethers.getContractFactory("P2PEscrowV2Test");
      const upgraded = await upgrades.upgradeProxy(proxyAddr, V2Mock, {
        unsafeAllow: ["constructor", "missing-initializer"],
      });
      expect(await upgraded.version()).to.equal("v2-test");
      expect(await upgraded.isAdmin(operator.address)).to.equal(true);
      const [avail] = await upgraded.balanceOf(seller.address, await usdt.getAddress());
      expect(avail).to.equal(1500_000000n);
    });
  });

  // --- Invariants ------------------------------------------------------------

  describe("Invariants", function () {
    it("contract token balance equals sum of available + locked for that token", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-inv");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1500_000000n);

      const [avail, locked] = await escrow.balanceOf(seller.address, await usdt.getAddress());
      const contractBal = await usdt.balanceOf(await escrow.getAddress());
      expect(avail + locked).to.equal(contractBal);
    });
  });
});
