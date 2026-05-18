const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const {
  loadFixture,
  time,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("P2PEscrow", function () {
  const THIRTY_MIN = 30 * 60;
  const ONE_DAY = 24 * 60 * 60;

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

    // Whitelist tokens before setMcgpToken — coupling enforced by contract.
    await escrow.setTokenAcceptance(await usdt.getAddress(), true);
    await escrow.setTokenAcceptance(await mcgp.getAddress(), true);
    await escrow.setMcgpToken(await mcgp.getAddress());
    await escrow.setSystemWallet(system.address);

    // Mint to seller (they post the ad)
    const seedUsdt = 100000_000000n; // 100,000 USDT (6 dec)
    const seedMcgp = ethers.parseEther("100000"); // 100,000 MCGP
    await usdt.mint(seller.address, seedUsdt);
    await mcgp.mint(seller.address, seedMcgp);

    return { escrow, usdt, mcgp, owner, seller, buyer, buyer2, operator, system, other };
  }

  async function postUsdtAd(escrow, usdt, seller, amount = 1000_000000n) {
    const adId = ethers.id(`ad-${Math.random()}`);
    await usdt.connect(seller).approve(await escrow.getAddress(), amount);
    await escrow.connect(seller).createAd(adId, await usdt.getAddress(), amount);
    return { adId, amount };
  }

  async function openUsdtTrade(escrow, adId, buyer, amount) {
    const tradeId = ethers.id(`trade-${Math.random()}`);
    await escrow.connect(buyer).openTrade(tradeId, adId, amount);
    return { tradeId, amount };
  }

  describe("Initialization & config", function () {
    it("seeds owner and initial admins", async function () {
      const { escrow, owner, operator } = await loadFixture(deployFixture);
      expect(await escrow.owner()).to.equal(owner.address);
      expect(await escrow.isAdmin(owner.address)).to.equal(true);
      expect(await escrow.isAdmin(operator.address)).to.equal(true);
    });

    it("sets MCGP token and system wallet", async function () {
      const { escrow, mcgp, system } = await loadFixture(deployFixture);
      expect(await escrow.mcgpToken()).to.equal(await mcgp.getAddress());
      expect(await escrow.systemWallet()).to.equal(system.address);
    });

    it("falls back to owner for system recipient when systemWallet is zero", async function () {
      const [owner, seller, buyer, _operator, _system] = await ethers.getSigners();
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      const escrow = await upgrades.deployProxy(
        P2PEscrow,
        [owner.address, []],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      await escrow.waitForDeployment();
      await escrow.setTokenAcceptance(await usdt.getAddress(), true);
      // systemWallet unset — releases should pay owner.
      await usdt.mint(seller.address, 1000_000000n);
      await usdt.connect(seller).approve(await escrow.getAddress(), 1000_000000n);
      const adId = ethers.id("fallback-ad");
      await escrow.connect(seller).createAd(adId, await usdt.getAddress(), 1000_000000n);
      const tradeId = ethers.id("fallback-trade");
      await escrow.connect(buyer).openTrade(tradeId, adId, 1000_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      const ownerBefore = await usdt.balanceOf(owner.address);
      await escrow.connect(seller).release(tradeId);
      const fee = (1000_000000n * 50n) / 10000n;
      expect(await usdt.balanceOf(owner.address)).to.equal(ownerBefore + fee);
    });
  });

  describe("Ad lifecycle", function () {
    it("createAd pulls tokens and stores ad", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const { adId, amount } = await postUsdtAd(escrow, usdt, seller);
      const ad = await escrow.getAd(adId);
      expect(ad.seller).to.equal(seller.address);
      expect(ad.token).to.equal(await usdt.getAddress());
      expect(ad.remaining).to.equal(amount);
      expect(ad.reserved).to.equal(0n);
      expect(ad.open).to.equal(true);
      expect(await usdt.balanceOf(await escrow.getAddress())).to.equal(amount);
    });

    it("createAd rejects non-whitelisted token", async function () {
      const { escrow, seller } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const bad = await MockERC20.deploy("Bad", "BAD", 6);
      await bad.mint(seller.address, 1000_000000n);
      await bad.connect(seller).approve(await escrow.getAddress(), 1000_000000n);
      await expect(
        escrow.connect(seller).createAd(ethers.id("a"), await bad.getAddress(), 1000_000000n)
      ).to.be.revertedWith("Token not accepted");
    });

    it("createAd rejects duplicate adId", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const adId = ethers.id("dup");
      await usdt.connect(seller).approve(await escrow.getAddress(), 2000_000000n);
      await escrow.connect(seller).createAd(adId, await usdt.getAddress(), 1000_000000n);
      await expect(
        escrow.connect(seller).createAd(adId, await usdt.getAddress(), 1000_000000n)
      ).to.be.revertedWith("Ad already exists");
    });

    it("createAd rejects zero amount", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      await usdt.connect(seller).approve(await escrow.getAddress(), 1000_000000n);
      await expect(
        escrow.connect(seller).createAd(ethers.id("z"), await usdt.getAddress(), 0)
      ).to.be.revertedWith("Amount must be > 0");
    });

    it("closeAd refunds remaining when no trades open", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const { adId, amount } = await postUsdtAd(escrow, usdt, seller);
      const before = await usdt.balanceOf(seller.address);
      await escrow.connect(seller).closeAd(adId);
      expect(await usdt.balanceOf(seller.address)).to.equal(before + amount);
      const ad = await escrow.getAd(adId);
      expect(ad.open).to.equal(false);
      expect(ad.remaining).to.equal(0n);
    });

    it("closeAd reverts when open trades exist", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      await openUsdtTrade(escrow, adId, buyer, 300_000000n);
      await expect(escrow.connect(seller).closeAd(adId)).to.be.revertedWith(
        "Open trades exist"
      );
    });

    it("closeAd rejects non-seller", async function () {
      const { escrow, usdt, seller, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      await expect(escrow.connect(other).closeAd(adId)).to.be.revertedWith(
        "Only seller can close"
      );
    });

    it("emits AdCreated", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const adId = ethers.id("evt-ad");
      await usdt.connect(seller).approve(await escrow.getAddress(), 1000_000000n);
      await expect(
        escrow.connect(seller).createAd(adId, await usdt.getAddress(), 1000_000000n)
      )
        .to.emit(escrow, "AdCreated")
        .withArgs(adId, seller.address, await usdt.getAddress(), 1000_000000n);
    });
  });

  describe("openTrade", function () {
    it("decrements remaining, increments reserved", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      await openUsdtTrade(escrow, adId, buyer, 300_000000n);

      const ad = await escrow.getAd(adId);
      expect(ad.remaining).to.equal(700_000000n);
      expect(ad.reserved).to.equal(300_000000n);
    });

    it("supports multiple trades against same ad", async function () {
      const { escrow, usdt, seller, buyer, buyer2 } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      await openUsdtTrade(escrow, adId, buyer, 400_000000n);
      await openUsdtTrade(escrow, adId, buyer2, 200_000000n);
      const ad = await escrow.getAd(adId);
      expect(ad.remaining).to.equal(400_000000n);
      expect(ad.reserved).to.equal(600_000000n);
    });

    it("rejects amount > remaining", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 100_000000n);
      await expect(
        escrow
          .connect(buyer)
          .openTrade(ethers.id("over"), adId, 200_000000n)
      ).to.be.revertedWith("Exceeds remaining");
    });

    it("rejects seller opening trade on own ad", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      await expect(
        escrow.connect(seller).openTrade(ethers.id("self"), adId, 100_000000n)
      ).to.be.revertedWith("Seller cannot self-buy");
    });

    it("rejects duplicate tradeId", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const tradeId = ethers.id("dup-trade");
      await escrow.connect(buyer).openTrade(tradeId, adId, 100_000000n);
      await expect(
        escrow.connect(buyer).openTrade(tradeId, adId, 100_000000n)
      ).to.be.revertedWith("Trade already exists");
    });

    it("rejects when ad is closed", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      await escrow.connect(seller).closeAd(adId);
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("after-close"), adId, 100_000000n)
      ).to.be.revertedWith("Ad not open");
    });
  });

  describe("Happy path: markPaid → release (USDT, 0.5% fee)", function () {
    it("buyer gets amount - 0.5%, system gets fee", async function () {
      const { escrow, usdt, seller, buyer, system } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 500_000000n);

      await escrow.connect(buyer).markPaid(tradeId);

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);

      await escrow.connect(seller).release(tradeId);

      const fee = (amount * 50n) / 10000n;
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + amount - fee);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);

      const ad = await escrow.getAd(adId);
      expect(ad.reserved).to.equal(0n);
      expect(ad.remaining).to.equal(500_000000n); // unchanged by release

      const tr = await escrow.getTrade(tradeId);
      expect(tr.status).to.equal(3n); // Released
    });

    it("MCGP trade pays zero fee", async function () {
      const { escrow, mcgp, seller, buyer, system } = await loadFixture(deployFixture);
      const adId = ethers.id("mcgp-ad");
      const amount = ethers.parseEther("100");
      await mcgp.connect(seller).approve(await escrow.getAddress(), amount);
      await escrow.connect(seller).createAd(adId, await mcgp.getAddress(), amount);

      const tradeId = ethers.id("mcgp-trade");
      const tradeAmount = ethers.parseEther("50");
      await escrow.connect(buyer).openTrade(tradeId, adId, tradeAmount);
      await escrow.connect(buyer).markPaid(tradeId);

      const buyerBefore = await mcgp.balanceOf(buyer.address);
      const systemBefore = await mcgp.balanceOf(system.address);

      await escrow.connect(seller).release(tradeId);

      expect(await mcgp.balanceOf(buyer.address)).to.equal(buyerBefore + tradeAmount);
      expect(await mcgp.balanceOf(system.address)).to.equal(systemBefore);
    });

    it("non-seller cannot release", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(
        escrow.connect(other).release(tradeId)
      ).to.be.revertedWith("Only seller");
    });

    it("release reverts before markPaid", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await expect(escrow.connect(seller).release(tradeId)).to.be.revertedWith("Not paid");
    });

    it("double-release reverts", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(seller).release(tradeId);
      await expect(escrow.connect(seller).release(tradeId)).to.be.revertedWith("Not paid");
    });

    it("emits TradeReleased and TradePaid", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await expect(escrow.connect(buyer).markPaid(tradeId))
        .to.emit(escrow, "TradePaid").withArgs(tradeId);
      const fee = (amount * 50n) / 10000n;
      await expect(escrow.connect(seller).release(tradeId))
        .to.emit(escrow, "TradeReleased")
        .withArgs(tradeId, buyer.address, amount - fee, fee);
    });
  });

  describe("markPaid", function () {
    it("only buyer can mark paid", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await expect(escrow.connect(other).markPaid(tradeId)).to.be.revertedWith("Only buyer");
      await expect(escrow.connect(seller).markPaid(tradeId)).to.be.revertedWith("Only buyer");
    });

    it("can only mark from Pending", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(escrow.connect(buyer).markPaid(tradeId)).to.be.revertedWith("Not pending");
    });
  });

  describe("cancelByTimeout", function () {
    it("anyone can cancel after 30 min if pending", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 400_000000n);

      await time.increase(THIRTY_MIN + 1);

      await escrow.connect(other).cancelByTimeout(tradeId);

      const ad = await escrow.getAd(adId);
      expect(ad.reserved).to.equal(0n);
      expect(ad.remaining).to.equal(1000_000000n); // restored

      const tr = await escrow.getTrade(tradeId);
      expect(tr.status).to.equal(4n); // Cancelled

      expect(amount).to.equal(400_000000n);
    });

    it("reverts before timeout", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await expect(
        escrow.connect(other).cancelByTimeout(tradeId)
      ).to.be.revertedWith("Timeout not elapsed");
    });

    it("reverts if trade already Paid", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await time.increase(THIRTY_MIN + 1);
      await expect(
        escrow.connect(buyer).cancelByTimeout(tradeId)
      ).to.be.revertedWith("Not pending");
    });
  });

  describe("Dispute & adminResolve", function () {
    it("buyer can raise dispute after paying; admin resolves to buyer", async function () {
      const { escrow, usdt, owner, seller, buyer, system } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 400_000000n);
      await escrow.connect(buyer).markPaid(tradeId);

      await escrow.connect(buyer).raiseDispute(tradeId);
      const trBefore = await escrow.getTrade(tradeId);
      expect(trBefore.status).to.equal(5n); // Disputed

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);

      await escrow.connect(owner).adminResolve(tradeId, true);

      const fee = (amount * 50n) / 10000n;
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + amount - fee);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);

      const ad = await escrow.getAd(adId);
      expect(ad.reserved).to.equal(0n);

      const tr = await escrow.getTrade(tradeId);
      expect(tr.status).to.equal(6n); // Resolved
    });

    it("seller can raise dispute; admin resolves to seller (returns to ad)", async function () {
      const { escrow, usdt, operator, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 400_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(seller).raiseDispute(tradeId);

      const buyerBefore = await usdt.balanceOf(buyer.address);

      await escrow.connect(operator).adminResolve(tradeId, false);

      // Buyer gets nothing, funds go back into ad
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore);
      const ad = await escrow.getAd(adId);
      expect(ad.reserved).to.equal(0n);
      expect(ad.remaining).to.equal(1000_000000n);
    });

    it("non-party cannot raise dispute", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(escrow.connect(other).raiseDispute(tradeId)).to.be.revertedWith("Not a party");
    });

    it("cannot raise dispute before paid", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await expect(escrow.connect(buyer).raiseDispute(tradeId)).to.be.revertedWith("Not paid");
    });

    it("non-admin cannot adminResolve", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(buyer).raiseDispute(tradeId);
      await expect(
        escrow.connect(other).adminResolve(tradeId, true)
      ).to.be.revertedWith("Not admin");
    });

    it("adminResolve reverts when not disputed", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(
        escrow.connect(owner).adminResolve(tradeId, true)
      ).to.be.revertedWith("Not disputed");
    });
  });

  describe("adminRelease (webhook path)", function () {
    it("admin can release a Paid trade", async function () {
      const { escrow, usdt, operator, seller, buyer, system } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const systemBefore = await usdt.balanceOf(system.address);

      await escrow.connect(operator).adminRelease(tradeId);

      const fee = (amount * 50n) / 10000n;
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + amount - fee);
      expect(await usdt.balanceOf(system.address)).to.equal(systemBefore + fee);
    });

    it("non-admin cannot adminRelease", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(escrow.connect(other).adminRelease(tradeId)).to.be.revertedWith("Not admin");
    });

    it("adminRelease reverts when not Paid", async function () {
      const { escrow, usdt, operator, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await expect(escrow.connect(operator).adminRelease(tradeId)).to.be.revertedWith("Not paid");
    });
  });

  describe("Pause", function () {
    it("createAd reverts when paused", async function () {
      const { escrow, usdt, owner, seller } = await loadFixture(deployFixture);
      await escrow.connect(owner).pause();
      await usdt.connect(seller).approve(await escrow.getAddress(), 1000_000000n);
      await expect(
        escrow.connect(seller).createAd(ethers.id("p"), await usdt.getAddress(), 1000_000000n)
      ).to.be.revertedWithCustomError(escrow, "EnforcedPause");
    });

    it("adminResolve works while paused", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(buyer).raiseDispute(tradeId);

      await escrow.connect(owner).pause();
      await expect(escrow.connect(owner).adminResolve(tradeId, true)).to.not.be.reverted;
    });
  });

  describe("Money invariants", function () {
    it("contract balance equals remaining + reserved across full lifecycle", async function () {
      const { escrow, usdt, seller, buyer, buyer2 } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const addr = await escrow.getAddress();

      // Initial: balance = 1000, remaining = 1000, reserved = 0
      expect(await usdt.balanceOf(addr)).to.equal(1000_000000n);

      // Open two trades
      const { tradeId: t1 } = await openUsdtTrade(escrow, adId, buyer, 300_000000n);
      await openUsdtTrade(escrow, adId, buyer2, 400_000000n);

      let ad = await escrow.getAd(adId);
      expect(await usdt.balanceOf(addr)).to.equal(ad.remaining + ad.reserved);

      // Cancel one via timeout
      await time.increase(THIRTY_MIN + 1);
      await escrow.connect(buyer).cancelByTimeout(t1);
      ad = await escrow.getAd(adId);
      expect(await usdt.balanceOf(addr)).to.equal(ad.remaining + ad.reserved);
    });

    it("release moves funds out without affecting ad.remaining", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 400_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(seller).release(tradeId);

      const ad = await escrow.getAd(adId);
      expect(ad.remaining).to.equal(600_000000n);
      expect(ad.reserved).to.equal(0n);
      expect(await usdt.balanceOf(await escrow.getAddress())).to.equal(600_000000n);
    });
  });

  describe("Admin role", function () {
    it("addAdmin reverts for zero / already-admin / non-owner", async function () {
      const { escrow, owner, other, operator } = await loadFixture(deployFixture);
      await expect(escrow.connect(owner).addAdmin(ethers.ZeroAddress)).to.be.revertedWith("Zero address");
      await expect(escrow.connect(owner).addAdmin(operator.address)).to.be.revertedWith("Already admin");
      await expect(escrow.connect(other).addAdmin(other.address)).to.be.revertedWithCustomError(
        escrow, "OwnableUnauthorizedAccount"
      );
    });

    it("removeAdmin works and revokes", async function () {
      const { escrow, owner, operator } = await loadFixture(deployFixture);
      await escrow.connect(owner).removeAdmin(operator.address);
      expect(await escrow.isAdmin(operator.address)).to.equal(false);
    });

    it("owner is unrevokable as admin", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(escrow.connect(owner).removeAdmin(owner.address)).to.be.revertedWith("Not admin");
      expect(await escrow.isAdmin(owner.address)).to.equal(true);
    });
  });

  describe("sweepExcess", function () {
    it("owner can sweep accidentally transferred tokens", async function () {
      const { escrow, usdt, owner } = await loadFixture(deployFixture);
      await usdt.mint(await escrow.getAddress(), 1000n);
      const before = await usdt.balanceOf(owner.address);
      await escrow.connect(owner).sweepExcess(await usdt.getAddress(), 1000n);
      expect(await usdt.balanceOf(owner.address)).to.equal(before + 1000n);
    });

    it("non-owner cannot sweep", async function () {
      const { escrow, usdt, other } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(other).sweepExcess(await usdt.getAddress(), 1000n)
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
    });

    it("sweepExcess rejects zero token address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).sweepExcess(ethers.ZeroAddress, 1n)
      ).to.be.revertedWith("Invalid token");
    });
  });

  // ──────────────────────────────────────────────────────────────────────
  // Gate-1 review additions
  // ──────────────────────────────────────────────────────────────────────

  describe("Setters: access control & validation", function () {
    it("setSystemWallet rejects zero address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).setSystemWallet(ethers.ZeroAddress)
      ).to.be.revertedWith("Invalid system wallet");
    });

    it("setSystemWallet rejects non-owner", async function () {
      const { escrow, other } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(other).setSystemWallet(other.address)
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
    });

    it("setSystemWallet emits SystemWalletUpdated", async function () {
      const { escrow, owner, system, other } = await loadFixture(deployFixture);
      await expect(escrow.connect(owner).setSystemWallet(other.address))
        .to.emit(escrow, "SystemWalletUpdated")
        .withArgs(system.address, other.address);
    });

    it("setMcgpToken rejects zero address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).setMcgpToken(ethers.ZeroAddress)
      ).to.be.revertedWith("Invalid MCGP address");
    });

    it("setMcgpToken rejects non-owner", async function () {
      const { escrow, mcgp, other } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(other).setMcgpToken(await mcgp.getAddress())
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
    });

    it("setMcgpToken requires token to be whitelisted first", async function () {
      // H2 from review: prevents granting fee discount to non-accepted addresses.
      const { escrow, owner } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const stranger = await MockERC20.deploy("Stranger", "STR", 18);
      await expect(
        escrow.connect(owner).setMcgpToken(await stranger.getAddress())
      ).to.be.revertedWith("MCGP must be accepted first");
    });

    it("setMcgpToken emits McgpTokenUpdated", async function () {
      const { escrow, owner, mcgp } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const newMcgp = await MockERC20.deploy("MCGP2", "MCGP2", 18);
      await escrow.connect(owner).setTokenAcceptance(await newMcgp.getAddress(), true);
      await expect(escrow.connect(owner).setMcgpToken(await newMcgp.getAddress()))
        .to.emit(escrow, "McgpTokenUpdated")
        .withArgs(await mcgp.getAddress(), await newMcgp.getAddress());
    });

    it("setTokenAcceptance rejects zero address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).setTokenAcceptance(ethers.ZeroAddress, true)
      ).to.be.revertedWith("Invalid token");
    });

    it("setTokenAcceptance rejects non-owner", async function () {
      const { escrow, usdt, other } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(other).setTokenAcceptance(await usdt.getAddress(), false)
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
    });

    it("setTokenAcceptance round-trips with event", async function () {
      const { escrow, owner, usdt } = await loadFixture(deployFixture);
      const addr = await usdt.getAddress();
      await expect(escrow.connect(owner).setTokenAcceptance(addr, false))
        .to.emit(escrow, "TokenAcceptanceUpdated")
        .withArgs(addr, false);
      expect(await escrow.acceptedTokens(addr)).to.equal(false);
    });
  });

  describe("Pause guard surface", function () {
    async function pausedFixture() {
      const f = await deployFixture();
      await f.escrow.connect(f.owner).pause();
      return f;
    }

    it("openTrade reverts when paused", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      await escrow.connect(owner).pause();
      await expect(
        escrow.connect(buyer).openTrade(ethers.id("p-ot"), adId, 100_000000n)
      ).to.be.revertedWithCustomError(escrow, "EnforcedPause");
    });

    it("markPaid reverts when paused", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(owner).pause();
      await expect(escrow.connect(buyer).markPaid(tradeId)).to.be.revertedWithCustomError(
        escrow, "EnforcedPause"
      );
    });

    it("release reverts when paused", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(owner).pause();
      await expect(escrow.connect(seller).release(tradeId)).to.be.revertedWithCustomError(
        escrow, "EnforcedPause"
      );
    });

    it("adminRelease reverts when paused", async function () {
      const { escrow, usdt, owner, operator, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(owner).pause();
      await expect(escrow.connect(operator).adminRelease(tradeId)).to.be.revertedWithCustomError(
        escrow, "EnforcedPause"
      );
    });

    it("raiseDispute reverts when paused", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(owner).pause();
      await expect(escrow.connect(buyer).raiseDispute(tradeId)).to.be.revertedWithCustomError(
        escrow, "EnforcedPause"
      );
    });

    it("closeAd reverts when paused", async function () {
      const { escrow, usdt, owner, seller } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      await escrow.connect(owner).pause();
      await expect(escrow.connect(seller).closeAd(adId)).to.be.revertedWithCustomError(
        escrow, "EnforcedPause"
      );
    });

    it("cancelByTimeout WORKS when paused (intentional escape hatch)", async function () {
      // M1 from review: refund-to-pool must be available during incident pause.
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 400_000000n);
      await time.increase(THIRTY_MIN + 1);
      await escrow.connect(owner).pause();
      await expect(escrow.connect(buyer).cancelByTimeout(tradeId)).to.not.be.reverted;
    });
  });

  describe("Boundary / overflow", function () {
    it("createAd accepts amount == type(uint96).max", async function () {
      const { escrow, seller, owner } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const big = await MockERC20.deploy("Big", "BIG", 18);
      await escrow.connect(owner).setTokenAcceptance(await big.getAddress(), true);
      const max = (1n << 96n) - 1n;
      await big.mint(seller.address, max);
      await big.connect(seller).approve(await escrow.getAddress(), max);
      await escrow.connect(seller).createAd(ethers.id("max"), await big.getAddress(), max);
      const ad = await escrow.getAd(ethers.id("max"));
      expect(ad.remaining).to.equal(max);
    });

    it("createAd rejects amount > type(uint96).max", async function () {
      const { escrow, seller, owner } = await loadFixture(deployFixture);
      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const big = await MockERC20.deploy("Big", "BIG", 18);
      await escrow.connect(owner).setTokenAcceptance(await big.getAddress(), true);
      const tooBig = (1n << 96n);
      await big.mint(seller.address, tooBig);
      await big.connect(seller).approve(await escrow.getAddress(), tooBig);
      await expect(
        escrow.connect(seller).createAd(ethers.id("over"), await big.getAddress(), tooBig)
      ).to.be.revertedWith("Amount overflow");
    });

    it("openTrade reverts when ad fully reserved", async function () {
      const { escrow, usdt, seller, buyer, buyer2 } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      await openUsdtTrade(escrow, adId, buyer, 1000_000000n);
      await expect(
        escrow.connect(buyer2).openTrade(ethers.id("after-drain"), adId, 1n)
      ).to.be.revertedWith("Exceeds remaining");
    });

    it("release of 1-wei trade rounds fee to 0 and pays buyer 1", async function () {
      const { escrow, usdt, seller, buyer, system } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 100n);
      const tradeId = ethers.id("dust");
      await escrow.connect(buyer).openTrade(tradeId, adId, 1n);
      await escrow.connect(buyer).markPaid(tradeId);

      const buyerBefore = await usdt.balanceOf(buyer.address);
      const sysBefore = await usdt.balanceOf(system.address);

      await expect(escrow.connect(seller).release(tradeId))
        .to.emit(escrow, "TradeReleased")
        .withArgs(tradeId, buyer.address, 1n, 0n);

      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + 1n);
      expect(await usdt.balanceOf(system.address)).to.equal(sysBefore); // 0.5% of 1 rounds to 0
    });

    it("cancelByTimeout succeeds at exactly TIMEOUT", async function () {
      // Exact-boundary regression guard: `>=` semantics on line 299.
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);

      const tr = await escrow.getTrade(tradeId);
      const exactDeadline = Number(tr.createdAt) + THIRTY_MIN;
      await time.increaseTo(exactDeadline);

      await expect(escrow.connect(buyer).cancelByTimeout(tradeId)).to.not.be.reverted;
    });
  });

  describe("closeAd after partial activity", function () {
    it("closeAd refunds correct remainder after partial release", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 300_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(seller).release(tradeId);

      const sellerBefore = await usdt.balanceOf(seller.address);
      await escrow.connect(seller).closeAd(adId);

      // Seller gets the unreleased 700; the released 300 is gone.
      expect(await usdt.balanceOf(seller.address)).to.equal(sellerBefore + 700_000000n);

      const ad = await escrow.getAd(adId);
      expect(ad.open).to.equal(false);
      expect(ad.remaining).to.equal(0n);
      // Contract balance: nothing left for this ad. (Other ads unaffected.)
      expect(await usdt.balanceOf(await escrow.getAddress())).to.equal(0n);
    });

    it("closeAd refunds correct remainder after a cancelled-by-timeout trade", async function () {
      const { escrow, usdt, seller, buyer, other } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 400_000000n);
      await time.increase(THIRTY_MIN + 1);
      await escrow.connect(other).cancelByTimeout(tradeId);

      const sellerBefore = await usdt.balanceOf(seller.address);
      await escrow.connect(seller).closeAd(adId);
      // The cancelled 400 returned to remaining; closeAd refunds the full 1000.
      expect(await usdt.balanceOf(seller.address)).to.equal(sellerBefore + 1000_000000n);
    });
  });

  describe("Event coverage", function () {
    it("emits AdClosed with refunded amount", async function () {
      const { escrow, usdt, seller } = await loadFixture(deployFixture);
      const { adId, amount } = await postUsdtAd(escrow, usdt, seller);
      await expect(escrow.connect(seller).closeAd(adId))
        .to.emit(escrow, "AdClosed")
        .withArgs(adId, amount);
    });

    it("emits TradeOpened on openTrade", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const tradeId = ethers.id("evt-open");
      await expect(escrow.connect(buyer).openTrade(tradeId, adId, 100_000000n))
        .to.emit(escrow, "TradeOpened")
        .withArgs(tradeId, adId, buyer.address, 100_000000n);
    });

    it("emits TradeCancelled with returned amount", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 250_000000n);
      await time.increase(THIRTY_MIN + 1);
      await expect(escrow.connect(buyer).cancelByTimeout(tradeId))
        .to.emit(escrow, "TradeCancelled")
        .withArgs(tradeId, amount);
    });

    it("emits TradeDisputed with the raising party", async function () {
      const { escrow, usdt, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await expect(escrow.connect(buyer).raiseDispute(tradeId))
        .to.emit(escrow, "TradeDisputed")
        .withArgs(tradeId, buyer.address);
    });

    it("emits TradeResolved(true) with fee split on dispute → buyer", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId, amount } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(buyer).raiseDispute(tradeId);
      const fee = (amount * 50n) / 10000n;
      await expect(escrow.connect(owner).adminResolve(tradeId, true))
        .to.emit(escrow, "TradeResolved")
        .withArgs(tradeId, true, amount - fee, fee);
    });

    it("emits TradeResolved(false, 0, 0) on dispute → seller", async function () {
      const { escrow, usdt, owner, seller, buyer } = await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller);
      const { tradeId } = await openUsdtTrade(escrow, adId, buyer, 100_000000n);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(buyer).raiseDispute(tradeId);
      await expect(escrow.connect(owner).adminResolve(tradeId, false))
        .to.emit(escrow, "TradeResolved")
        .withArgs(tradeId, false, 0n, 0n);
    });

    it("emits AdminAdded / AdminRemoved", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await expect(escrow.connect(owner).addAdmin(other.address))
        .to.emit(escrow, "AdminAdded").withArgs(other.address);
      await expect(escrow.connect(owner).removeAdmin(other.address))
        .to.emit(escrow, "AdminRemoved").withArgs(other.address);
    });

    it("emits ExcessSwept on sweepExcess", async function () {
      const { escrow, usdt, owner } = await loadFixture(deployFixture);
      await usdt.mint(await escrow.getAddress(), 1000n);
      await expect(escrow.connect(owner).sweepExcess(await usdt.getAddress(), 1000n))
        .to.emit(escrow, "ExcessSwept")
        .withArgs(await usdt.getAddress(), 1000n);
    });
  });

  describe("MCGP dispute path (0% fee)", function () {
    it("adminResolve(true) on MCGP trade pays buyer full amount, zero to system", async function () {
      const { escrow, mcgp, owner, seller, buyer, system } = await loadFixture(deployFixture);
      const adId = ethers.id("mcgp-disp-ad");
      const amount = ethers.parseEther("100");
      await mcgp.connect(seller).approve(await escrow.getAddress(), amount);
      await escrow.connect(seller).createAd(adId, await mcgp.getAddress(), amount);

      const tradeId = ethers.id("mcgp-disp");
      const tradeAmount = ethers.parseEther("40");
      await escrow.connect(buyer).openTrade(tradeId, adId, tradeAmount);
      await escrow.connect(buyer).markPaid(tradeId);
      await escrow.connect(buyer).raiseDispute(tradeId);

      const buyerBefore = await mcgp.balanceOf(buyer.address);
      const sysBefore = await mcgp.balanceOf(system.address);

      await expect(escrow.connect(owner).adminResolve(tradeId, true))
        .to.emit(escrow, "TradeResolved")
        .withArgs(tradeId, true, tradeAmount, 0n);

      expect(await mcgp.balanceOf(buyer.address)).to.equal(buyerBefore + tradeAmount);
      expect(await mcgp.balanceOf(system.address)).to.equal(sysBefore);
    });
  });

  describe("Multiple disputes on same ad", function () {
    it("two disputed trades on same ad resolve independently", async function () {
      const { escrow, usdt, owner, seller, buyer, buyer2, system } =
        await loadFixture(deployFixture);
      const { adId } = await postUsdtAd(escrow, usdt, seller, 1000_000000n);

      const { tradeId: tA } = await openUsdtTrade(escrow, adId, buyer, 300_000000n);
      const { tradeId: tB } = await openUsdtTrade(escrow, adId, buyer2, 400_000000n);

      await escrow.connect(buyer).markPaid(tA);
      await escrow.connect(buyer2).markPaid(tB);
      await escrow.connect(buyer).raiseDispute(tA);
      await escrow.connect(seller).raiseDispute(tB);

      const sysBefore = await usdt.balanceOf(system.address);
      const buyerBefore = await usdt.balanceOf(buyer.address);

      await escrow.connect(owner).adminResolve(tA, true);   // to buyer
      await escrow.connect(owner).adminResolve(tB, false);  // back to ad

      // Buyer A: 300 - 0.5% = 298.5. Fee 1.5 to system.
      const feeA = (300_000000n * 50n) / 10000n;
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore + 300_000000n - feeA);
      expect(await usdt.balanceOf(system.address)).to.equal(sysBefore + feeA);

      const ad = await escrow.getAd(adId);
      // remaining: 300 (initial untouched) + 400 (returned from B) = 700
      expect(ad.remaining).to.equal(700_000000n);
      expect(ad.reserved).to.equal(0n);
    });
  });

  describe("Multi-token money invariant", function () {
    it("contract balances per token match Σ(remaining + reserved) across mixed lifecycle", async function () {
      const { escrow, usdt, mcgp, owner, seller, buyer, buyer2, system } =
        await loadFixture(deployFixture);

      // USDT ad
      const usdtAdId = ethers.id("inv-usdt");
      await usdt.connect(seller).approve(await escrow.getAddress(), 1000_000000n);
      await escrow.connect(seller).createAd(usdtAdId, await usdt.getAddress(), 1000_000000n);

      // MCGP ad
      const mcgpAdId = ethers.id("inv-mcgp");
      const mcgpAmount = ethers.parseEther("100");
      await mcgp.connect(seller).approve(await escrow.getAddress(), mcgpAmount);
      await escrow.connect(seller).createAd(mcgpAdId, await mcgp.getAddress(), mcgpAmount);

      // Open 3 trades on each
      const usdtTrades = [];
      for (let i = 0; i < 3; i++) {
        const tradeId = ethers.id(`u-${i}`);
        usdtTrades.push(tradeId);
        await escrow.connect(buyer).openTrade(tradeId, usdtAdId, 200_000000n);
      }
      const mcgpTrades = [];
      for (let i = 0; i < 3; i++) {
        const tradeId = ethers.id(`m-${i}`);
        mcgpTrades.push(tradeId);
        await escrow.connect(buyer2).openTrade(tradeId, mcgpAdId, ethers.parseEther("20"));
      }

      // Release first of each
      await escrow.connect(buyer).markPaid(usdtTrades[0]);
      await escrow.connect(seller).release(usdtTrades[0]);
      await escrow.connect(buyer2).markPaid(mcgpTrades[0]);
      await escrow.connect(seller).release(mcgpTrades[0]);

      // Cancel-by-timeout second of each
      await time.increase(THIRTY_MIN + 1);
      await escrow.connect(seller).cancelByTimeout(usdtTrades[1]);
      await escrow.connect(seller).cancelByTimeout(mcgpTrades[1]);

      // Dispute → admin-to-seller (back to pool) for third of each
      await escrow.connect(buyer).markPaid(usdtTrades[2]);
      await escrow.connect(seller).raiseDispute(usdtTrades[2]);
      await escrow.connect(owner).adminResolve(usdtTrades[2], false);

      await escrow.connect(buyer2).markPaid(mcgpTrades[2]);
      await escrow.connect(seller).raiseDispute(mcgpTrades[2]);
      await escrow.connect(owner).adminResolve(mcgpTrades[2], false);

      // Add some dust then sweep — must not affect invariant
      await usdt.mint(await escrow.getAddress(), 7n);
      await escrow.connect(owner).sweepExcess(await usdt.getAddress(), 7n);

      // Close USDT ad
      await escrow.connect(seller).closeAd(usdtAdId);

      const usdtAd = await escrow.getAd(usdtAdId);
      const mcgpAd = await escrow.getAd(mcgpAdId);

      // For each token, contract balance equals Σ(remaining + reserved)
      expect(await usdt.balanceOf(await escrow.getAddress())).to.equal(
        usdtAd.remaining + usdtAd.reserved
      );
      expect(await mcgp.balanceOf(await escrow.getAddress())).to.equal(
        mcgpAd.remaining + mcgpAd.reserved
      );

      // Per-trade status invariant: catches "accounting right, status wrong" bugs.
      const RELEASED = 3n, CANCELLED = 4n, RESOLVED = 6n;
      const usdtT0 = await escrow.getTrade(usdtTrades[0]);
      const usdtT1 = await escrow.getTrade(usdtTrades[1]);
      const usdtT2 = await escrow.getTrade(usdtTrades[2]);
      const mcgpT0 = await escrow.getTrade(mcgpTrades[0]);
      const mcgpT1 = await escrow.getTrade(mcgpTrades[1]);
      const mcgpT2 = await escrow.getTrade(mcgpTrades[2]);
      expect(usdtT0.status).to.equal(RELEASED);
      expect(usdtT1.status).to.equal(CANCELLED);
      expect(usdtT2.status).to.equal(RESOLVED);
      expect(mcgpT0.status).to.equal(RELEASED);
      expect(mcgpT1.status).to.equal(CANCELLED);
      expect(mcgpT2.status).to.equal(RESOLVED);

      // System-wallet balance: exactly one USDT release (200 USDT, 0.5% fee = 1 USDT).
      // MCGP release pays zero fee — system wallet should never see MCGP.
      const expectedUsdtFee = (200_000000n * 50n) / 10000n;
      expect(await usdt.balanceOf(system.address)).to.equal(expectedUsdtFee);
      expect(await mcgp.balanceOf(system.address)).to.equal(0n);
    });
  });

  describe("Reentrancy guard", function () {
    it("malicious token cannot re-enter release", async function () {
      const [owner, seller, buyer, system] = await ethers.getSigners();
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      const escrow = await upgrades.deployProxy(
        P2PEscrow,
        [owner.address, []],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      await escrow.waitForDeployment();
      await escrow.setSystemWallet(system.address);

      const ReentrantERC20 = await ethers.getContractFactory("ReentrantERC20");
      const evil = await ReentrantERC20.deploy("Evil", "EVL", 6);
      await escrow.setTokenAcceptance(await evil.getAddress(), true);

      await evil.mint(seller.address, 1000_000000n);
      await evil.connect(seller).approve(await escrow.getAddress(), 1000_000000n);

      const adId = ethers.id("evil-ad");
      await escrow.connect(seller).createAd(adId, await evil.getAddress(), 1000_000000n);

      const tradeId = ethers.id("evil-trade");
      await escrow.connect(buyer).openTrade(tradeId, adId, 500_000000n);
      await escrow.connect(buyer).markPaid(tradeId);

      // Arm the attack: when escrow transfers tokens out, the mock will re-enter release(tradeId).
      await evil.configureAttack(await escrow.getAddress(), tradeId);

      await expect(escrow.connect(seller).release(tradeId)).to.be.revertedWithCustomError(
        escrow, "ReentrancyGuardReentrantCall"
      );

      // Trade must remain in Paid state — no funds moved.
      const tr = await escrow.getTrade(tradeId);
      expect(tr.status).to.equal(2n); // Paid
    });
  });

  describe("Upgrade authorization", function () {
    it("only owner can upgrade the proxy", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      const V2 = await ethers.getContractFactory("P2PEscrowV2Test");

      // Non-owner attempt: deploy a new impl via upgrades.upgradeProxy from a non-owner signer.
      await expect(
        upgrades.upgradeProxy(await escrow.getAddress(), V2.connect(other), {
          kind: "uups",
          unsafeAllow: ["constructor", "missing-initializer"],
        })
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");

      // Owner upgrade works and preserves state.
      const upgraded = await upgrades.upgradeProxy(
        await escrow.getAddress(),
        V2.connect(owner),
        { kind: "uups", unsafeAllow: ["constructor", "missing-initializer"] }
      );
      expect(await upgraded.version()).to.equal("v2-test");
      // Existing admin (operator) still admin post-upgrade.
      const { operator } = await loadFixture(deployFixture);
      expect(await upgraded.isAdmin(operator.address)).to.equal(true);
    });
  });

  describe("Initialize edges", function () {
    it("initialize rejects zero address in initialAdmins", async function () {
      const [deployer] = await ethers.getSigners();
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      await expect(
        upgrades.deployProxy(
          P2PEscrow,
          [deployer.address, [ethers.ZeroAddress]],
          { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
        )
      ).to.be.revertedWith("Zero address");
    });

    it("initialize rejects duplicate admin in initialAdmins", async function () {
      const [deployer, op] = await ethers.getSigners();
      const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
      await expect(
        upgrades.deployProxy(
          P2PEscrow,
          [deployer.address, [op.address, op.address]],
          { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
        )
      ).to.be.revertedWith("Already admin");
    });
  });
});
