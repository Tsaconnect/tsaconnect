const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");

// ──────────────────────────────────────────────────────────────────────────────
// FeeManager ⇄ escrows — A1 live-read + legacySplitEnabled gating, UUPS upgrades.
// See docs/superpowers/specs/2026-06-08-configurable-feemanager-design.md §7 (7.1/7.2/7.3) / §12.
//
//   - P2PEscrowV2 → P2PEscrowV3: snapshots p2p_platform + p2p bps at openTrade; keeps feeBps for
//     pre-upgrade trades; _settleRelease/adminResolve sum both snapshots with a feeBps fallback.
//   - ProductEscrow → ProductEscrowV2: live-read product_merchant bps + product flatUSD; legacy split gated.
//   - ServiceContact → ServiceContactV2: live-read off-chain; flat fee stays > 0; 4-way split gated.
//   - Storage-layout safety proven via upgrades.validateUpgrade / upgradeProxy for all three.
// ──────────────────────────────────────────────────────────────────────────────

// Rate tuple ordering matches the Solidity struct { bps, flatUSD, mcgpExempt, active }.
const rate = (bps, flatUSD, mcgpExempt, active) => [bps, flatUSD, mcgpExempt, active];

async function deployFeeManager(owner) {
  const FeeManager = await ethers.getContractFactory("FeeManager");
  const fm = await upgrades.deployProxy(FeeManager, [owner.address], {
    kind: "uups",
    initializer: "initialize",
    unsafeAllow: ["constructor"],
  });
  await fm.waitForDeployment();
  return fm;
}

async function deployToken(symbol = "USDT", decimals = 18, recipients = []) {
  const ERC20 = await ethers.getContractFactory("MockERC20");
  const token = await ERC20.deploy(symbol, symbol, decimals);
  await token.waitForDeployment();
  for (const acct of recipients) await token.mint(acct.address, ethers.parseUnits("1000000", decimals));
  return token;
}

// ── P2P helpers (mirror test/P2PEscrowV2.test.js) ────────────────────────────
async function signVoucher(escrow, seller, v) {
  const domain = {
    name: "TSAConnectP2PEscrow",
    version: "2",
    chainId: (await ethers.provider.getNetwork()).chainId,
    verifyingContract: await escrow.getAddress(),
  };
  const types = { AdVoucher: [
    { name: "seller", type: "address" }, { name: "token", type: "address" },
    { name: "maxAmount", type: "uint256" }, { name: "minPerTrade", type: "uint256" },
    { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
    { name: "nonce", type: "bytes32" },
  ]};
  return seller.signTypedData(domain, types, v);
}

function makeVoucher(seller, token, overrides = {}) {
  return {
    seller, token,
    maxAmount: ethers.parseUnits("1000", 18),
    minPerTrade: 0,
    maxPerTrade: 0,
    deadline: 4102444800, // 2100-01-01
    nonce: ethers.hexlify(ethers.randomBytes(32)),
    ...overrides,
  };
}

// Deploy a V2 proxy, fund alice's pool, and open a single sell-ad trade.
async function deployP2PV2AndOpen(feeBps = null) {
  const [owner, admin, alice, bob, system] = await ethers.getSigners();
  const V2 = await ethers.getContractFactory("P2PEscrowV2");
  const escrow = await upgrades.deployProxy(V2, [owner.address, [admin.address]], { kind: "uups" });
  await escrow.waitForDeployment();
  await escrow.connect(owner).setSystemWallet(system.address);
  if (feeBps !== null) await escrow.connect(owner).setFeeBps(feeBps);

  const token = await deployToken("USDT", 18, [alice, bob]);
  await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);

  const dep = ethers.parseUnits("1000", 18);
  await token.connect(alice).approve(await escrow.getAddress(), dep);
  await escrow.connect(alice).deposit(await token.getAddress(), dep);

  const v = makeVoucher(alice.address, await token.getAddress());
  const sig = await signVoucher(escrow, alice, v);
  const tradeId = ethers.id("v2-trade-1");
  const amt = ethers.parseUnits("100", 18);
  await escrow.connect(bob).openTrade(tradeId, v, sig, amt);

  return { escrow, owner, admin, alice, bob, system, token, tradeId, amt };
}

// Mirrors the production upgrade: upgradeProxy with the `call` option runs the one-shot
// `initializeV3()` reinitializer atomically, so the freshly-added `legacySplitEnabled` slot
// is set TRUE at upgrade (the spec D5 / §9-step-1 "deployed TRUE = byte-for-byte today"
// guarantee). Without `call`, upgradeProxy does NOT re-run initialize and the bool stays false.
async function upgradeToV3(proxyAddr) {
  const V3 = await ethers.getContractFactory("P2PEscrowV3");
  const upgraded = await upgrades.upgradeProxy(proxyAddr, V3, {
    unsafeAllow: ["constructor", "missing-initializer"],
    call: { fn: "initializeV3" },
  });
  await upgraded.waitForDeployment();
  return upgraded;
}

