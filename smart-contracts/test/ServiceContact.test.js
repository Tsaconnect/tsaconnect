const { expect } = require("chai");
const { ethers } = require("hardhat");
const {
  loadFixture,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("ServiceContact", function () {
  const FEE_AMOUNT = 100000n; // $0.10 in 6-decimal tokens

  async function deployFixture() {
    const [owner, caller, provider, upline, other] = await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);
    const usdc = await MockERC20.deploy("USD Coin", "USDC", 6);
    const unapproved = await MockERC20.deploy("Bad Token", "BAD", 6);

    const ServiceContact = await ethers.getContractFactory("ServiceContact");
    const service = await ServiceContact.deploy(owner.address);

    // Accept USDT and USDC
    await service.setTokenAcceptance(await usdt.getAddress(), true);
    await service.setTokenAcceptance(await usdc.getAddress(), true);

    // Mint tokens to caller
    await usdt.mint(caller.address, 10000_000000n);
    await usdc.mint(caller.address, 10000_000000n);

    return { service, usdt, usdc, unapproved, owner, caller, provider, upline, other };
  }

  describe("Happy path: pay contact fee with 4-way split", function () {
    it("should correctly split $0.10 fee", async function () {
      const { service, usdt, owner, caller, provider, upline } =
        await loadFixture(deployFixture);

      await usdt
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      const ownerBefore = await usdt.balanceOf(owner.address);
      const providerBefore = await usdt.balanceOf(provider.address);
      const callerBefore = await usdt.balanceOf(caller.address);
      const uplineBefore = await usdt.balanceOf(upline.address);

      await service
        .connect(caller)
        .payContactFee(
          provider.address,
          upline.address,
          await usdt.getAddress()
        );

      // Expected splits: $0.05 system, $0.025 provider, $0.0125 caller, $0.0125 upline
      const systemAmount = (FEE_AMOUNT * 5000n) / 10000n; // 50000
      const providerAmount = (FEE_AMOUNT * 2500n) / 10000n; // 25000
      const callerCashback = (FEE_AMOUNT * 1250n) / 10000n; // 12500
      const uplineAmount = FEE_AMOUNT - systemAmount - providerAmount - callerCashback; // 12500

      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemAmount
      );
      expect(await usdt.balanceOf(provider.address)).to.equal(
        providerBefore + providerAmount
      );
      // Caller paid FEE_AMOUNT but gets cashback
      expect(await usdt.balanceOf(caller.address)).to.equal(
        callerBefore - FEE_AMOUNT + callerCashback
      );
      expect(await usdt.balanceOf(upline.address)).to.equal(
        uplineBefore + uplineAmount
      );
    });
  });

  describe("Zero upline", function () {
    it("should send upline share to system wallet when upline is address(0)", async function () {
      const { service, usdt, owner, caller, provider } = await loadFixture(
        deployFixture
      );

      await usdt
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      const ownerBefore = await usdt.balanceOf(owner.address);

      await service
        .connect(caller)
        .payContactFee(
          provider.address,
          ethers.ZeroAddress,
          await usdt.getAddress()
        );

      const systemAmount = (FEE_AMOUNT * 5000n) / 10000n;
      const uplineAmount = FEE_AMOUNT - systemAmount - (FEE_AMOUNT * 2500n) / 10000n - (FEE_AMOUNT * 1250n) / 10000n;

      // Owner gets system + upline share
      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemAmount + uplineAmount
      );
    });
  });

  describe("Owner can update fee amount", function () {
    it("should update fee amount", async function () {
      const { service, owner } = await loadFixture(deployFixture);

      const newFee = 200000n; // $0.20
      await service.connect(owner).setFeeAmount(newFee);

      expect(await service.feeAmount()).to.equal(newFee);
    });

    it("should use updated fee for next payment", async function () {
      const { service, usdt, owner, caller, provider, upline } =
        await loadFixture(deployFixture);

      const newFee = 200000n;
      await service.connect(owner).setFeeAmount(newFee);

      await usdt.connect(caller).approve(await service.getAddress(), newFee);

      const callerBefore = await usdt.balanceOf(caller.address);

      await service
        .connect(caller)
        .payContactFee(
          provider.address,
          upline.address,
          await usdt.getAddress()
        );

      const callerCashback = (newFee * 1250n) / 10000n;
      expect(await usdt.balanceOf(caller.address)).to.equal(
        callerBefore - newFee + callerCashback
      );
    });

    it("should reject zero fee amount", async function () {
      const { service, owner } = await loadFixture(deployFixture);

      await expect(
        service.connect(owner).setFeeAmount(0)
      ).to.be.revertedWith("Fee must be > 0");
    });
  });

  describe("Owner can update accepted tokens", function () {
    it("should accept new token", async function () {
      const { service, unapproved, owner } = await loadFixture(deployFixture);

      await service
        .connect(owner)
        .setTokenAcceptance(await unapproved.getAddress(), true);

      expect(
        await service.acceptedTokens(await unapproved.getAddress())
      ).to.be.true;
    });

    it("should remove token acceptance", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await service
        .connect(owner)
        .setTokenAcceptance(await usdt.getAddress(), false);

      expect(await service.acceptedTokens(await usdt.getAddress())).to.be
        .false;
    });
  });

  describe("Access control", function () {
    it("should not allow non-owner to update fee", async function () {
      const { service, other } = await loadFixture(deployFixture);

      await expect(
        service.connect(other).setFeeAmount(200000n)
      ).to.be.revertedWithCustomError(service, "OwnableUnauthorizedAccount");
    });

    it("should not allow non-owner to update token acceptance", async function () {
      const { service, usdt, other } = await loadFixture(deployFixture);

      await expect(
        service
          .connect(other)
          .setTokenAcceptance(await usdt.getAddress(), false)
      ).to.be.revertedWithCustomError(service, "OwnableUnauthorizedAccount");
    });
  });

  describe("Rejects unapproved tokens", function () {
    it("should reject payment with unapproved token", async function () {
      const { service, unapproved, caller, provider, upline } =
        await loadFixture(deployFixture);

      await unapproved.mint(caller.address, FEE_AMOUNT);
      await unapproved
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await expect(
        service
          .connect(caller)
          .payContactFee(
            provider.address,
            upline.address,
            await unapproved.getAddress()
          )
      ).to.be.revertedWith("Token not accepted");
    });
  });

  describe("Caller must have sufficient approval and balance", function () {
    it("should revert without approval", async function () {
      const { service, usdt, caller, provider, upline } = await loadFixture(
        deployFixture
      );

      // No approval given
      await expect(
        service
          .connect(caller)
          .payContactFee(
            provider.address,
            upline.address,
            await usdt.getAddress()
          )
      ).to.be.reverted;
    });

    it("should revert with insufficient balance", async function () {
      const { service, usdt, provider, upline, other } = await loadFixture(
        deployFixture
      );

      // other has no tokens
      await usdt
        .connect(other)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await expect(
        service
          .connect(other)
          .payContactFee(
            provider.address,
            upline.address,
            await usdt.getAddress()
          )
      ).to.be.reverted;
    });
  });

  describe("Events", function () {
    it("should emit ContactFeePaid on successful payment", async function () {
      const { service, usdt, caller, provider, upline } = await loadFixture(
        deployFixture
      );

      await usdt
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await expect(
        service
          .connect(caller)
          .payContactFee(
            provider.address,
            upline.address,
            await usdt.getAddress()
          )
      )
        .to.emit(service, "ContactFeePaid")
        .withArgs(
          caller.address,
          provider.address,
          await usdt.getAddress(),
          FEE_AMOUNT,
          upline.address
        );
    });

    it("should emit FeeAmountUpdated on fee change", async function () {
      const { service, owner } = await loadFixture(deployFixture);

      const newFee = 200000n;

      await expect(service.connect(owner).setFeeAmount(newFee))
        .to.emit(service, "FeeAmountUpdated")
        .withArgs(FEE_AMOUNT, newFee);
    });

    it("should emit TokenAcceptanceUpdated on token change", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await expect(
        service
          .connect(owner)
          .setTokenAcceptance(await usdt.getAddress(), false)
      )
        .to.emit(service, "TokenAcceptanceUpdated")
        .withArgs(await usdt.getAddress(), false);
    });
  });
});
