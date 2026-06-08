const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

// ──────────────────────────────────────────────────────────────────────────────
// FeeManager — UUPS, one instance per chain, the on-chain SOURCE OF TRUTH for fee
// rates. See docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §3/§3.1.
//
//   Rate = { bps:uint16, flatUSD:uint64, mcgpExempt:bool, active:bool }
//   key  = keccak256(abi.encode(feature, tokenSymbol))
//   MAX_BPS = 1000 (10% hard ceiling, enforced in both setters)
// ──────────────────────────────────────────────────────────────────────────────

const { keccak256, AbiCoder } = ethers;
const abi = AbiCoder.defaultAbiCoder();

// Solidity struct args go over the wire as ordered tuples.
const rate = (bps, flatUSD, mcgpExempt, active) => [bps, flatUSD, mcgpExempt, active];

function rateKey(feature, token) {
  return keccak256(abi.encode(["string", "string"], [feature, token]));
}

async function deploy() {
  const [owner, alice, other] = await ethers.getSigners();
  const FeeManager = await ethers.getContractFactory("FeeManager");
  const fm = await upgrades.deployProxy(FeeManager, [owner.address], {
    kind: "uups",
    initializer: "initialize",
    unsafeAllow: ["constructor"],
  });
  await fm.waitForDeployment();
  return { fm, owner, alice, other, FeeManager };
}

