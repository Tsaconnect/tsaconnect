const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

describe("ProductEscrow upgrade path", function () {
  it("preserves admins mapping across UUPS upgrade", async function () {
    const [owner, alice, bob] = await ethers.getSigners();

    const V1 = await ethers.getContractFactory("ProductEscrow");
    const proxy = await upgrades.deployProxy(
      V1,
      [owner.address, [alice.address]],
      { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
    );
    await proxy.waitForDeployment();

    // Add a second admin post-deploy to ensure non-init writes also survive.
    await proxy.connect(owner).addAdmin(bob.address);

    expect(await proxy.admins(alice.address)).to.equal(true);
    expect(await proxy.admins(bob.address)).to.equal(true);

    const V2 = await ethers.getContractFactory("ProductEscrowV2Test");
    const upgraded = await upgrades.upgradeProxy(
      await proxy.getAddress(),
      V2,
      { unsafeAllow: ["constructor", "missing-initializer"] }
    );

    // Both admins survive.
    expect(await upgraded.admins(alice.address)).to.equal(true);
    expect(await upgraded.admins(bob.address)).to.equal(true);

    // New V2 function is callable on the same address.
    expect(await upgraded.version()).to.equal("v2-test");

    // Owner unchanged.
    expect(await upgraded.owner()).to.equal(owner.address);
  });

  it("_authorizeUpgrade rejects non-owner", async function () {
    const [owner, attacker] = await ethers.getSigners();

    const V1 = await ethers.getContractFactory("ProductEscrow");
    const proxy = await upgrades.deployProxy(
      V1,
      [owner.address, []],
      { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
    );
    await proxy.waitForDeployment();

    const V2 = await ethers.getContractFactory("ProductEscrowV2Test");
    const v2Impl = await V2.deploy();
    await v2Impl.waitForDeployment();

    // UUPS exposes upgradeToAndCall on the proxy; must fail for non-owner.
    const proxyAsV1 = V1.attach(await proxy.getAddress()).connect(attacker);
    await expect(
      proxyAsV1.upgradeToAndCall(await v2Impl.getAddress(), "0x")
    ).to.be.revertedWithCustomError(proxy, "OwnableUnauthorizedAccount");
  });

  it("initialize cannot be called twice on the proxy", async function () {
    const [owner, other] = await ethers.getSigners();

    const V1 = await ethers.getContractFactory("ProductEscrow");
    const proxy = await upgrades.deployProxy(
      V1,
      [owner.address, []],
      { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
    );
    await proxy.waitForDeployment();

    await expect(
      proxy.initialize(other.address, [])
    ).to.be.revertedWithCustomError(proxy, "InvalidInitialization");
  });

  it("implementation contract cannot be initialized directly", async function () {
    // Deploy the implementation without the proxy; its constructor calls
    // _disableInitializers, so any initialize() on it must revert.
    const [owner] = await ethers.getSigners();
    const V1 = await ethers.getContractFactory("ProductEscrow");
    const impl = await V1.deploy();
    await impl.waitForDeployment();

    await expect(
      impl.initialize(owner.address, [])
    ).to.be.revertedWithCustomError(impl, "InvalidInitialization");
  });
});
