const { expect } = require("chai");
const { ethers } = require("hardhat");
const {
  loadFixture,
} = require("@nomicfoundation/hardhat-toolbox/network-helpers");

describe("ServiceContact", function () {
  const FEE_AMOUNT = 100000n; // $0.10 in 6-decimal tokens
  const MIN_FEE = 10000n;

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

      // Expected splits — system gets remainder
      const providerAmount = (FEE_AMOUNT * 2500n) / 10000n; // 25000
      const callerCashback = (FEE_AMOUNT * 1250n) / 10000n; // 12500
      const uplineAmount = (FEE_AMOUNT * 1250n) / 10000n; // 12500
      const systemAmount = FEE_AMOUNT - providerAmount - callerCashback - uplineAmount; // 50000

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

    it("should leave no dust in the contract", async function () {
      const { service, usdt, caller, provider, upline } = await loadFixture(
        deployFixture
      );

      await usdt
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await service
        .connect(caller)
        .payContactFee(
          provider.address,
          upline.address,
          await usdt.getAddress()
        );

      expect(await usdt.balanceOf(await service.getAddress())).to.equal(0n);
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

      const providerAmount = (FEE_AMOUNT * 2500n) / 10000n;
      const callerCashback = (FEE_AMOUNT * 1250n) / 10000n;
      const uplineAmount = (FEE_AMOUNT * 1250n) / 10000n;
      const systemAmount = FEE_AMOUNT - providerAmount - callerCashback - uplineAmount;

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

    it("should reject fee below minimum", async function () {
      const { service, owner } = await loadFixture(deployFixture);

      await expect(
        service.connect(owner).setFeeAmount(MIN_FEE - 1n)
      ).to.be.revertedWith("Fee below minimum");
    });

    it("should accept fee at exactly minimum", async function () {
      const { service, owner } = await loadFixture(deployFixture);

      await service.connect(owner).setFeeAmount(MIN_FEE);
      expect(await service.feeAmount()).to.equal(MIN_FEE);
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

  describe("Self-service prevention", function () {
    it("should reject caller paying for own contact", async function () {
      const { service, usdt, caller, upline } = await loadFixture(
        deployFixture
      );

      await usdt
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await expect(
        service
          .connect(caller)
          .payContactFee(
            caller.address, // caller == provider
            upline.address,
            await usdt.getAddress()
          )
      ).to.be.revertedWith("Cannot pay for own contact");
    });
  });

  describe("Pausable", function () {
    it("should prevent payContactFee when paused", async function () {
      const { service, usdt, owner, caller, provider, upline } =
        await loadFixture(deployFixture);

      await service.connect(owner).pause();

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
      ).to.be.revertedWithCustomError(service, "EnforcedPause");
    });

    it("should allow operations after unpause", async function () {
      const { service, usdt, owner, caller, provider, upline } =
        await loadFixture(deployFixture);

      await service.connect(owner).pause();
      await service.connect(owner).unpause();

      await usdt
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await service
        .connect(caller)
        .payContactFee(
          provider.address,
          upline.address,
          await usdt.getAddress()
        );

      // Should succeed — just verify no revert
    });
  });

  describe("sweepExcess", function () {
    it("should allow owner to sweep excess tokens", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await usdt.mint(await service.getAddress(), 5000n);

      const ownerBefore = await usdt.balanceOf(owner.address);
      await service.sweepExcess(await usdt.getAddress(), 5000n);

      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + 5000n
      );
    });

    it("should reject non-owner sweep", async function () {
      const { service, usdt, other } = await loadFixture(deployFixture);

      await expect(
        service.connect(other).sweepExcess(await usdt.getAddress(), 1000n)
      ).to.be.revertedWithCustomError(service, "OwnableUnauthorizedAccount");
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

    it("should emit ExcessSwept on sweep", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await usdt.mint(await service.getAddress(), 1000n);

      await expect(service.sweepExcess(await usdt.getAddress(), 1000n))
        .to.emit(service, "ExcessSwept")
        .withArgs(await usdt.getAddress(), 1000n);
    });
  });
});
