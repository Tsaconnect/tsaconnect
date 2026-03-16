const { expect } = require("chai");
const { ethers } = require("hardhat");
const {
  loadFixture,
  time,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("ProductEscrow", function () {
  const THIRTY_DAYS = 30 * 24 * 60 * 60;

  async function deployFixture() {
    const [owner, buyer, seller, upline, other] = await ethers.getSigners();

    // Deploy 6-decimal stablecoin mock (USDT/USDC)
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);

    // Deploy 18-decimal MCGP token mock
    const mcgp = await MockERC20.deploy("MCGP Token", "MCGP", 18);

    // Deploy escrow contract with owner as system wallet
    const ProductEscrow = await ethers.getContractFactory("ProductEscrow");
    const escrow = await ProductEscrow.deploy(owner.address);

    // Set MCGP token address
    await escrow.setMcgpToken(await mcgp.getAddress());

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
      const { orderId, productAmount, shippingAmount } =
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

      // Calculate expected splits (basis points of productAmount)
      const systemFee = (productAmount * 500n) / 10000n; // 5%
      const buyerCashback = (productAmount * 250n) / 10000n; // 2.5%
      const uplineFee = (productAmount * 250n) / 10000n; // 2.5%
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

      await escrow.autoRefund(orderId);

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

      await escrow.autoRefund(orderId);

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

      await expect(escrow.autoRefund(orderId)).to.be.revertedWith(
        "30 days not elapsed"
      );
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
      const { orderId, totalAmount } = await createStandardOrder(
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
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
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

      await expect(escrow.autoRefund(orderId)).to.be.revertedWith(
        "Order already resolved"
      );
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
      ).to.be.revertedWithCustomError(escrow, "OwnableUnauthorizedAccount");
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
      const sellerBefore = await usdt.balanceOf(seller.address);

      await escrow.connect(buyer).confirmReceipt(orderId);

      // System gets 5% + 2.5% (upline's share) = 7.5%
      const systemFee = (productAmount * 500n) / 10000n;
      const uplineFee = (productAmount * 250n) / 10000n;
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

      await expect(escrow.autoRefund(orderId))
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
