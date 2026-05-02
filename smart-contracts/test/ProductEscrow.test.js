const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const {
  loadFixture,
  time,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("ProductEscrow", function () {
  const THIRTY_DAYS = 30 * 24 * 60 * 60;
  const THREE_DAYS = 3 * 24 * 60 * 60;

  async function deployFixture() {
    const [owner, buyer, seller, upline, other] = await ethers.getSigners();

    // Deploy 6-decimal stablecoin mock (USDT/USDC)
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);

    // Deploy 18-decimal MCGP token mock
    const mcgp = await MockERC20.deploy("MCGP Token", "MCGP", 18);

    // Deploy escrow contract with owner as system wallet
    const ProductEscrow = await ethers.getContractFactory("ProductEscrow");
    const escrow = await upgrades.deployProxy(
        ProductEscrow,
        [owner.address, []],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
    );
    await escrow.waitForDeployment();

    // Set MCGP token address and whitelist tokens
    await escrow.setMcgpToken(await mcgp.getAddress());
    await escrow.setTokenAcceptance(await usdt.getAddress(), true);
    await escrow.setTokenAcceptance(await mcgp.getAddress(), true);

    // Mint tokens to buyer
    const mintAmount6 = 10000_000000n; // 10,000 USDT (6 decimals)
    const mintAmount18 = ethers.parseEther("10000"); // 10,000 MCGP
    await usdt.mint(buyer.address, mintAmount6);
    await mcgp.mint(buyer.address, mintAmount18);

    return { escrow, usdt, mcgp, owner, buyer, seller, upline, other };
  }

  // Helper to create a standard order
  async function createStandardOrder(escrow, usdt, buyer, seller, upline) {
    const orderId = ethers.id("order-1");
    const productAmount = 1000_000000n; // $1000 USDT
    const shippingAmount = 50_000000n; // $50 USDT
    const platformFee = 100_000000n; // 10% of productAmount = $100

    const totalAmount = productAmount + shippingAmount + platformFee;
    await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);

    await escrow
      .connect(buyer)
      .createOrder(
        orderId,
        seller.address,
        await usdt.getAddress(),
        productAmount,
        shippingAmount,
        upline.address
      );

    return { orderId, productAmount, shippingAmount, platformFee, totalAmount };
  }

  describe("Happy path: create → deliver → confirm → fee splits", function () {
    it("should correctly split fees on receipt confirmation", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, productAmount, shippingAmount, platformFee } =
        await createStandardOrder(escrow, usdt, buyer, seller, upline);

      // Seller marks delivered
      await escrow.connect(seller).markDelivered(orderId);

      // Record balances before confirmation
      const sellerBefore = await usdt.balanceOf(seller.address);
      const ownerBefore = await usdt.balanceOf(owner.address);
      const buyerBefore = await usdt.balanceOf(buyer.address);
      const uplineBefore = await usdt.balanceOf(upline.address);

      // Buyer confirms receipt
      await escrow.connect(buyer).confirmReceipt(orderId);

      // Calculate expected splits
      const buyerCashback = (productAmount * 250n) / 10000n; // 2.5%
      const uplineFee = (productAmount * 250n) / 10000n; // 2.5%
      const systemFee = platformFee - buyerCashback - uplineFee; // remainder
      const sellerAmount = productAmount + shippingAmount;

      expect(await usdt.balanceOf(seller.address)).to.equal(
        sellerBefore + sellerAmount
      );
      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemFee
      );
      expect(await usdt.balanceOf(buyer.address)).to.equal(
        buyerBefore + buyerCashback
      );
      expect(await usdt.balanceOf(upline.address)).to.equal(
        uplineBefore + uplineFee
      );

      // Verify order is resolved
      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
      expect(order.buyerConfirmed).to.be.true;
    });

    it("should handle rounding dust correctly for small amounts", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      const orderId = ethers.id("small-order");
      const productAmount = 33n; // small amount to trigger rounding
      const shippingAmount = 1n;
      // platformFee = 33 * 1000 / 10000 = 3
      const platformFee = 3n;
      const totalAmount = productAmount + shippingAmount + platformFee;

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);
      await escrow
        .connect(buyer)
        .createOrder(
          orderId,
          seller.address,
          await usdt.getAddress(),
          productAmount,
          shippingAmount,
          upline.address
        );

      await escrow.connect(seller).markDelivered(orderId);

      const contractBefore = await usdt.balanceOf(await escrow.getAddress());

      await escrow.connect(buyer).confirmReceipt(orderId);

      // Contract should have 0 balance — no dust locked
      const contractAfter = await usdt.balanceOf(await escrow.getAddress());
      expect(contractAfter).to.equal(0n);
    });
  });

  describe("MCGP 0% fee path", function () {
    it("should have zero platform fee for MCGP token", async function () {
      const { escrow, mcgp, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      const orderId = ethers.id("mcgp-order-1");
      const productAmount = ethers.parseEther("100"); // 100 MCGP
      const shippingAmount = ethers.parseEther("5"); // 5 MCGP

      // For MCGP, platformFee = 0, total = productAmount + shippingAmount
      const totalAmount = productAmount + shippingAmount;
      await mcgp
        .connect(buyer)
        .approve(await escrow.getAddress(), totalAmount);

      await escrow
        .connect(buyer)
        .createOrder(
          orderId,
          seller.address,
          await mcgp.getAddress(),
          productAmount,
          shippingAmount,
          upline.address
        );

      const order = await escrow.orders(orderId);
      expect(order.platformFee).to.equal(0n);

      // Complete the order
      await escrow.connect(seller).markDelivered(orderId);

      const sellerBefore = await mcgp.balanceOf(seller.address);
      const ownerBefore = await mcgp.balanceOf(owner.address);

      await escrow.connect(buyer).confirmReceipt(orderId);

      // Seller gets full productAmount + shippingAmount, no fees
      expect(await mcgp.balanceOf(seller.address)).to.equal(
        sellerBefore + productAmount + shippingAmount
      );
      // Owner gets nothing
      expect(await mcgp.balanceOf(owner.address)).to.equal(ownerBefore);
    });
  });

  describe("30-day autoRefund", function () {
    it("should refund 100% to buyer if seller never delivered", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, totalAmount } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      const buyerBefore = await usdt.balanceOf(buyer.address);

      // Advance time by 30 days
      await time.increase(THIRTY_DAYS);

      // Buyer can call during grace period
      await escrow.connect(buyer).autoRefund(orderId);

      expect(await usdt.balanceOf(buyer.address)).to.equal(
        buyerBefore + totalAmount
      );

      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
    });

    it("should split 90/7/3 if seller delivered but buyer didn't confirm", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, totalAmount } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      // Seller marks delivered
      await escrow.connect(seller).markDelivered(orderId);

      // Record balances
      const buyerBefore = await usdt.balanceOf(buyer.address);
      const ownerBefore = await usdt.balanceOf(owner.address);
      const sellerBefore = await usdt.balanceOf(seller.address);

      // Advance time by 30 days
      await time.increase(THIRTY_DAYS);

      await escrow.connect(buyer).autoRefund(orderId);

      const buyerShare = (totalAmount * 9000n) / 10000n; // 90%
      const systemShare = (totalAmount * 700n) / 10000n; // 7%
      const sellerShare = totalAmount - buyerShare - systemShare; // 3% (remainder)

      expect(await usdt.balanceOf(buyer.address)).to.equal(
        buyerBefore + buyerShare
      );
      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemShare
      );
      expect(await usdt.balanceOf(seller.address)).to.equal(
        sellerBefore + sellerShare
      );
    });

    it("should revert before 30 days", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(
        escrow.connect(buyer).autoRefund(orderId)
      ).to.be.revertedWith("30 days not elapsed");
    });

    it("should restrict to buyer/owner during 3-day grace period", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);

      // Advance to exactly 30 days (within grace period)
      await time.increase(THIRTY_DAYS);

      // Random caller should be rejected during grace period
      await expect(
        escrow.connect(other).autoRefund(orderId)
      ).to.be.revertedWith("Grace period: only buyer or owner");

      // Buyer should succeed
      await escrow.connect(buyer).autoRefund(orderId);
    });

    it("should allow anyone after grace period expires", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      // Advance past 30 days + 3 day grace period
      await time.increase(THIRTY_DAYS + THREE_DAYS);

      // Anyone can call now
      await escrow.connect(other).autoRefund(orderId);

      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
    });
  });

  describe("Buyer refund before seller delivers", function () {
    it("should give 100% instant refund", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, totalAmount } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      const buyerBefore = await usdt.balanceOf(buyer.address);

      await escrow.connect(buyer).requestRefund(orderId);

      expect(await usdt.balanceOf(buyer.address)).to.equal(
        buyerBefore + totalAmount
      );

      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
      expect(order.refundRequested).to.be.true;
    });
  });

  describe("Buyer refund after seller delivers", function () {
    it("should create dispute state, not refund immediately", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);

      const buyerBefore = await usdt.balanceOf(buyer.address);

      await escrow.connect(buyer).requestRefund(orderId);

      // Buyer should NOT have received funds
      expect(await usdt.balanceOf(buyer.address)).to.equal(buyerBefore);

      const order = await escrow.orders(orderId);
      expect(order.refundRequested).to.be.true;
      expect(order.resolved).to.be.false; // Not resolved — dispute
    });

    it("should only allow admin to resolve dispute", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      // Non-owner cannot resolve
      await expect(
        escrow.connect(other).adminResolve(orderId, true)
      ).to.be.revertedWith("Not admin");
    });
  });

  describe("Seller cancels order", function () {
    it("should refund 100% to buyer", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, totalAmount } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      const buyerBefore = await usdt.balanceOf(buyer.address);

      await escrow.connect(seller).cancelOrder(orderId);

      expect(await usdt.balanceOf(buyer.address)).to.equal(
        buyerBefore + totalAmount
      );

      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
    });

    it("should not allow cancel after delivery", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);

      await expect(
        escrow.connect(seller).cancelOrder(orderId)
      ).to.be.revertedWith("Cannot cancel after delivery");
    });
  });

  describe("Admin dispute resolution", function () {
    it("should refund buyer when admin resolves in buyer's favor", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, totalAmount } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      const buyerBefore = await usdt.balanceOf(buyer.address);

      await escrow.connect(owner).adminResolve(orderId, true);

      expect(await usdt.balanceOf(buyer.address)).to.equal(
        buyerBefore + totalAmount
      );

      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
    });

    it("should release to seller when admin resolves in seller's favor", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, productAmount, shippingAmount } =
        await createStandardOrder(escrow, usdt, buyer, seller, upline);

      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      const sellerBefore = await usdt.balanceOf(seller.address);

      await escrow.connect(owner).adminResolve(orderId, false);

      const sellerAmount = productAmount + shippingAmount;
      expect(await usdt.balanceOf(seller.address)).to.equal(
        sellerBefore + sellerAmount
      );

      const order = await escrow.orders(orderId);
      expect(order.resolved).to.be.true;
    });

    it("should reject adminResolve when no dispute exists", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      // No refund requested — no dispute
      await expect(
        escrow.connect(owner).adminResolve(orderId, true)
      ).to.be.revertedWith("No dispute to resolve");
    });
  });

  describe("Cannot operate on resolved orders", function () {
    it("should reject refund on resolved order", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      // Resolve via refund
      await escrow.connect(buyer).requestRefund(orderId);

      await expect(
        escrow.connect(buyer).requestRefund(orderId)
      ).to.be.revertedWith("Order already resolved");
    });

    it("should reject cancel on resolved order", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(buyer).requestRefund(orderId);

      await expect(
        escrow.connect(seller).cancelOrder(orderId)
      ).to.be.revertedWith("Order already resolved");
    });

    it("should reject confirm on resolved order", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(buyer).requestRefund(orderId);

      await expect(
        escrow.connect(buyer).confirmReceipt(orderId)
      ).to.be.revertedWith("Order already resolved");
    });

    it("should reject adminResolve on resolved order", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(buyer).requestRefund(orderId);

      await expect(
        escrow.connect(owner).adminResolve(orderId, true)
      ).to.be.revertedWith("Order already resolved");
    });

    it("should reject autoRefund on resolved order", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(buyer).requestRefund(orderId);

      await time.increase(THIRTY_DAYS);

      await expect(
        escrow.connect(buyer).autoRefund(orderId)
      ).to.be.revertedWith("Order already resolved");
    });
  });

  describe("Access control", function () {
    it("should not allow non-owner to call adminResolve", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(
        escrow.connect(other).adminResolve(orderId, true)
      ).to.be.revertedWith("Not admin");
    });

    it("should not allow non-seller to mark delivered", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(
        escrow.connect(other).markDelivered(orderId)
      ).to.be.revertedWith("Only seller can mark delivered");
    });

    it("should not allow non-buyer to confirm receipt", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(
        escrow.connect(other).confirmReceipt(orderId)
      ).to.be.revertedWith("Only buyer can confirm");
    });

    it("should not allow non-buyer to request refund", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(
        escrow.connect(other).requestRefund(orderId)
      ).to.be.revertedWith("Only buyer can request refund");
    });

    it("should not allow non-seller to cancel order", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(
        escrow.connect(other).cancelOrder(orderId)
      ).to.be.revertedWith("Only seller can cancel");
    });
  });

  describe("Edge cases", function () {
    it("should send upline share to system wallet when upline is address(0)", async function () {
      const { escrow, usdt, owner, buyer, seller } = await loadFixture(
        deployFixture
      );

      const orderId = ethers.id("no-upline-order");
      const productAmount = 1000_000000n;
      const shippingAmount = 50_000000n;
      const platformFee = 100_000000n;
      const totalAmount = productAmount + shippingAmount + platformFee;

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);
      await escrow
        .connect(buyer)
        .createOrder(
          orderId,
          seller.address,
          await usdt.getAddress(),
          productAmount,
          shippingAmount,
          ethers.ZeroAddress
        );

      await escrow.connect(seller).markDelivered(orderId);

      const ownerBefore = await usdt.balanceOf(owner.address);

      await escrow.connect(buyer).confirmReceipt(orderId);

      // System gets systemFee (remainder) + upline share
      const buyerCashback = (productAmount * 250n) / 10000n;
      const uplineFee = (productAmount * 250n) / 10000n;
      const systemFee = platformFee - buyerCashback - uplineFee;
      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemFee + uplineFee
      );
    });

    it("should reject duplicate orderId", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      const productAmount = 100_000000n;
      const shippingAmount = 10_000000n;
      const platformFee = 10_000000n;
      const totalAmount = productAmount + shippingAmount + platformFee;

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);

      await expect(
        escrow
          .connect(buyer)
          .createOrder(
            orderId,
            seller.address,
            await usdt.getAddress(),
            productAmount,
            shippingAmount,
            upline.address
          )
      ).to.be.revertedWith("Order already exists");
    });

    it("should reject zero product amount", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      const orderId = ethers.id("zero-amount");
      await usdt
        .connect(buyer)
        .approve(await escrow.getAddress(), 1000_000000n);

      await expect(
        escrow
          .connect(buyer)
          .createOrder(
            orderId,
            seller.address,
            await usdt.getAddress(),
            0,
            50_000000n,
            upline.address
          )
      ).to.be.revertedWith("Product amount must be > 0");
    });

    it("should allow zero shipping amount", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      const orderId = ethers.id("zero-shipping");
      const productAmount = 100_000000n;
      const platformFee = 10_000000n;
      const totalAmount = productAmount + platformFee;

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);

      await escrow
        .connect(buyer)
        .createOrder(
          orderId,
          seller.address,
          await usdt.getAddress(),
          productAmount,
          0,
          upline.address
        );

      const order = await escrow.orders(orderId);
      expect(order.shippingAmount).to.equal(0n);
    });

    it("should reject buyer == seller (self-dealing)", async function () {
      const { escrow, usdt, buyer, upline } = await loadFixture(deployFixture);

      const orderId = ethers.id("self-deal");
      const productAmount = 100_000000n;
      const totalAmount = productAmount + 10_000000n; // + platformFee

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);

      await expect(
        escrow
          .connect(buyer)
          .createOrder(
            orderId,
            buyer.address, // buyer == seller
            await usdt.getAddress(),
            productAmount,
            0,
            upline.address
          )
      ).to.be.revertedWith("Buyer cannot be seller");
    });

    it("should reject non-whitelisted token", async function () {
      const { escrow, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      const MockERC20 = await ethers.getContractFactory("MockERC20");
      const badToken = await MockERC20.deploy("Bad", "BAD", 6);
      await badToken.mint(buyer.address, 10000_000000n);

      const orderId = ethers.id("bad-token");
      await badToken
        .connect(buyer)
        .approve(await escrow.getAddress(), 10000_000000n);

      await expect(
        escrow
          .connect(buyer)
          .createOrder(
            orderId,
            seller.address,
            await badToken.getAddress(),
            100_000000n,
            0,
            upline.address
          )
      ).to.be.revertedWith("Token not accepted");
    });
  });

  describe("Pausable", function () {
    it("should prevent createOrder when paused", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      await escrow.pause();

      const orderId = ethers.id("paused-order");
      await usdt.connect(buyer).approve(await escrow.getAddress(), 1000n);

      await expect(
        escrow
          .connect(buyer)
          .createOrder(
            orderId,
            seller.address,
            await usdt.getAddress(),
            100n,
            0,
            upline.address
          )
      ).to.be.revertedWithCustomError(escrow, "EnforcedPause");
    });

    it("should allow operations after unpause", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      await escrow.pause();
      await escrow.unpause();

      // Should work again
      const orderId = ethers.id("unpaused-order");
      const productAmount = 100_000000n;
      const platformFee = 10_000000n;
      const totalAmount = productAmount + platformFee;

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);
      await escrow
        .connect(buyer)
        .createOrder(
          orderId,
          seller.address,
          await usdt.getAddress(),
          productAmount,
          0,
          upline.address
        );

      const order = await escrow.orders(orderId);
      expect(order.buyer).to.equal(buyer.address);
    });
  });

  describe("sweepExcess", function () {
    it("should allow owner to sweep excess tokens", async function () {
      const { escrow, usdt, owner } = await loadFixture(deployFixture);

      // Directly mint tokens to the contract (simulating accidental transfer)
      await usdt.mint(await escrow.getAddress(), 1000n);

      const ownerBefore = await usdt.balanceOf(owner.address);
      await escrow.sweepExcess(await usdt.getAddress(), 1000n);

      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + 1000n
      );
    });

    it("should reject non-owner sweep", async function () {
      const { escrow, usdt, other } = await loadFixture(deployFixture);

      await expect(
        escrow.connect(other).sweepExcess(await usdt.getAddress(), 1000n)
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
    });
  });

  describe("Admin role", function () {
    it("isAdmin(owner) returns true without addAdmin", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      expect(await escrow.isAdmin(owner.address)).to.equal(true);
    });

    it("isAdmin(random) returns false", async function () {
      const { escrow, other } = await loadFixture(deployFixture);
      expect(await escrow.isAdmin(other.address)).to.equal(false);
    });

    it("addAdmin from owner flips mapping and emits event", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await expect(escrow.connect(owner).addAdmin(other.address))
        .to.emit(escrow, "AdminAdded")
        .withArgs(other.address);
      expect(await escrow.admins(other.address)).to.equal(true);
      expect(await escrow.isAdmin(other.address)).to.equal(true);
    });

    it("addAdmin reverts for zero address", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).addAdmin(ethers.ZeroAddress)
      ).to.be.revertedWith("Zero address");
    });

    it("addAdmin reverts when already admin", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await escrow.connect(owner).addAdmin(other.address);
      await expect(
        escrow.connect(owner).addAdmin(other.address)
      ).to.be.revertedWith("Already admin");
    });

    it("addAdmin reverts for non-owner caller", async function () {
      const { escrow, other, buyer } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(other).addAdmin(buyer.address)
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
    });

    it("removeAdmin flips mapping and emits event", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await escrow.connect(owner).addAdmin(other.address);
      await expect(escrow.connect(owner).removeAdmin(other.address))
        .to.emit(escrow, "AdminRemoved")
        .withArgs(other.address);
      expect(await escrow.admins(other.address)).to.equal(false);
    });

    it("removeAdmin reverts on non-admin", async function () {
      const { escrow, owner, other } = await loadFixture(deployFixture);
      await expect(
        escrow.connect(owner).removeAdmin(other.address)
      ).to.be.revertedWith("Not admin");
    });

    it("owner is unrevokable as admin", async function () {
      const { escrow, owner } = await loadFixture(deployFixture);
      // Owner was never added via addAdmin, so removeAdmin(owner) must revert.
      await expect(
        escrow.connect(owner).removeAdmin(owner.address)
      ).to.be.revertedWith("Not admin");
      // And even if addAdmin + removeAdmin were run, isAdmin stays true via
      // the implicit owner clause.
      await escrow.connect(owner).addAdmin(owner.address);
      await escrow.connect(owner).removeAdmin(owner.address);
      expect(await escrow.isAdmin(owner.address)).to.equal(true);
    });

    it("initialize seeds initialAdmins", async function () {
      const signers = await ethers.getSigners();
      const deployer = signers[0];
      const seeded = signers[4];
      const ProductEscrow = await ethers.getContractFactory("ProductEscrow");
      const escrow = await upgrades.deployProxy(
        ProductEscrow,
        [deployer.address, [seeded.address]],
        { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
      );
      await escrow.waitForDeployment();
      expect(await escrow.admins(seeded.address)).to.equal(true);
    });

    it("adminResolve succeeds for explicitly added admin", async function () {
      const { escrow, usdt, owner, buyer, seller, upline, other } = await loadFixture(deployFixture);
      const { orderId } = await createStandardOrder(escrow, usdt, buyer, seller, upline);
      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      await escrow.connect(owner).addAdmin(other.address);
      await expect(
        escrow.connect(other).adminResolve(orderId, true)
      ).to.emit(escrow, "DisputeResolved").withArgs(orderId, true);
    });

    it("adminResolve succeeds for owner (implicit admin)", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(deployFixture);
      const { orderId } = await createStandardOrder(escrow, usdt, buyer, seller, upline);
      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      await expect(
        escrow.connect(owner).adminResolve(orderId, true)
      ).to.emit(escrow, "DisputeResolved");
    });

    it("adminResolve reverts for random EOA", async function () {
      const { escrow, usdt, buyer, seller, upline, other } = await loadFixture(deployFixture);
      const { orderId } = await createStandardOrder(escrow, usdt, buyer, seller, upline);
      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      await expect(
        escrow.connect(other).adminResolve(orderId, true)
      ).to.be.revertedWith("Not admin");
    });
  });

  describe("Events", function () {
    it("should emit OrderCreated on createOrder", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );

      const orderId = ethers.id("event-order-1");
      const productAmount = 100_000000n;
      const shippingAmount = 10_000000n;
      const platformFee = 10_000000n;
      const totalAmount = productAmount + shippingAmount + platformFee;

      await usdt.connect(buyer).approve(await escrow.getAddress(), totalAmount);

      await expect(
        escrow
          .connect(buyer)
          .createOrder(
            orderId,
            seller.address,
            await usdt.getAddress(),
            productAmount,
            shippingAmount,
            upline.address
          )
      )
        .to.emit(escrow, "OrderCreated")
        .withArgs(
          orderId,
          buyer.address,
          seller.address,
          await usdt.getAddress(),
          productAmount,
          shippingAmount,
          platformFee
        );
    });

    it("should emit DeliveryMarked on markDelivered", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(escrow.connect(seller).markDelivered(orderId))
        .to.emit(escrow, "DeliveryMarked")
        .withArgs(orderId);
    });

    it("should emit ReceiptConfirmed and OrderReleased on confirmReceipt", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);

      const tx = escrow.connect(buyer).confirmReceipt(orderId);
      await expect(tx).to.emit(escrow, "ReceiptConfirmed").withArgs(orderId);
      await expect(tx).to.emit(escrow, "OrderReleased");
    });

    it("should emit OrderRefunded on instant refund", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId, totalAmount } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(escrow.connect(buyer).requestRefund(orderId))
        .to.emit(escrow, "OrderRefunded")
        .withArgs(orderId, buyer.address, totalAmount);
    });

    it("should emit RefundRequested on dispute refund", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);

      await expect(escrow.connect(buyer).requestRefund(orderId))
        .to.emit(escrow, "RefundRequested")
        .withArgs(orderId);
    });

    it("should emit OrderCancelled on cancelOrder", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await expect(escrow.connect(seller).cancelOrder(orderId))
        .to.emit(escrow, "OrderCancelled")
        .withArgs(orderId);
    });

    it("should emit AutoRefunded on autoRefund", async function () {
      const { escrow, usdt, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await time.increase(THIRTY_DAYS);

      await expect(escrow.connect(buyer).autoRefund(orderId))
        .to.emit(escrow, "AutoRefunded")
        .withArgs(orderId);
    });

    it("should emit DisputeResolved on adminResolve", async function () {
      const { escrow, usdt, owner, buyer, seller, upline } = await loadFixture(
        deployFixture
      );
      const { orderId } = await createStandardOrder(
        escrow,
        usdt,
        buyer,
        seller,
        upline
      );

      await escrow.connect(seller).markDelivered(orderId);
      await escrow.connect(buyer).requestRefund(orderId);

      await expect(escrow.connect(owner).adminResolve(orderId, true))
        .to.emit(escrow, "DisputeResolved")
        .withArgs(orderId, true);
    });
  });
});