describe("FeeManager", () => {
  it("initializes the owner and exposes MAX_BPS = 1000", async () => {
    const { fm, owner } = await deploy();
    expect(await fm.owner()).to.equal(owner.address);
    expect(await fm.MAX_BPS()).to.equal(1000n);
  });

  it("setRate / rateFor roundtrip", async () => {
    const { fm, owner } = await deploy();
    await fm.connect(owner).setRate("withdrawal", "USDT", rate(50, 100000, false, true));

    const r = await fm.rateFor("withdrawal", "USDT");
    expect(r.bps).to.equal(50n);
    expect(r.flatUSD).to.equal(100000n);
    expect(r.mcgpExempt).to.equal(false);
    expect(r.active).to.equal(true);
  });

  it("rateFor returns active=false (all-zero) for an unset key", async () => {
    const { fm } = await deploy();
    const r = await fm.rateFor("never_set", "USDT");
    expect(r.bps).to.equal(0n);
    expect(r.flatUSD).to.equal(0n);
    expect(r.mcgpExempt).to.equal(false);
    expect(r.active).to.equal(false);
  });

  it("setRate overwrites an existing rate", async () => {
    const { fm, owner } = await deploy();
    await fm.connect(owner).setRate("p2p_platform", "USDC", rate(100, 0, false, true));
    await fm.connect(owner).setRate("p2p_platform", "USDC", rate(60, 0, true, false));

    const r = await fm.rateFor("p2p_platform", "USDC");
    expect(r.bps).to.equal(60n);
    expect(r.mcgpExempt).to.equal(true);
    expect(r.active).to.equal(false);
  });

  it("setRates batch-sets multiple rates", async () => {
    const { fm, owner } = await deploy();
    const features = ["withdrawal", "p2p", "p2p_platform"];
    const tokens = ["*", "USDT", "USDT"];
    const rs = [rate(50, 0, false, true), rate(50, 0, false, true), rate(100, 0, false, true)];

    await fm.connect(owner).setRates(features, tokens, rs);

    const a = await fm.rateFor("withdrawal", "*");
    const b = await fm.rateFor("p2p", "USDT");
    const c = await fm.rateFor("p2p_platform", "USDT");
    expect(a.bps).to.equal(50n);
    expect(a.active).to.equal(true);
    expect(b.bps).to.equal(50n);
    expect(c.bps).to.equal(100n);
    expect(c.active).to.equal(true);
  });

  it("setRates reverts on array length mismatch", async () => {
    const { fm, owner } = await deploy();
    await expect(
      fm.connect(owner).setRates(["a", "b"], ["USDT"], [rate(10, 0, false, true)])
    ).to.be.revertedWith("length mismatch");
  });

  it("setRate reverts when bps > MAX_BPS (1000)", async () => {
    const { fm, owner } = await deploy();
    await expect(
      fm.connect(owner).setRate("withdrawal", "USDT", rate(1001, 0, false, true))
    ).to.be.revertedWith("rate too high");

    // Boundary: exactly MAX_BPS is allowed.
    await expect(fm.connect(owner).setRate("withdrawal", "USDT", rate(1000, 0, false, true))).to.not.be.reverted;
  });

  it("setRates reverts (whole batch) if ANY bps > MAX_BPS", async () => {
    const { fm, owner } = await deploy();
    await expect(
      fm
        .connect(owner)
        .setRates(["a", "b"], ["USDT", "USDT"], [rate(50, 0, false, true), rate(1001, 0, false, true)])
    ).to.be.revertedWith("rate too high");

    // First row must NOT have landed (atomic batch).
    const a = await fm.rateFor("a", "USDT");
    expect(a.active).to.equal(false);
  });

  it("onlyOwner: non-owner cannot setRate or setRates", async () => {
    const { fm, alice } = await deploy();
    await expect(
      fm.connect(alice).setRate("withdrawal", "USDT", rate(50, 0, false, true))
    ).to.be.revertedWithCustomError(fm, "OwnableUnauthorizedAccount");

    await expect(
      fm.connect(alice).setRates(["a"], ["USDT"], [rate(50, 0, false, true)])
    ).to.be.revertedWithCustomError(fm, "OwnableUnauthorizedAccount");
  });

  it("emits RateSet with the full payload (and the keccak256 key) on setRate", async () => {
    const { fm, owner } = await deploy();
    const key = rateKey("withdrawal", "USDT");

    await expect(fm.connect(owner).setRate("withdrawal", "USDT", rate(50, 100000, true, true)))
      .to.emit(fm, "RateSet")
      .withArgs(key, "withdrawal", "USDT", 50n, 100000n, true, true);

    // The emitted key matches the contract's own rateKey() derivation.
    expect(await fm.rateKey("withdrawal", "USDT")).to.equal(key);
  });

  it("emits one RateSet per row on setRates", async () => {
    const { fm, owner } = await deploy();
    const tx = await fm.connect(owner).setRates(
      ["withdrawal", "p2p"],
      ["*", "USDT"],
      [rate(50, 0, false, true), rate(50, 0, false, true)]
    );
    const receipt = await tx.wait();
    const rateSet = receipt.logs.filter((l) => {
      try {
        return fm.interface.parseLog(l)?.name === "RateSet";
      } catch {
        return false;
      }
    });
    expect(rateSet.length).to.equal(2);
  });

  it("UUPS: deployProxy + no-op upgradeProxy preserves a set rate", async () => {
    const { fm, owner } = await deploy();
    await fm.connect(owner).setRate("withdrawal", "USDT", rate(60, 100000, false, true));

    const V2 = await ethers.getContractFactory("FeeManagerV2Test");
    const upgraded = await upgrades.upgradeProxy(await fm.getAddress(), V2, {
      unsafeAllow: ["constructor", "missing-initializer"],
    });

    // Same proxy address; new behavior available.
    expect(await upgraded.getAddress()).to.equal(await fm.getAddress());
    expect(await upgraded.version()).to.equal("v2-test");

    // The rate set before the upgrade survives.
    const r = await upgraded.rateFor("withdrawal", "USDT");
    expect(r.bps).to.equal(60n);
    expect(r.flatUSD).to.equal(100000n);
    expect(r.active).to.equal(true);

    // Owner + MAX_BPS unchanged.
    expect(await upgraded.owner()).to.equal(owner.address);
    expect(await upgraded.MAX_BPS()).to.equal(1000n);
  });

  it("UUPS: validateUpgrade accepts the no-op V2 (storage-layout safe)", async () => {
    const { FeeManager } = await deploy();
    const V2 = await ethers.getContractFactory("FeeManagerV2Test");
    // Throws if the storage layout is incompatible; resolves otherwise.
    await upgrades.validateUpgrade(FeeManager, V2, {
      unsafeAllow: ["constructor", "missing-initializer"],
      kind: "uups",
    });
  });

  it("UUPS: _authorizeUpgrade rejects a non-owner upgradeToAndCall", async () => {
    const { fm, FeeManager, other } = await deploy();
    const V2 = await ethers.getContractFactory("FeeManagerV2Test");
    const v2Impl = await V2.deploy();
    await v2Impl.waitForDeployment();

    const proxyAsImpl = FeeManager.attach(await fm.getAddress()).connect(other);
    await expect(
      proxyAsImpl.upgradeToAndCall(await v2Impl.getAddress(), "0x")
    ).to.be.revertedWithCustomError(fm, "OwnableUnauthorizedAccount");
  });

  it("initialize cannot be called twice on the proxy", async () => {
    const { fm, other } = await deploy();
    await expect(fm.initialize(other.address)).to.be.revertedWithCustomError(fm, "InvalidInitialization");
  });

  it("implementation contract cannot be initialized directly (_disableInitializers)", async () => {
    const { FeeManager, owner } = await deploy();
    const impl = await FeeManager.deploy();
    await impl.waitForDeployment();
    await expect(impl.initialize(owner.address)).to.be.revertedWithCustomError(impl, "InvalidInitialization");
  });
});
