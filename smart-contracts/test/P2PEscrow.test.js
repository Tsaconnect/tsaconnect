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

    it("non-MCGP release reverts loudly when systemWallet is unset", async function () {
      // Refusing to silently route fees to owner() is the safer default —
      // a forgotten setSystemWallet at deploy time would otherwise quietly
      // send every fee to the deployer EOA. This test pins that behaviour.
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
      await expect(
        escrow.connect(seller).release(tradeId)
      ).to.be.revertedWith("System wallet not set");

      // Once set, release succeeds and the fee lands on the configured wallet.
      await escrow.connect(owner).setSystemWallet(buyer.address); // any non-zero
      await expect(escrow.connect(seller).release(tradeId)).to.not.be.reverted;
    });

    it("MCGP release still works without a systemWallet (fee=0, no recipient call)", async function () {
      const [owner, seller, buyer] = await ethers.getSigners();
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const mcgp = await MockERC20.deploy("MCGP", "MCGP", 18);
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      const escrow = await upgrades.deployProxy(
        P2PEscrow,
        [owner.address, []],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      const mcgpAddr = await mcgp.getAddress();
      await escrow.setTokenAcceptance(mcgpAddr, true);
      await escrow.setMcgpToken(mcgpAddr);
      // systemWallet intentionally unset — for MCGP the fee is 0 so the
      // _systemRecipient check is never reached.
      const amount = ethers.parseEther("10");
      await mcgp.mint(seller.address, amount);
      await mcgp.connect(seller).approve(await escrow.getAddress(), amount);
      await escrow.connect(seller).deposit(mcgpAddr, amount);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: mcgpAddr, maxAmount: amount });
      const tradeId = ethers.id("t-mcgp-no-sw");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, amount);
      await expect(escrow.connect(seller).release(tradeId)).to.not.be.reverted;
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
      const { escrow, seller, operator, tradeId } = await disputedTrade();
      await escrow.connect(operator).adminResolve(tradeId, true);
      await expect(
        escrow.connect(seller).raiseDispute(tradeId)
      ).to.be.revertedWith("Not disputable");
    });

    it("cannot re-dispute an already-disputed trade", async function () {
      const { escrow, seller, tradeId } = await disputedTrade();
      await expect(
        escrow.connect(seller).raiseDispute(tradeId)
      ).to.be.revertedWith("Not disputable");
    });

    it("adminResolve is single-shot (cannot resolve twice)", async function () {
      const { escrow, operator, tradeId } = await disputedTrade();
      await escrow.connect(operator).adminResolve(tradeId, true);
      await expect(
        escrow.connect(operator).adminResolve(tradeId, true)
      ).to.be.revertedWith("Not disputed");
      await expect(
        escrow.connect(operator).adminResolve(tradeId, false)
      ).to.be.revertedWith("Not disputed");
    });

    it("cancelByTimeout rejects Disputed trades", async function () {
      const { escrow, tradeId } = await disputedTrade();
      await time.increase(THIRTY_MIN + 1);
      await expect(escrow.cancelByTimeout(tradeId)).to.be.revertedWith("Not pending");
    });

    it("cannot dispute Released trade", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-disp-released");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await escrow.connect(seller).release(tradeId);
      await expect(
        escrow.connect(seller).raiseDispute(tradeId)
      ).to.be.revertedWith("Not disputable");
    });

    it("cannot dispute Cancelled trade", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });
      const tradeId = ethers.id("t-disp-cancelled");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await time.increase(THIRTY_MIN + 1);
      await escrow.cancelByTimeout(tradeId);
      await expect(
        escrow.connect(seller).raiseDispute(tradeId)
      ).to.be.revertedWith("Not disputable");
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

    it("accountedBalance equals sum of available + locked across the lifecycle", async function () {
      const { escrow, usdt, seller, buyer, operator } = await loadFixture(deployFixture);
      const tok = await usdt.getAddress();
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      expect(await escrow.accountedBalance(tok)).to.equal(5000_000000n);

      const { voucher, sig } = await signVoucher(escrow, seller, { token: tok });
      const t1 = ethers.id("a-1");
      await escrow.connect(buyer).openTrade(t1, voucher, sig, 1000_000000n);
      // openTrade is an internal move — accountedBalance unchanged
      expect(await escrow.accountedBalance(tok)).to.equal(5000_000000n);

      await escrow.connect(seller).release(t1);
      // release sends funds OUT — accountedBalance -= 1000
      expect(await escrow.accountedBalance(tok)).to.equal(4000_000000n);

      await escrow.connect(seller).withdraw(tok, 500_000000n);
      expect(await escrow.accountedBalance(tok)).to.equal(3500_000000n);

      // Timeout cancel: accounted unchanged (funds stayed in contract)
      const t2 = ethers.id("a-2");
      await escrow.connect(buyer).openTrade(t2, voucher, sig, 100_000000n);
      await time.increase(THIRTY_MIN + 1);
      await escrow.cancelByTimeout(t2);
      expect(await escrow.accountedBalance(tok)).to.equal(3500_000000n);
    });

    it("accountedBalance debits correctly through dispute → adminResolve(true)", async function () {
      const { escrow, usdt, seller, buyer, operator } = await loadFixture(deployFixture);
      const tok = await usdt.getAddress();
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: tok });
      const tradeId = ethers.id("a-disp");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      expect(await escrow.accountedBalance(tok)).to.equal(5000_000000n);

      await escrow.connect(buyer).raiseDispute(tradeId);
      // raiseDispute is state-only — accounted unchanged
      expect(await escrow.accountedBalance(tok)).to.equal(5000_000000n);

      await escrow.connect(operator).adminResolve(tradeId, true);
      // Funds left the contract → accounted -= 1000
      expect(await escrow.accountedBalance(tok)).to.equal(4000_000000n);
    });
  });

  // --- sweepExcess fence -----------------------------------------------------

  describe("sweepExcess fence", function () {
    it("refuses to drain user deposits", async function () {
      const { escrow, usdt, seller, owner } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      await expect(
        escrow.connect(owner).sweepExcess(await usdt.getAddress(), 1)
      ).to.be.revertedWith("Exceeds excess");
    });

    it("allows sweeping only excess (tokens sent directly)", async function () {
      const { escrow, usdt, seller, owner } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      await usdt.mint(await escrow.getAddress(), 123_000000n);

      const ownerBefore = await usdt.balanceOf(owner.address);
      await escrow.connect(owner).sweepExcess(await usdt.getAddress(), 123_000000n);
      expect(await usdt.balanceOf(owner.address)).to.equal(ownerBefore + 123_000000n);

      // Now any further sweep should hit the fence
      await expect(
        escrow.connect(owner).sweepExcess(await usdt.getAddress(), 1)
      ).to.be.revertedWith("Exceeds excess");
    });
  });

  // --- Voucher capacity refund on cancel/dispute ------------------------------

  describe("voucherFilled refund", function () {
    it("decrements on cancelByTimeout so seller can re-fill the slot", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      await escrow.connect(buyer).openTrade(ethers.id("vf-1"), voucher, sig, 1000_000000n);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(1000_000000n);

      await time.increase(THIRTY_MIN + 1);
      await escrow.cancelByTimeout(ethers.id("vf-1"));
      expect(await escrow.voucherFilled(voucherHash)).to.equal(0n);

      // Seller's full voucher capacity is restored — a new buyer can take it.
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("vf-2"), voucher, sig, 1000_000000n)
      ).to.not.be.reverted;
    });

    it("decrements on adminResolve(false) (void trade)", async function () {
      const { escrow, usdt, seller, buyer, operator } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      const tradeId = ethers.id("vf-resolve-false");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 800_000000n);
      await escrow.connect(buyer).raiseDispute(tradeId);
      await escrow.connect(operator).adminResolve(tradeId, false);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(0n);
    });

    it("does NOT decrement on adminResolve(true) — the fill was real", async function () {
      const { escrow, usdt, seller, buyer, operator } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      const tradeId = ethers.id("vf-resolve-true");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 700_000000n);
      await escrow.connect(buyer).raiseDispute(tradeId);
      await escrow.connect(operator).adminResolve(tradeId, true);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(700_000000n);
    });

    it("multi-fill voucher: cancelling one trade refunds only that trade's slice", async function () {
      const { escrow, usdt, seller, buyer, buyer2 } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      const t1 = ethers.id("vf-multi-1");
      const t2 = ethers.id("vf-multi-2");
      await escrow.connect(buyer).openTrade(t1, voucher, sig, 600_000000n);
      await escrow.connect(buyer2).openTrade(t2, voucher, sig, 400_000000n);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(1000_000000n);

      // Time out only the first trade.
      await time.increase(THIRTY_MIN + 1);
      await escrow.cancelByTimeout(t1);
      // Voucher fill drops back to just t2's slice.
      expect(await escrow.voucherFilled(voucherHash)).to.equal(400_000000n);

      // The 600 slice is now re-fillable by a new buyer.
      const t3 = ethers.id("vf-multi-3");
      await expect(
        escrow.connect(buyer).openTrade(t3, voucher, sig, 600_000000n)
      ).to.not.be.reverted;
      expect(await escrow.voucherFilled(voucherHash)).to.equal(1000_000000n);
    });

    it("does NOT decrement on release — the fill was real", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig, voucherHash } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
      });
      const tradeId = ethers.id("vf-release");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 600_000000n);
      await escrow.connect(seller).release(tradeId);
      expect(await escrow.voucherFilled(voucherHash)).to.equal(600_000000n);
    });
  });

  // --- Reentrancy ------------------------------------------------------------

  describe("Reentrancy", function () {
    async function deployWithReentrant() {
      const [owner, seller, buyer, operator, system] = await ethers.getSigners();
      const Reentrant = await ethers.getContractFactory("ReentrantERC20");
      const tok = await Reentrant.deploy("Reentrant USDT", "rUSDT", 6);

      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      const escrow = await upgrades.deployProxy(
        P2PEscrow, [owner.address, [operator.address]],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      await escrow.setTokenAcceptance(await tok.getAddress(), true);
      await escrow.setSystemWallet(system.address);
      await tok.mint(seller.address, 100_000_000000n);
      await tok.connect(seller).approve(await escrow.getAddress(), 100_000_000000n);
      await escrow.connect(seller).deposit(await tok.getAddress(), 50_000_000000n);
      return { escrow, tok, owner, seller, buyer, operator, system };
    }

    it("guard prevents re-entry into release via malicious ERC20", async function () {
      const { escrow, tok, seller, buyer } = await deployWithReentrant();
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await tok.getAddress() });
      const tradeId = ethers.id("re-release");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);

      // Arm: when escrow transfers to buyer, try to call release(tradeId) again.
      const data = escrow.interface.encodeFunctionData("release", [tradeId]);
      await tok.armAttack(await escrow.getAddress(), data);

      await expect(escrow.connect(seller).release(tradeId)).to.be.reverted;
    });

    it("guard prevents re-entry into withdraw via malicious ERC20", async function () {
      const { escrow, tok, seller } = await deployWithReentrant();
      const tokAddr = await tok.getAddress();
      const data = escrow.interface.encodeFunctionData("withdraw", [tokAddr, 1_000000n]);
      await tok.armAttack(await escrow.getAddress(), data);

      await expect(
        escrow.connect(seller).withdraw(tokAddr, 1_000000n)
      ).to.be.reverted;
    });

    it("guard prevents re-entry into adminResolve via malicious ERC20", async function () {
      const { escrow, tok, seller, buyer, operator } = await deployWithReentrant();
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await tok.getAddress() });
      const tradeId = ethers.id("re-resolve");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      await escrow.connect(buyer).raiseDispute(tradeId);

      const data = escrow.interface.encodeFunctionData("adminResolve", [tradeId, true]);
      await tok.armAttack(await escrow.getAddress(), data);

      await expect(escrow.connect(operator).adminResolve(tradeId, true)).to.be.reverted;
    });
  });

  // --- EIP-712 domain + signature edge cases --------------------------------

  describe("EIP-712 domain & malleability", function () {
    it("rejects signature from a different chainId", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const v = {
        seller: seller.address,
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
        minPerTrade: 0n,
        maxPerTrade: 0n,
        deadline: (await time.latest()) + ONE_DAY,
        nonce: ethers.id("evil-chain"),
      };
      const wrongChainDomain = {
        name: "TSAConnectP2PEscrow",
        version: "1",
        chainId: 99999n, // not the live chain
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
      const sig = await seller.signTypedData(wrongChainDomain, types, v);
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-chain"), v, sig, 100_000000n)
      ).to.be.revertedWith("Bad voucher signature");
    });

    it("rejects signature for a different verifyingContract", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const v = {
        seller: seller.address,
        token: await usdt.getAddress(),
        maxAmount: 1000_000000n,
        minPerTrade: 0n,
        maxPerTrade: 0n,
        deadline: (await time.latest()) + ONE_DAY,
        nonce: ethers.id("evil-contract"),
      };
      const wrongContractDomain = {
        name: "TSAConnectP2PEscrow",
        version: "1",
        chainId: (await ethers.provider.getNetwork()).chainId,
        verifyingContract: ethers.Wallet.createRandom().address,
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
      const sig = await seller.signTypedData(wrongContractDomain, types, v);
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-contract"), v, sig, 100_000000n)
      ).to.be.revertedWith("Bad voucher signature");
    });

    it("rejects high-S (malleable) signature variant", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: await usdt.getAddress() });

      // Flip a canonical signature to its high-S complement.
      const SECP256K1_N = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141n;
      const sigBytes = ethers.getBytes(sig);
      const r = sigBytes.slice(0, 32);
      const sLow = BigInt("0x" + Buffer.from(sigBytes.slice(32, 64)).toString("hex"));
      const v = sigBytes[64];
      const sHigh = SECP256K1_N - sLow;
      const vFlipped = v === 27 ? 28 : 27;
      const malleable = ethers.concat([
        r,
        ethers.zeroPadValue("0x" + sHigh.toString(16), 32),
        new Uint8Array([vFlipped]),
      ]);
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-mal"), voucher, malleable, 100_000000n)
      ).to.be.reverted;
    });

    it("hashVoucher matches ethers.TypedDataEncoder.hash", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const v = {
        seller: seller.address,
        token: await usdt.getAddress(),
        maxAmount: 1234n,
        minPerTrade: 10n,
        maxPerTrade: 100n,
        deadline: 1900000000n,
        nonce: ethers.id("parity"),
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
      const jsHash = ethers.TypedDataEncoder.hash(domain, types, v);
      const contractHash = await escrow.hashVoucher(v);
      expect(contractHash).to.equal(jsHash);
    });
  });

  // --- Voucher invariants ---------------------------------------------------

  describe("Voucher self-inconsistency", function () {
    async function attempt(escrow, seller, buyer, overrides) {
      const { voucher, sig } = await signVoucher(escrow, seller, overrides);
      return escrow.connect(buyer).openTrade(ethers.id(`inv-${Math.random()}`), voucher, sig, 100_000000n);
    }

    it("rejects maxAmount == 0", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      await expect(
        attempt(escrow, seller, buyer, { token: await usdt.getAddress(), maxAmount: 0n })
      ).to.be.revertedWith("Voucher maxAmount=0");
    });

    it("rejects maxPerTrade > maxAmount", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      await expect(
        attempt(escrow, seller, buyer, {
          token: await usdt.getAddress(),
          maxAmount: 100n,
          maxPerTrade: 200n,
        })
      ).to.be.revertedWith("Voucher: maxPerTrade > maxAmount");
    });

    it("rejects minPerTrade > maxPerTrade", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      await expect(
        attempt(escrow, seller, buyer, {
          token: await usdt.getAddress(),
          maxAmount: 1000_000000n,
          minPerTrade: 500_000000n,
          maxPerTrade: 100_000000n,
        })
      ).to.be.revertedWith("Voucher: minPerTrade > maxPerTrade");
    });

    it("accepts sentinel-zero bounds (min=0 or max=0 disables that bound)", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      await expect(
        attempt(escrow, seller, buyer, {
          token: await usdt.getAddress(),
          maxAmount: 1000_000000n,
          minPerTrade: 0n,
          maxPerTrade: 0n,
        })
      ).to.not.be.reverted;
    });
  });

  // --- Amount overflow guard -------------------------------------------------

  describe("uint96 amount overflow guard", function () {
    it("rejects amount > type(uint96).max", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const big = 1n << 96n; // 2**96
      await depositUsdt(escrow, usdt, seller, 100_000000n); // small deposit; guard runs before balance check
      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: big + 1n,
      });
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("t-of"), voucher, sig, big)
      ).to.be.revertedWith("Amount overflow");
    });
  });

  // --- Pause matrix ----------------------------------------------------------

  describe("Pause matrix", function () {
    async function setupForPause() {
      const fx = await loadFixture(deployFixture);
      await depositUsdt(fx.escrow, fx.usdt, fx.seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(fx.escrow, fx.seller, {
        token: await fx.usdt.getAddress(),
      });
      const tradeId = ethers.id(`pm-${Math.random()}`);
      await fx.escrow.connect(fx.buyer).openTrade(tradeId, voucher, sig, 1000_000000n);
      return { ...fx, tradeId, voucher, sig };
    }

    it("paused: deposit / openTrade / markPaid / release / adminRelease / raiseDispute revert", async function () {
      const { escrow, usdt, owner, seller, buyer, operator, tradeId, voucher, sig } =
        await setupForPause();
      await escrow.connect(owner).pause();

      await expect(
        escrow.connect(seller).deposit(await usdt.getAddress(), 1n)
      ).to.be.reverted;
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("pm-blocked"), voucher, sig, 1n)
      ).to.be.reverted;
      await expect(escrow.connect(buyer).markPaid(tradeId)).to.be.reverted;
      await expect(escrow.connect(seller).release(tradeId)).to.be.reverted;
      await expect(escrow.connect(operator).adminRelease(tradeId)).to.be.reverted;
      await expect(escrow.connect(buyer).raiseDispute(tradeId)).to.be.reverted;
    });

    it("paused recovery paths still work: withdraw / cancelByTimeout / adminResolve / cancelVoucher", async function () {
      const { escrow, usdt, owner, seller, buyer, operator, tradeId, voucher } = await setupForPause();
      await escrow.connect(buyer).raiseDispute(tradeId); // dispute BEFORE pause
      await escrow.connect(owner).pause();

      // adminResolve allowed while paused
      await expect(
        escrow.connect(operator).adminResolve(tradeId, false)
      ).to.not.be.reverted;
      // withdraw allowed while paused
      await expect(
        escrow.connect(seller).withdraw(await usdt.getAddress(), 1_000000n)
      ).to.not.be.reverted;
      // cancelVoucher allowed while paused (off-chain housekeeping)
      await expect(escrow.connect(seller).cancelVoucher(voucher)).to.not.be.reverted;
    });

    it("cancelByTimeout works while paused (recovery)", async function () {
      const { escrow, owner, buyer, tradeId } = await setupForPause();
      // tradeId is Pending; advance past timeout and pause
      await time.increase(THIRTY_MIN + 1);
      await escrow.connect(owner).pause();
      await expect(escrow.cancelByTimeout(tradeId)).to.not.be.reverted;
    });
  });

  // --- Token delisting -------------------------------------------------------

  describe("Token delisted mid-flight", function () {
    it("settles existing trades but blocks new deposits/openTrade", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const tok = await usdt.getAddress();
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, { token: tok });
      const tradeId = ethers.id("delist-1");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 1000_000000n);

      // Delist after trade is in-flight
      await escrow.connect(owner).setTokenAcceptance(tok, false);

      // Existing trade can still settle
      await expect(escrow.connect(seller).release(tradeId)).to.not.be.reverted;
      // Seller can still withdraw remaining available
      await expect(escrow.connect(seller).withdraw(tok, 1n)).to.not.be.reverted;
      // But new deposit / openTrade are blocked
      await usdt.connect(seller).approve(await escrow.getAddress(), 1n);
      await expect(escrow.connect(seller).deposit(tok, 1n)).to.be.revertedWith("Token not accepted");
      const { voucher: v2, sig: sig2 } = await signVoucher(escrow, seller, { token: tok });
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("delist-2"), v2, sig2, 100_000000n)
      ).to.be.revertedWith("Token not accepted");
    });
  });

  // --- Zero/dust fee math ----------------------------------------------------

  describe("Fee rounding", function () {
    it("amount=199 yields fee=0 and pays buyer the full amount", async function () {
      const { escrow, usdt, seller, buyer, system } = await loadFixture(deployFixture);
      await depositUsdt(escrow, usdt, seller, 5000_000000n);
      const { voucher, sig } = await signVoucher(escrow, seller, {
        token: await usdt.getAddress(),
        maxAmount: 1000n,
        minPerTrade: 0n,
        maxPerTrade: 0n,
      });
      const tradeId = ethers.id("dust");
      await escrow.connect(buyer).openTrade(tradeId, voucher, sig, 199n);

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);
      await expect(escrow.connect(seller).release(tradeId))
        .to.emit(escrow, "TradeReleased").withArgs(tradeId, buyer.address, 199n, 0n);
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + 199n);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore); // no fee
    });
  });

  // --- Admin config edge cases ----------------------------------------------

  describe("Admin config edge cases", function () {
    it("setSystemWallet rejects zero address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).setSystemWallet(ethers.ZeroAddress)
      ).to.be.revertedWith("Invalid system wallet");
    });

    it("removeAdmin rejects non-admin", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).removeAdmin(other.address)
      ).to.be.revertedWith("Not admin");
    });

    it("setMcgpToken rejects zero address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).setMcgpToken(ethers.ZeroAddress)
      ).to.be.revertedWith("Invalid MCGP address");
    });
  });
});
