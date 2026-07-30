// The ERC-2771 context overrides themselves, plus the two admin setters the
// other suites only ever exercise on their revert path.
//
// _msgData and _contextSuffixLength are not called anywhere in V2 today. They
// still have to be correct: the moment any inherited or future code reads them,
// a mismatch with _msgSender means half the call strips the appended sender and
// half does not.

const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

const mcgp = (n) => ethers.parseUnits(String(n), 18);
const DEFAULT_CAPS = [0, mcgp(1_000_000_000), 3600, mcgp(1_000_000_000)];

describe("McgpEntitlementSwapV2 — context overrides and admin setters", function () {
  let harness, swap, mcgpToken, usdcToken, forwarder;
  let owner, alice, bob, guardian;

  beforeEach(async function () {
    [owner, alice, bob, guardian] = await ethers.getSigners();

    const MockERC20 = await ethers.getContractFactory("MockERC20");
    mcgpToken = await MockERC20.deploy("MCGP Token", "MCGP", 18);
    usdcToken = await MockERC20.deploy("USD Coin", "USDC", 6);
    await mcgpToken.waitForDeployment();
    await usdcToken.waitForDeployment();

    const MockForwarder = await ethers.getContractFactory("MockForwarder");
    forwarder = await MockForwarder.deploy();
    await forwarder.waitForDeployment();

    const Harness = await ethers.getContractFactory("McgpV2ContextHarness");
    harness = await upgrades.deployProxy(
      Harness,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
      { kind: "uups", unsafeAllow: ["constructor", "missing-initializer"] },
    );
    await harness.waitForDeployment();
    await harness.setTrustedForwarder(await forwarder.getAddress());

    const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
    swap = await upgrades.deployProxy(
      Swap,
      [owner.address, await mcgpToken.getAddress(), await usdcToken.getAddress(), ...DEFAULT_CAPS],
      { kind: "uups" },
    );
    await swap.waitForDeployment();
  });

  // MockForwarder.relay does a `call`, so a view function's return value is not
  // handed back to us directly — read it via a staticcall the forwarder makes on
  // our behalf by encoding the same calldata and calling from the forwarder.
  async function relayStatic(fn, from) {
    const data = harness.interface.encodeFunctionData(fn, []);
    const raw = await ethers.provider.call({
      to: await harness.getAddress(),
      from: await forwarder.getAddress(),
      data: data + from.address.slice(2), // append the 20-byte sender
    });
    return harness.interface.decodeFunctionResult(fn, raw)[0];
  }

  describe("_msgSender / _msgData / _contextSuffixLength agree", function () {
    it("resolves the appended sender and strips exactly those 20 bytes", async function () {
      const sender = await relayStatic("exposedMsgSender", alice);
      expect(sender).to.equal(alice.address);

      const data = await relayStatic("exposedMsgData", alice);
      // The stripped calldata is just the 4-byte selector — the appended address is gone.
      expect(data).to.equal(harness.interface.getFunction("exposedMsgData").selector);

      const suffix = await relayStatic("exposedSuffixLength", alice);
      expect(suffix).to.equal(20n);
    });

    it("a direct call (not via the forwarder) strips nothing", async function () {
      expect(await harness.exposedMsgSender()).to.equal(owner.address);
      expect(await harness.exposedSuffixLength()).to.equal(0n);
      expect(await harness.exposedMsgData()).to.equal(
        harness.interface.getFunction("exposedMsgData").selector,
      );
    });

    it("calldata shorter than 20 bytes from the forwarder does not misparse", async function () {
      // A bare 4-byte selector sent by the trusted forwarder: the length guard must
      // fail closed and report the forwarder itself, not slice into garbage.
      const raw = await ethers.provider.call({
        to: await harness.getAddress(),
        from: await forwarder.getAddress(),
        data: harness.interface.getFunction("exposedMsgSender").selector,
      });
      const [sender] = harness.interface.decodeFunctionResult("exposedMsgSender", raw);
      expect(sender).to.equal(await forwarder.getAddress());
    });
  });

  describe("admin setters", function () {
    it("setFeeManager stores the address and emits both sides of the change", async function () {
      expect(await swap.feeManager()).to.equal(ethers.ZeroAddress); // deployed inert
      await expect(swap.setFeeManager(bob.address))
        .to.emit(swap, "FeeManagerUpdated")
        .withArgs(ethers.ZeroAddress, bob.address);
      expect(await swap.feeManager()).to.equal(bob.address);
    });

    it("setFeeManager is owner-only", async function () {
      await expect(swap.connect(alice).setFeeManager(bob.address))
        .to.be.revertedWithCustomError(swap, "OwnableUnauthorizedAccount");
    });

    it("removeGuardian revokes the role", async function () {
      await swap.addGuardian(guardian.address);
      expect(await swap.guardians(guardian.address)).to.equal(true);
      await expect(swap.removeGuardian(guardian.address))
        .to.emit(swap, "GuardianRemoved")
        .withArgs(guardian.address);
      expect(await swap.guardians(guardian.address)).to.equal(false);
      await expect(swap.connect(guardian).pause()).to.be.revertedWith("Not owner/guardian");
    });

    it("rejects a zero operator or guardian", async function () {
      await expect(swap.addOperator(ethers.ZeroAddress)).to.be.revertedWith("Invalid operator");
      await expect(swap.addGuardian(ethers.ZeroAddress)).to.be.revertedWith("Invalid guardian");
    });

    it("isTrustedForwarder answers false for the zero address", async function () {
      // Guards against a cleared forwarder accidentally trusting address(0).
      expect(await swap.isTrustedForwarder(ethers.ZeroAddress)).to.equal(false);
      await swap.setTrustedForwarder(bob.address);
      expect(await swap.isTrustedForwarder(bob.address)).to.equal(true);
      expect(await swap.isTrustedForwarder(alice.address)).to.equal(false);
    });

    it("initialize rejects zero token addresses", async function () {
      const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
      await expect(
        upgrades.deployProxy(Swap, [owner.address, ethers.ZeroAddress, usdcToken.target, ...DEFAULT_CAPS], { kind: "uups" }),
      ).to.be.revertedWith("Invalid MCGP address");
      await expect(
        upgrades.deployProxy(Swap, [owner.address, mcgpToken.target, ethers.ZeroAddress, ...DEFAULT_CAPS], { kind: "uups" }),
      ).to.be.revertedWith("Invalid USDC address");
    });
  });
});