// Deploy a fresh V3 proxy directly (for the "open after FeeManager set" tests). We deploy V2
// then upgrade to V3 so the proxy is a real V2→V3 (matching production); the proxy keeps state.
async function deployP2PV3() {
  const [owner, admin, alice, bob, system] = await ethers.getSigners();
  const V2 = await ethers.getContractFactory("P2PEscrowV2");
  const proxy = await upgrades.deployProxy(V2, [owner.address, [admin.address]], { kind: "uups" });
  await proxy.waitForDeployment();
  const escrow = await upgradeToV3(await proxy.getAddress());
  await escrow.connect(owner).setSystemWallet(system.address);

  const token = await deployToken("USDT", 18, [alice, bob]);
  await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
  await escrow.connect(owner).setTokenSymbol(await token.getAddress(), "USDT");

  return { escrow, owner, admin, alice, bob, system, token };
}

async function fundAndOpenV3(ctx, feature, tradeId = ethers.id("v3-trade")) {
  const { escrow, alice, bob, token } = ctx;
  const dep = ethers.parseUnits("1000", 18);
  await token.connect(alice).approve(await escrow.getAddress(), dep);
  await escrow.connect(alice).deposit(await token.getAddress(), dep);
  const v = makeVoucher(alice.address, await token.getAddress());
  const sig = await signVoucher(escrow, alice, v);
  const amt = ethers.parseUnits("100", 18);
  await escrow.connect(bob).openTrade(tradeId, v, sig, amt);
  return { tradeId, amt };
}

