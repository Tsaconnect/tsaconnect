const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
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
    const mcgp = await MockERC20.deploy("MCG Point", "MCGP", 18); // 18-dec
    const unapproved = await MockERC20.deploy("Bad Token", "BAD", 6);

    const ServiceContact = await ethers.getContractFactory("ServiceContact");
    const service = await upgrades.deployProxy(
      ServiceContact,
      [owner.address],
      { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] },
    );
    await service.waitForDeployment();

    // Accept and set fees for the stables
    await service.setTokenAcceptance(await usdt.getAddress(), true);
    await service.setTokenAcceptance(await usdc.getAddress(), true);
    await service.setFeeAmount(await usdt.getAddress(), FEE_AMOUNT);
    await service.setFeeAmount(await usdc.getAddress(), FEE_AMOUNT);

    // Mint tokens to caller
    await usdt.mint(caller.address, 10000_000000n);
    await usdc.mint(caller.address, 10000_000000n);

    return { service, usdt, usdc, mcgp, unapproved, owner, caller, provider, upline, other };
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

      const providerAmount = (FEE_AMOUNT * 2500n) / 10000n;
      const callerCashback = (FEE_AMOUNT * 1250n) / 10000n;
      const uplineAmount = (FEE_AMOUNT * 1250n) / 10000n;
      const systemAmount = FEE_AMOUNT - providerAmount - callerCashback - uplineAmount;

      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemAmount
      );
      expect(await usdt.balanceOf(provider.address)).to.equal(
        providerBefore + providerAmount
      );
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

  describe("Per-token fee", function () {
    it("supports an 18-decimal token alongside 6-decimal stables", async function () {
      const { service, mcgp, owner, caller, provider, upline } =
        await loadFixture(deployFixture);

      // Configure MCGP at $0.10 worth assuming MCGP ≈ $0.02 → 5 MCGP at 18-dec
      const mcgpFee = ethers.parseUnits("5", 18);
      await service.setTokenAcceptance(await mcgp.getAddress(), true);
      await service.setFeeAmount(await mcgp.getAddress(), mcgpFee);

      // Mint and approve
      await mcgp.mint(caller.address, ethers.parseUnits("100", 18));
      await mcgp
        .connect(caller)
        .approve(await service.getAddress(), mcgpFee);

      const providerBefore = await mcgp.balanceOf(provider.address);
      const ownerBefore = await mcgp.balanceOf(owner.address);

      await service
        .connect(caller)
        .payContactFee(
          provider.address,
          upline.address,
          await mcgp.getAddress()
        );

      const providerAmount = (mcgpFee * 2500n) / 10000n;
      expect(await mcgp.balanceOf(provider.address)).to.equal(
        providerBefore + providerAmount
      );
      // Owner gets system share (50%)
      const callerCashback = (mcgpFee * 1250n) / 10000n;
      const uplineAmount = (mcgpFee * 1250n) / 10000n;
      const systemAmount = mcgpFee - providerAmount - callerCashback - uplineAmount;
      expect(await mcgp.balanceOf(owner.address)).to.equal(
        ownerBefore + systemAmount
      );
    });

    it("rejects payment in a token whose fee is unset", async function () {
      const { service, usdc, caller, provider, upline } = await loadFixture(
        deployFixture
      );

      // Clear the configured fee for USDC
      await service.setFeeAmount(await usdc.getAddress(), 0n);

      await usdc
        .connect(caller)
        .approve(await service.getAddress(), FEE_AMOUNT);

      await expect(
        service
          .connect(caller)
          .payContactFee(
            provider.address,
            upline.address,
            await usdc.getAddress()
          )
      ).to.be.revertedWith("Fee not configured for token");
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

      expect(await usdt.balanceOf(owner.address)).to.equal(
        ownerBefore + systemAmount + uplineAmount
      );
    });
  });

  describe("Owner can update fee amount", function () {
    it("should update per-token fee", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      const newFee = 200000n; // $0.20
      await service
        .connect(owner)
        .setFeeAmount(await usdt.getAddress(), newFee);

      expect(await service.feeAmounts(await usdt.getAddress())).to.equal(
        newFee
      );
    });

    it("should use updated fee for next payment", async function () {
      const { service, usdt, owner, caller, provider, upline } =
        await loadFixture(deployFixture);

      const newFee = 200000n;
      await service
        .connect(owner)
        .setFeeAmount(await usdt.getAddress(), newFee);

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
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await expect(
        service
          .connect(owner)
          .setFeeAmount(await usdt.getAddress(), MIN_FEE - 1n)
      ).to.be.revertedWith("Fee below minimum");
    });

    it("should accept fee at exactly minimum", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await service
        .connect(owner)
        .setFeeAmount(await usdt.getAddress(), MIN_FEE);
      expect(await service.feeAmounts(await usdt.getAddress())).to.equal(
        MIN_FEE
      );
    });

    it("should accept zero to clear", async function () {
      const { service, usdt, owner } = await loadFixture(deployFixture);

      await service.connect(owner).setFeeAmount(await usdt.getAddress(), 0n);
      expect(await service.feeAmounts(await usdt.getAddress())).to.equal(0n);
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
      const { service, usdt, other } = await loadFixture(deployFixture);

      await expect(
        service
          .connect(other)
          .setFeeAmount(await usdt.getAddress(), 200000n)
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

    it("should not allow non-owner to upgrade", async function () {
      const { service, other } = await loadFixture(deployFixture);
      // upgradeTo is restricted by _authorizeUpgrade onlyOwner
      const ServiceContact = await ethers.getContractFactory(
        "ServiceContact",
        other,
      );
      await expect(
        upgrades.upgradeProxy(await service.getAddress(), ServiceContact, {
          unsafeAllow: ["constructor"],
        }),
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
            caller.address,
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
      const { service, usdt, owner } = await loadFixture(deployFixture);

      const newFee = 200000n;

      await expect(
        service.connect(owner).setFeeAmount(await usdt.getAddress(), newFee)
      )
        .to.emit(service, "FeeAmountUpdated")
        .withArgs(await usdt.getAddress(), FEE_AMOUNT, newFee);
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
