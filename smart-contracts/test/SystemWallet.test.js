const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

// Platform fees go to `systemWallet` once set, and to `owner()` until then, on both
// ProductEscrowV2 and ServiceContactV2.

const USDT = (n) => ethers.parseUnits(String(n), 6);

async function deployToken(recipients) {
  const token = await (await ethers.getContractFactory("MockERC20")).deploy("USDT", "USDT", 6);
  await token.waitForDeployment();
  for (const acct of recipients) await token.mint(acct.address, USDT(1_000_000));
  return token;
}

async function deployUpgraded(v1Name, v2Name, v1Args) {
  const proxy = await upgrades.deployProxy(await ethers.getContractFactory(v1Name), v1Args, {
    kind: "uups",
    unsafeAllow: ["constructor"],
  });
  await proxy.waitForDeployment();
  const escrow = await upgrades.upgradeProxy(await proxy.getAddress(), await ethers.getContractFactory(v2Name), {
    unsafeAllow: ["constructor", "missing-initializer"],
    call: { fn: "initializeV2" },
  });
  await escrow.waitForDeployment();
  return escrow;
}

describe("systemWallet fee recipient", () => {
  describe("ProductEscrowV2", () => {
    const PRODUCT = USDT(1000);
    const PLATFORM_FEE = (PRODUCT * 200n) / 10000n;
    const GAS = 100000n; // $0.10 legacy fallback (feeManager unset)

    async function setup() {
      const [owner, admin, buyer, seller, upline, feeWallet, stranger] = await ethers.getSigners();
      const escrow = await deployUpgraded("ProductEscrow", "ProductEscrowV2", [owner.address, [admin.address]]);
      const token = await deployToken([buyer]);
      await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
      await escrow.connect(owner).setLegacySplitEnabled(false);
      return { escrow, token, owner, buyer, seller, upline, feeWallet, stranger };
    }

    async function placeOrder(ctx, id) {
      await ctx.token.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), PRODUCT + PLATFORM_FEE + GAS);
      await ctx.escrow.connect(ctx.buyer).createOrder(
        id, ctx.seller.address, await ctx.token.getAddress(), PRODUCT, 0n, ctx.upline.address
      );
    }

    it("falls back to owner() while systemWallet is unset", async () => {
      const ctx = await setup();
      expect(await ctx.escrow.systemWallet()).to.equal(ethers.ZeroAddress);
      const before = await ctx.token.balanceOf(ctx.owner.address);
      await placeOrder(ctx, ethers.id("unset"));
      await ctx.escrow.connect(ctx.seller).markDelivered(ethers.id("unset"));
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(ethers.id("unset"));
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(before + PLATFORM_FEE + GAS);
    });

    it("pays the gas fee and platform fee to systemWallet once set", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.owner).setSystemWallet(ctx.feeWallet.address);
      const ownerBefore = await ctx.token.balanceOf(ctx.owner.address);

      await placeOrder(ctx, ethers.id("set"));
      expect(await ctx.token.balanceOf(ctx.feeWallet.address)).to.equal(GAS);

      await ctx.escrow.connect(ctx.seller).markDelivered(ethers.id("set"));
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(ethers.id("set"));
      expect(await ctx.token.balanceOf(ctx.feeWallet.address)).to.equal(GAS + PLATFORM_FEE);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerBefore);
    });

    it("sends only the system share to systemWallet when the legacy split is on", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(true);
      await ctx.escrow.connect(ctx.owner).setSystemWallet(ctx.feeWallet.address);
      await placeOrder(ctx, ethers.id("legacy"));
      await ctx.escrow.connect(ctx.seller).markDelivered(ethers.id("legacy"));
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(ethers.id("legacy"));
      const cashback = (PRODUCT * 50n) / 10000n;
      const upline = (PRODUCT * 50n) / 10000n;
      expect(await ctx.token.balanceOf(ctx.feeWallet.address)).to.equal(GAS + PLATFORM_FEE - cashback - upline);
    });

    it("setSystemWallet is owner-only and rejects the zero address", async () => {
      const ctx = await setup();
      await expect(ctx.escrow.connect(ctx.stranger).setSystemWallet(ctx.feeWallet.address))
        .to.be.revertedWithCustomError(ctx.escrow, "OwnableUnauthorizedAccount");
      await expect(ctx.escrow.connect(ctx.owner).setSystemWallet(ethers.ZeroAddress)).to.be.revertedWith("Zero address");
      await expect(ctx.escrow.connect(ctx.owner).setSystemWallet(ctx.feeWallet.address))
        .to.emit(ctx.escrow, "SystemWalletUpdated").withArgs(ethers.ZeroAddress, ctx.feeWallet.address);
    });
  });

  describe("ServiceContactV2", () => {
    const FEE = 100000n; // $0.10

    async function setup() {
      const [owner, caller, provider, upline, feeWallet, stranger] = await ethers.getSigners();
      const escrow = await deployUpgraded("ServiceContact", "ServiceContactV2", [owner.address]);
      const token = await deployToken([caller]);
      await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
      await escrow.connect(owner).setFeeAmount(await token.getAddress(), FEE);
      await escrow.connect(owner).setLegacySplitEnabled(false);
      return { escrow, token, owner, caller, provider, upline, feeWallet, stranger };
    }

    async function pay(ctx) {
      await ctx.token.connect(ctx.caller).approve(await ctx.escrow.getAddress(), FEE);
      await ctx.escrow.connect(ctx.caller).payContactFee(ctx.provider.address, ctx.upline.address, await ctx.token.getAddress());
    }

    it("falls back to owner() while systemWallet is unset", async () => {
      const ctx = await setup();
      const before = await ctx.token.balanceOf(ctx.owner.address);
      await pay(ctx);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(before + FEE);
    });

    it("pays the fee to systemWallet once set", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.owner).setSystemWallet(ctx.feeWallet.address);
      const ownerBefore = await ctx.token.balanceOf(ctx.owner.address);
      await pay(ctx);
      expect(await ctx.token.balanceOf(ctx.feeWallet.address)).to.equal(FEE);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerBefore);
    });

    it("sends only the system share to systemWallet when the legacy split is on", async () => {
      const ctx = await setup();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(true);
      await ctx.escrow.connect(ctx.owner).setSystemWallet(ctx.feeWallet.address);
      await pay(ctx);
      const system = FEE - (FEE * 2500n) / 10000n - (FEE * 1250n) / 10000n - (FEE * 1250n) / 10000n;
      expect(await ctx.token.balanceOf(ctx.feeWallet.address)).to.equal(system);
    });

    it("setSystemWallet is owner-only and rejects the zero address", async () => {
      const ctx = await setup();
      await expect(ctx.escrow.connect(ctx.stranger).setSystemWallet(ctx.feeWallet.address))
        .to.be.revertedWithCustomError(ctx.escrow, "OwnableUnauthorizedAccount");
      await expect(ctx.escrow.connect(ctx.owner).setSystemWallet(ethers.ZeroAddress)).to.be.revertedWith("Zero address");
    });
  });
});