describe("FeeManager ⇄ Escrows (A1 live-read + legacySplitEnabled)", () => {
  // ── (6) Storage-layout safety for all three upgrades ──────────────────────
  describe("storage-layout safety (upgrades.validateUpgrade)", () => {
    it("P2PEscrowV2 → P2PEscrowV3 is layout-compatible (no slot collision)", async () => {
      const V2 = await ethers.getContractFactory("P2PEscrowV2");
      const V3 = await ethers.getContractFactory("P2PEscrowV3");
      await upgrades.validateUpgrade(V2, V3, {
        kind: "uups",
        unsafeAllow: ["constructor", "missing-initializer"],
      });
    });

    it("ProductEscrow → ProductEscrowV2 is layout-compatible (no slot collision)", async () => {
      const V1 = await ethers.getContractFactory("ProductEscrow");
      const V2 = await ethers.getContractFactory("ProductEscrowV2");
      await upgrades.validateUpgrade(V1, V2, {
        kind: "uups",
        unsafeAllow: ["constructor", "missing-initializer"],
      });
    });

    it("ServiceContact → ServiceContactV2 is layout-compatible (no slot collision)", async () => {
      const V1 = await ethers.getContractFactory("ServiceContact");
      const V2 = await ethers.getContractFactory("ServiceContactV2");
      await upgrades.validateUpgrade(V1, V2, {
        kind: "uups",
        unsafeAllow: ["constructor", "missing-initializer"],
      });
    });

    it("upgradeProxy on a live proxy preserves state for all three", async () => {
      const [owner, admin] = await ethers.getSigners();

      // P2P
      const P2PV2 = await ethers.getContractFactory("P2PEscrowV2");
      const p2p = await upgrades.deployProxy(P2PV2, [owner.address, [admin.address]], { kind: "uups" });
      await p2p.waitForDeployment();
      const p2pV3 = await upgradeToV3(await p2p.getAddress());
      expect(await p2pV3.getAddress()).to.equal(await p2p.getAddress());
      expect(await p2pV3.feeBps()).to.equal(50n);
      // The `call: initializeV3` in upgradeToV3 sets legacySplitEnabled TRUE at upgrade
      // (D5 / §9-step-1). Without the reinitializer it would have read default zero=false.
      expect(await p2pV3.legacySplitEnabled()).to.equal(true);

      // Product
      const ProdV1 = await ethers.getContractFactory("ProductEscrow");
      const prod = await upgrades.deployProxy(ProdV1, [owner.address, [admin.address]], {
        kind: "uups", unsafeAllow: ["constructor"],
      });
      await prod.waitForDeployment();
      const ProdV2 = await ethers.getContractFactory("ProductEscrowV2");
      const prodV2 = await upgrades.upgradeProxy(await prod.getAddress(), ProdV2, {
        unsafeAllow: ["constructor", "missing-initializer"],
        call: { fn: "initializeV2" },
      });
      expect(await prodV2.getAddress()).to.equal(await prod.getAddress());
      expect(await prodV2.admins(admin.address)).to.equal(true);
      // reinitializer(2) ran via `call` → legacySplitEnabled is TRUE at upgrade (D5 / §9-step-1).
      expect(await prodV2.legacySplitEnabled()).to.equal(true);

      // Service
      const SvcV1 = await ethers.getContractFactory("ServiceContact");
      const svc = await upgrades.deployProxy(SvcV1, [owner.address], {
        kind: "uups", unsafeAllow: ["constructor"],
      });
      await svc.waitForDeployment();
      const SvcV2 = await ethers.getContractFactory("ServiceContactV2");
      const svcV2 = await upgrades.upgradeProxy(await svc.getAddress(), SvcV2, {
        unsafeAllow: ["constructor", "missing-initializer"],
        call: { fn: "initializeV2" },
      });
      expect(await svcV2.getAddress()).to.equal(await svc.getAddress());
      expect(await svcV2.owner()).to.equal(owner.address);
      // reinitializer(2) ran via `call` → legacySplitEnabled is TRUE at upgrade (D5 / §9-step-1).
      expect(await svcV2.legacySplitEnabled()).to.equal(true);
    });
  });

  // ── (1) Upgrade preserves a pre-V3 trade snapshot ─────────────────────────
  describe("P2P: V2→V3 upgrade preserves trade snapshot (back-compat branch)", () => {
    it("test_P2PV2toV3UpgradePreservesTradeSnapshot", async () => {
      // Deploy V2, open a trade at feeBps=50, upgrade to V3, read back + release.
      const { escrow, bob, system, token, tradeId, amt } = await deployP2PV2AndOpen(50);

      // Pre-upgrade snapshot: feeBps == 50.
      const before = await escrow.getTrade(tradeId);
      expect(before.feeBps_).to.equal(50n);

      const v3 = await upgradeToV3(await escrow.getAddress());

      // The pre-upgrade trade still carries feeBps=50 and zero new-snapshot bytes.
      const raw = await v3.trades(tradeId);
      expect(raw.feeBps).to.equal(50n);
      expect(raw.platformFeeBpsSnapshot).to.equal(0n);
      expect(raw.gasFeeBpsSnapshot).to.equal(0n);

      // Release: back-compat branch (snapshots==0 → fall back to feeBps=50).
      const buyerBefore = await token.balanceOf(bob.address);
      const sysBefore = await token.balanceOf(system.address);
      // adminRelease is onlyAdmin; the admin signer (deployP2PV2AndOpen index 1) calls it.
      const [, admin] = await ethers.getSigners();
      await v3.connect(admin).adminRelease(tradeId);

      const fee = (amt * 50n) / 10000n;
      expect(await token.balanceOf(bob.address)).to.equal(buyerBefore + (amt - fee));
      expect(await token.balanceOf(system.address)).to.equal(sysBefore + fee);
    });
  });

  // ── (2) Open AFTER setRate snapshots new rate; later setRate doesn't change in-flight ──
  describe("P2P: live-read snapshot at open, immune to later setRate", () => {
    it("open after setRate snapshots the new rate; a later setRate does NOT move an in-flight trade", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      const fm = await deployFeeManager(owner);
      await ctx.escrow.connect(owner).setFeeManager(await fm.getAddress());

      // FeeManager: p2p_platform = 100 bps, p2p (gas) = 50 bps → total 150 bps.
      await fm.connect(owner).setRate("p2p_platform", "USDT", rate(100, 0, false, true));
      await fm.connect(owner).setRate("p2p", "USDT", rate(50, 0, false, true));

      const { tradeId, amt } = await fundAndOpenV3(ctx, "p2p", ethers.id("inflight"));

      const raw = await ctx.escrow.trades(tradeId);
      expect(raw.platformFeeBpsSnapshot).to.equal(100n);
      expect(raw.gasFeeBpsSnapshot).to.equal(50n);
      expect(raw.feeBps).to.equal(0n); // new V3 trade zeroes the legacy field

      // A LATER rate change must NOT affect the already-open trade.
      await fm.connect(owner).setRate("p2p_platform", "USDT", rate(300, 0, false, true));
      await fm.connect(owner).setRate("p2p", "USDT", rate(0, 0, false, true));

      const stillRaw = await ctx.escrow.trades(tradeId);
      expect(stillRaw.platformFeeBpsSnapshot).to.equal(100n);
      expect(stillRaw.gasFeeBpsSnapshot).to.equal(50n);

      // Release uses the snapshot (150 bps), not the new live rate (300).
      const buyerBefore = await ctx.token.balanceOf(ctx.bob.address);
      const sysBefore = await ctx.token.balanceOf(ctx.system.address);
      await ctx.escrow.connect(ctx.alice).release(tradeId);
      const fee = (amt * 150n) / 10000n;
      expect(await ctx.token.balanceOf(ctx.bob.address)).to.equal(buyerBefore + (amt - fee));
      expect(await ctx.token.balanceOf(ctx.system.address)).to.equal(sysBefore + fee);
    });
  });

  // ── (3) feeManager == 0 snapshots legacy feeBps, not 0 ────────────────────
  describe("P2P: pre-wire (feeManager == 0) snapshots legacy feeBps", () => {
    it("open with feeManager unset snapshots the live legacy feeBps into platformFeeBpsSnapshot (not 0)", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3(); // feeManager NOT set
      expect(await ctx.escrow.feeManager()).to.equal(ethers.ZeroAddress);
      await ctx.escrow.connect(owner).setFeeBps(50);

      const { tradeId, amt } = await fundAndOpenV3(ctx, "p2p", ethers.id("prewire"));
      const raw = await ctx.escrow.trades(tradeId);
      expect(raw.platformFeeBpsSnapshot).to.equal(50n); // legacy feeBps, NOT 0
      expect(raw.gasFeeBpsSnapshot).to.equal(0n);

      const buyerBefore = await ctx.token.balanceOf(ctx.bob.address);
      const sysBefore = await ctx.token.balanceOf(ctx.system.address);
      await ctx.escrow.connect(ctx.alice).release(tradeId);
      const fee = (amt * 50n) / 10000n;
      expect(await ctx.token.balanceOf(ctx.bob.address)).to.equal(buyerBefore + (amt - fee));
      expect(await ctx.token.balanceOf(ctx.system.address)).to.equal(sysBefore + fee);
    });

    it("MCGP token still snapshots 0 (mcgpExempt) in the pre-wire path", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      await ctx.escrow.connect(owner).setMcgpToken(await ctx.token.getAddress());

      const { tradeId } = await fundAndOpenV3(ctx, "p2p", ethers.id("prewire-mcgp"));
      const raw = await ctx.escrow.trades(tradeId);
      expect(raw.platformFeeBpsSnapshot).to.equal(0n);
      expect(raw.gasFeeBpsSnapshot).to.equal(0n);
    });
  });

  // ── Finding #2: FeeManager wired but the rate is UNSET / inactive → fall back, never charge 0 ──
  describe("P2P: feeManager set but rate inactive/unset falls back to legacy feeBps (no silent $0)", () => {
    it("an UNSET key (active==false) snapshots legacy feeBps, not 0 — and releases at it", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      await ctx.escrow.connect(owner).setFeeBps(50);
      const fm = await deployFeeManager(owner);
      // FeeManager wired but NO rate ever seeded for USDT (the §9 cutover-window hazard).
      await ctx.escrow.connect(owner).setFeeManager(await fm.getAddress());

      const { tradeId, amt } = await fundAndOpenV3(ctx, "p2p", ethers.id("inactive-unset"));
      const raw = await ctx.escrow.trades(tradeId);
      // Platform leg falls back to legacy feeBps=50; gas leg has no legacy → 0.
      expect(raw.platformFeeBpsSnapshot).to.equal(50n);
      expect(raw.gasFeeBpsSnapshot).to.equal(0n);

      const buyerBefore = await ctx.token.balanceOf(ctx.bob.address);
      const sysBefore = await ctx.token.balanceOf(ctx.system.address);
      await ctx.escrow.connect(ctx.alice).release(tradeId);
      const fee = (amt * 50n) / 10000n; // NOT zero
      expect(fee).to.be.gt(0n);
      expect(await ctx.token.balanceOf(ctx.bob.address)).to.equal(buyerBefore + (amt - fee));
      expect(await ctx.token.balanceOf(ctx.system.address)).to.equal(sysBefore + fee);
    });

    it("an explicitly inactive rate (active==false, bps>0) is ignored in favor of legacy feeBps", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      await ctx.escrow.connect(owner).setFeeBps(50);
      const fm = await deployFeeManager(owner);
      await ctx.escrow.connect(owner).setFeeManager(await fm.getAddress());
      // A configured-then-disabled row: bps=200 but active=false → must NOT be used.
      await fm.connect(owner).setRate("p2p_platform", "USDT", rate(200, 0, false, false));
      await fm.connect(owner).setRate("p2p", "USDT", rate(123, 0, false, false));

      const { tradeId } = await fundAndOpenV3(ctx, "p2p", ethers.id("inactive-set"));
      const raw = await ctx.escrow.trades(tradeId);
      expect(raw.platformFeeBpsSnapshot).to.equal(50n); // legacy, not the inactive 200
      expect(raw.gasFeeBpsSnapshot).to.equal(0n);        // legacy gas (none), not the inactive 123
    });

    it("a per-leg mix: platform active, gas inactive → platform from FM, gas from legacy (0)", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      await ctx.escrow.connect(owner).setFeeBps(50);
      const fm = await deployFeeManager(owner);
      await ctx.escrow.connect(owner).setFeeManager(await fm.getAddress());
      await fm.connect(owner).setRate("p2p_platform", "USDT", rate(120, 0, false, true));  // active
      await fm.connect(owner).setRate("p2p", "USDT", rate(80, 0, false, false));            // inactive

      const { tradeId } = await fundAndOpenV3(ctx, "p2p", ethers.id("mixed-active"));
      const raw = await ctx.escrow.trades(tradeId);
      expect(raw.platformFeeBpsSnapshot).to.equal(120n); // honored (active)
      expect(raw.gasFeeBpsSnapshot).to.equal(0n);         // inactive → legacy 0, NOT 80
    });
  });

  // ── (5) Escrow MAX_* caps revert a misconfigured snapshot ─────────────────
  describe("P2P: MAX_* caps revert a misconfigured FeeManager snapshot", () => {
    it("a platform bps above MAX_PLATFORM_FEE_BPS (300) reverts at openTrade", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      const fm = await deployFeeManager(owner);
      await ctx.escrow.connect(owner).setFeeManager(await fm.getAddress());

      // 400 bps > escrow cap 300 (but <= FeeManager MAX_BPS 1000, so the FeeManager accepts it).
      await fm.connect(owner).setRate("p2p_platform", "USDT", rate(400, 0, false, true));
      await fm.connect(owner).setRate("p2p", "USDT", rate(0, 0, false, true));

      const dep = ethers.parseUnits("1000", 18);
      await ctx.token.connect(ctx.alice).approve(await ctx.escrow.getAddress(), dep);
      await ctx.escrow.connect(ctx.alice).deposit(await ctx.token.getAddress(), dep);
      const v = makeVoucher(ctx.alice.address, await ctx.token.getAddress());
      const sig = await signVoucher(ctx.escrow, ctx.alice, v);
      await expect(
        ctx.escrow.connect(ctx.bob).openTrade(ethers.id("toohigh"), v, sig, ethers.parseUnits("100", 18))
      ).to.be.revertedWithCustomError(ctx.escrow, "SnapshotFeeTooHigh");
    });

    it("a gas bps above MAX_GAS_FEE_BPS (300) reverts at openTrade", async () => {
      const [owner] = await ethers.getSigners();
      const ctx = await deployP2PV3();
      const fm = await deployFeeManager(owner);
      await ctx.escrow.connect(owner).setFeeManager(await fm.getAddress());

      await fm.connect(owner).setRate("p2p_platform", "USDT", rate(50, 0, false, true));
      await fm.connect(owner).setRate("p2p", "USDT", rate(400, 0, false, true)); // > 300

      const dep = ethers.parseUnits("1000", 18);
      await ctx.token.connect(ctx.alice).approve(await ctx.escrow.getAddress(), dep);
      await ctx.escrow.connect(ctx.alice).deposit(await ctx.token.getAddress(), dep);
      const v = makeVoucher(ctx.alice.address, await ctx.token.getAddress());
      const sig = await signVoucher(ctx.escrow, ctx.alice, v);
      await expect(
        ctx.escrow.connect(ctx.bob).openTrade(ethers.id("gastoohigh"), v, sig, ethers.parseUnits("100", 18))
      ).to.be.revertedWithCustomError(ctx.escrow, "SnapshotFeeTooHigh");
    });
  });

  // ── (4) Product: legacySplitEnabled true reproduces today; false = 100% to owner ──
  describe("ProductEscrowV2: legacySplitEnabled gates the platform-fee split", () => {
    async function setupProduct() {
      const [owner, admin, buyer, seller, upline] = await ethers.getSigners();
      const V1 = await ethers.getContractFactory("ProductEscrow");
      const proxy = await upgrades.deployProxy(V1, [owner.address, [admin.address]], {
        kind: "uups", unsafeAllow: ["constructor"],
      });
      await proxy.waitForDeployment();
      const V2 = await ethers.getContractFactory("ProductEscrowV2");
      const escrow = await upgrades.upgradeProxy(await proxy.getAddress(), V2, {
        unsafeAllow: ["constructor", "missing-initializer"],
        call: { fn: "initializeV2" },
      });
      await escrow.waitForDeployment();
      // The `call: initializeV2` reinitializer sets legacySplitEnabled TRUE at upgrade
      // (the production default). Each test still flips it explicitly to assert both branches.
      const token = await deployToken("USDT", 6, [buyer]);
      await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
      return { escrow, owner, admin, buyer, seller, upline, token };
    }

    // 2% platform fee on $1000 (6-dec) = $20 = 20_000000; cashback 0.5%=$5, upline 0.5%=$5, system=$10.
    const PRODUCT = ethers.parseUnits("1000", 6);
    const SHIPPING = 0n;
    const PLATFORM_FEE = (PRODUCT * 200n) / 10000n; // 2%
    const GAS = 100000n; // $0.10 fallback (feeManager == 0)

    async function createAndDeliver(ctx, orderId) {
      const total = PRODUCT + SHIPPING + PLATFORM_FEE + GAS;
      await ctx.token.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), total);
      await ctx.escrow.connect(ctx.buyer).createOrder(
        orderId, ctx.seller.address, await ctx.token.getAddress(), PRODUCT, SHIPPING, ctx.upline.address
      );
      await ctx.escrow.connect(ctx.seller).markDelivered(orderId);
    }

    it("legacySplitEnabled=true reproduces today's cashback/upline/system split", async () => {
      const ctx = await setupProduct();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(true);
      const orderId = ethers.id("prod-legacy");
      await createAndDeliver(ctx, orderId);

      const buyerBefore = await ctx.token.balanceOf(ctx.buyer.address);
      const uplineBefore = await ctx.token.balanceOf(ctx.upline.address);
      const ownerBefore = await ctx.token.balanceOf(ctx.owner.address);
      const sellerBefore = await ctx.token.balanceOf(ctx.seller.address);

      await ctx.escrow.connect(ctx.buyer).confirmReceipt(orderId);

      const cashback = (PRODUCT * 50n) / 10000n;   // 0.5%
      const upline = (PRODUCT * 50n) / 10000n;     // 0.5%
      const system = PLATFORM_FEE - cashback - upline; // 1%
      expect(await ctx.token.balanceOf(ctx.buyer.address)).to.equal(buyerBefore + cashback);
      expect(await ctx.token.balanceOf(ctx.upline.address)).to.equal(uplineBefore + upline);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerBefore + system);
      expect(await ctx.token.balanceOf(ctx.seller.address)).to.equal(sellerBefore + PRODUCT + SHIPPING);
    });

    it("legacySplitEnabled=false sends 100% of platformFee to owner(); flipping back restores the split", async () => {
      const ctx = await setupProduct();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(false);
      const orderId = ethers.id("prod-100pct");
      await createAndDeliver(ctx, orderId);

      const buyerBefore = await ctx.token.balanceOf(ctx.buyer.address);
      const uplineBefore = await ctx.token.balanceOf(ctx.upline.address);
      const ownerBefore = await ctx.token.balanceOf(ctx.owner.address);
      const sellerBefore = await ctx.token.balanceOf(ctx.seller.address);

      await ctx.escrow.connect(ctx.buyer).confirmReceipt(orderId);

      // 100% of platformFee to owner; buyer + upline get nothing; seller gets product+shipping.
      expect(await ctx.token.balanceOf(ctx.buyer.address)).to.equal(buyerBefore);
      expect(await ctx.token.balanceOf(ctx.upline.address)).to.equal(uplineBefore);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerBefore + PLATFORM_FEE);
      expect(await ctx.token.balanceOf(ctx.seller.address)).to.equal(sellerBefore + PRODUCT + SHIPPING);

      // Flip back to true → split behavior restored on the next order.
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(true);
      const orderId2 = ethers.id("prod-restored");
      await createAndDeliver(ctx, orderId2);
      const buyer2 = await ctx.token.balanceOf(ctx.buyer.address);
      const upline2 = await ctx.token.balanceOf(ctx.upline.address);
      await ctx.escrow.connect(ctx.buyer).confirmReceipt(orderId2);
      const cashback = (PRODUCT * 50n) / 10000n;
      const upl = (PRODUCT * 50n) / 10000n;
      expect(await ctx.token.balanceOf(ctx.buyer.address)).to.equal(buyer2 + cashback);
      expect(await ctx.token.balanceOf(ctx.upline.address)).to.equal(upline2 + upl);
    });

    it("live-read merchant bps + gas flat from FeeManager (and the escrow MAX cap reverts a misconfig)", async () => {
      const ctx = await setupProduct();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(false);
      const fm = await deployFeeManager(ctx.owner);
      await ctx.escrow.connect(ctx.owner).setFeeManager(await fm.getAddress());
      await ctx.escrow.connect(ctx.owner).setTokenSymbol(await ctx.token.getAddress(), "USDT");

      // merchant 300 bps (3%), gas flat $0.25.
      await fm.connect(ctx.owner).setRate("product_merchant", "USDT", rate(300, 0, false, true));
      await fm.connect(ctx.owner).setRate("product", "USDT", rate(0, 250000, false, true));

      const product = ethers.parseUnits("1000", 6);
      const platformFee = (product * 300n) / 10000n; // 3%
      const gas = 250000n;
      const total = product + platformFee + gas;
      const orderId = ethers.id("prod-liveread");
      await ctx.token.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), total);

      const ownerBefore = await ctx.token.balanceOf(ctx.owner.address);
      await ctx.escrow.connect(ctx.buyer).createOrder(
        orderId, ctx.seller.address, await ctx.token.getAddress(), product, 0, ctx.upline.address
      );
      // Gas fee routed to owner immediately at createOrder.
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerBefore + gas);
      const order = await ctx.escrow.orders(orderId);
      expect(order.platformFee).to.equal(platformFee);

      // MAX cap: 400 bps merchant > MAX_PLATFORM_FEE_BPS(300) reverts createOrder.
      await fm.connect(ctx.owner).setRate("product_merchant", "USDT", rate(400, 0, false, true));
      await ctx.token.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), total);
      await expect(
        ctx.escrow.connect(ctx.buyer).createOrder(
          ethers.id("prod-cap"), ctx.seller.address, await ctx.token.getAddress(), product, 0, ctx.upline.address
        )
      ).to.be.revertedWith("Platform fee too high");
    });

    // ── Finding #2: FeeManager wired but rate UNSET / inactive → fall back to legacy, never $0 ──
    it("feeManager set but the rate is unset (active==false) falls back to legacy PLATFORM_FEE_BPS + GAS_FEE", async () => {
      const ctx = await setupProduct();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(false);
      const fm = await deployFeeManager(ctx.owner);
      // Wire the FeeManager but NEVER seed a rate for USDT (the §9 cutover-window hazard).
      await ctx.escrow.connect(ctx.owner).setFeeManager(await fm.getAddress());
      await ctx.escrow.connect(ctx.owner).setTokenSymbol(await ctx.token.getAddress(), "USDT");

      const product = ethers.parseUnits("1000", 6);
      const legacyFee = (product * 200n) / 10000n; // PLATFORM_FEE_BPS = 2%, NOT 0
      const legacyGas = 100000n;                    // GAS_FEE = $0.10, NOT 0
      const total = product + legacyFee + legacyGas;
      const orderId = ethers.id("prod-inactive-fallback");
      await ctx.token.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), total);

      const ownerBefore = await ctx.token.balanceOf(ctx.owner.address);
      await ctx.escrow.connect(ctx.buyer).createOrder(
        orderId, ctx.seller.address, await ctx.token.getAddress(), product, 0, ctx.upline.address
      );
      // Gas fell back to GAS_FEE (routed to owner at createOrder), NOT 0.
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerBefore + legacyGas);
      const order = await ctx.escrow.orders(orderId);
      expect(order.platformFee).to.equal(legacyFee); // legacy 2%, never silent 0
      expect(order.platformFee).to.be.gt(0n);
    });

    it("an explicitly inactive rate (active==false, bps>0) is ignored in favor of legacy", async () => {
      const ctx = await setupProduct();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(false);
      const fm = await deployFeeManager(ctx.owner);
      await ctx.escrow.connect(ctx.owner).setFeeManager(await fm.getAddress());
      await ctx.escrow.connect(ctx.owner).setTokenSymbol(await ctx.token.getAddress(), "USDT");
      // Configured-then-disabled rows: nonzero bps/flat but active=false → must be ignored.
      await fm.connect(ctx.owner).setRate("product_merchant", "USDT", rate(50, 0, false, false));
      await fm.connect(ctx.owner).setRate("product", "USDT", rate(0, 999, false, false));

      const product = ethers.parseUnits("1000", 6);
      const legacyFee = (product * 200n) / 10000n;
      const total = product + legacyFee + 100000n;
      await ctx.token.connect(ctx.buyer).approve(await ctx.escrow.getAddress(), total);
      await ctx.escrow.connect(ctx.buyer).createOrder(
        ethers.id("prod-inactive-set"), ctx.seller.address, await ctx.token.getAddress(), product, 0, ctx.upline.address
      );
      const order = await ctx.escrow.orders(ethers.id("prod-inactive-set"));
      expect(order.platformFee).to.equal(legacyFee); // legacy 2%, not the inactive 50 bps
    });
  });

  // ── (4) Service: legacySplitEnabled gates the 4-way split; feeAmount stays > 0 ──
  describe("ServiceContactV2: legacySplitEnabled gates the 4-way split; feeAmount stays > 0", () => {
    async function setupService() {
      const [owner, caller, provider, upline] = await ethers.getSigners();
      const V1 = await ethers.getContractFactory("ServiceContact");
      const proxy = await upgrades.deployProxy(V1, [owner.address], {
        kind: "uups", unsafeAllow: ["constructor"],
      });
      await proxy.waitForDeployment();
      const V2 = await ethers.getContractFactory("ServiceContactV2");
      const escrow = await upgrades.upgradeProxy(await proxy.getAddress(), V2, {
        unsafeAllow: ["constructor", "missing-initializer"],
        call: { fn: "initializeV2" },
      });
      await escrow.waitForDeployment();
      const token = await deployToken("USDT", 6, [caller]);
      await escrow.connect(owner).setTokenAcceptance(await token.getAddress(), true);
      await escrow.connect(owner).setFeeAmount(await token.getAddress(), 100000n); // $0.10, > MIN_FEE_AMOUNT
      return { escrow, owner, caller, provider, upline, token };
    }

    const FEE = 100000n;

    it("legacySplitEnabled=true reproduces today's 4-way split", async () => {
      const ctx = await setupService();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(true);

      const providerB = await ctx.token.balanceOf(ctx.provider.address);
      const callerB = await ctx.token.balanceOf(ctx.caller.address);
      const uplineB = await ctx.token.balanceOf(ctx.upline.address);
      const ownerB = await ctx.token.balanceOf(ctx.owner.address);

      await ctx.token.connect(ctx.caller).approve(await ctx.escrow.getAddress(), FEE);
      await ctx.escrow.connect(ctx.caller).payContactFee(ctx.provider.address, ctx.upline.address, await ctx.token.getAddress());

      const providerAmt = (FEE * 2500n) / 10000n;
      const callerAmt = (FEE * 1250n) / 10000n;
      const uplineAmt = (FEE * 1250n) / 10000n;
      const systemAmt = FEE - providerAmt - callerAmt - uplineAmt;
      expect(await ctx.token.balanceOf(ctx.provider.address)).to.equal(providerB + providerAmt);
      // caller paid FEE then got callerAmt cashback.
      expect(await ctx.token.balanceOf(ctx.caller.address)).to.equal(callerB - FEE + callerAmt);
      expect(await ctx.token.balanceOf(ctx.upline.address)).to.equal(uplineB + uplineAmt);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerB + systemAmt);
    });

    it("legacySplitEnabled=false sends 100% to owner and does NOT revert; feeAmount stays > 0; flipping back restores", async () => {
      const ctx = await setupService();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(false);

      // feeAmount remains > 0 (the I2 correction).
      expect(await ctx.escrow.feeAmounts(await ctx.token.getAddress())).to.equal(FEE);

      const providerB = await ctx.token.balanceOf(ctx.provider.address);
      const callerB = await ctx.token.balanceOf(ctx.caller.address);
      const uplineB = await ctx.token.balanceOf(ctx.upline.address);
      const ownerB = await ctx.token.balanceOf(ctx.owner.address);

      await ctx.token.connect(ctx.caller).approve(await ctx.escrow.getAddress(), FEE);
      // Must NOT revert when legacySplitEnabled = false.
      await expect(
        ctx.escrow.connect(ctx.caller).payContactFee(ctx.provider.address, ctx.upline.address, await ctx.token.getAddress())
      ).to.emit(ctx.escrow, "ContactFeePaid");

      // 100% to owner; provider/caller-cashback/upline get nothing extra (caller is down the full fee).
      expect(await ctx.token.balanceOf(ctx.provider.address)).to.equal(providerB);
      expect(await ctx.token.balanceOf(ctx.caller.address)).to.equal(callerB - FEE);
      expect(await ctx.token.balanceOf(ctx.upline.address)).to.equal(uplineB);
      expect(await ctx.token.balanceOf(ctx.owner.address)).to.equal(ownerB + FEE);

      // Flip back → split restored.
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(true);
      const provider2 = await ctx.token.balanceOf(ctx.provider.address);
      await ctx.token.connect(ctx.caller).approve(await ctx.escrow.getAddress(), FEE);
      await ctx.escrow.connect(ctx.caller).payContactFee(ctx.provider.address, ctx.upline.address, await ctx.token.getAddress());
      const providerAmt = (FEE * 2500n) / 10000n;
      expect(await ctx.token.balanceOf(ctx.provider.address)).to.equal(provider2 + providerAmt);
    });

    it("payContactFee still requires fee >= MIN_FEE_AMOUNT (feeAmount must not be zeroed)", async () => {
      const ctx = await setupService();
      await ctx.escrow.connect(ctx.owner).setLegacySplitEnabled(false);
      // Owner zeroes the fee (allowed by setFeeAmount) — payContactFee must then revert, proving
      // why §7.3 keeps feeAmount > 0 and disables the split via the bool instead.
      await ctx.escrow.connect(ctx.owner).setFeeAmount(await ctx.token.getAddress(), 0);
      await ctx.token.connect(ctx.caller).approve(await ctx.escrow.getAddress(), FEE);
      await expect(
        ctx.escrow.connect(ctx.caller).payContactFee(ctx.provider.address, ctx.upline.address, await ctx.token.getAddress())
      ).to.be.revertedWith("Fee not configured for token");
    });
  });
});
