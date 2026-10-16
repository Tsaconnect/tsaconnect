const { expect } = require("chai");
const hre = require("hardhat");
const { ethers } = hre;
const { deploy, parseRates } = require("../../deploy/p2p-direct");
const det = require("../../deploy/deterministic-escrow");
const create2 = require("../../deploy/create2");
const { CANONICAL_HOT_KEYS } = require("../../deploy/chains");
const { DIRECT_WINDOW, directChain, directFixture, deployerKey, depositAndSign } = require("../helpers/direct");

// refuses runs a deploy that must be refused before any contract exists: only
// the three mock-token deploys of directChain may have been sent.
async function refuses(direct, pattern, { top, opts } = {}) {
  const [deployer] = await ethers.getSigners();
  const before = await ethers.provider.getTransactionCount(deployer.address);
  const ctx = await directChain(direct, top, opts);
  await expect(deploy(hre, ctx.deployOpts)).to.be.rejectedWith(pattern);
  expect(await ethers.provider.getTransactionCount(deployer.address)).to.equal(before + 3);
  const p = await det.plan(hre, { deployer: deployer.address, admins: ctx.chain.direct.arbiters.length ? ctx.chain.direct.arbiters : [deployer.address], name: ctx.deployOpts.name });
  expect(await create2.hasCode(ethers.provider, p.proxy)).to.equal(false);
}

describe("deploy/p2p-direct", () => {
  it("wires exactly: arbiters only, rates, 30m window, no forwarder, direct tokens only, owner last", async () => {
    const ctx = await directFixture();
    const { esc, fm, chain } = ctx;
    expect(await esc.owner()).to.equal(ctx.ownerWallet.address);
    expect(await fm.owner()).to.equal(ctx.ownerWallet.address);
    expect(ctx.out.owner).to.equal(ctx.ownerWallet.address);
    expect(await esc.feeManager()).to.equal(ctx.out.feeManager);
    expect(await esc.paymentWindow()).to.equal(BigInt(DIRECT_WINDOW));
    expect(await esc.trustedForwarder()).to.equal(ethers.ZeroAddress);
    expect(await esc.permit2()).to.equal(chain.permit2);
    expect(await esc.paused()).to.equal(false);
    expect(await esc.systemWallet()).to.equal(ctx.sysWallet.address);
    for (const a of [ctx.arbA, ctx.arbB]) expect(await esc.isAdmin(a.address)).to.equal(true);
    for (const f of [ctx.deployer, ctx.releaseEoa, ctx.sysWallet, ctx.canceller, ctx.sponsor, ctx.stranger]) {
      expect(await esc.isAdmin(f.address), f.address).to.equal(false);
    }
    for (const sym of ["USDT", "USDC"]) {
      expect(await esc.acceptedTokens(chain.tokens[sym])).to.equal(true);
      expect(await esc.tokenSymbol(chain.tokens[sym])).to.equal(sym);
      const [pBps, , , pActive] = await fm.rateFor("p2p_platform", sym);
      const [gBps, , , gActive] = await fm.rateFor("p2p", sym);
      expect([pBps, pActive, gBps, gActive]).to.deep.equal([100n, true, 50n, true]);
    }
    expect(await esc.acceptedTokens(chain.tokens.MCGP)).to.equal(false);
    expect(await esc.mcgpToken()).to.equal(ethers.ZeroAddress);
  });

  describe("owner = deployer (DIRECT_OWNER_IS_DEPLOYER, decision 2026-10-16)", () => {
    it("the deployer keeps both contracts, is an implicit admin, and every other hot key stays out", async () => {
      const s = await ethers.getSigners();
      const ctx = await directFixture({}, { deploy: { ownerIsDeployer: true, ownerConfirm: s[0].address } });
      expect(ctx.out.owner).to.equal(ctx.deployer.address);
      expect(await ctx.esc.owner()).to.equal(ctx.deployer.address);
      expect(await ctx.fm.owner()).to.equal(ctx.deployer.address);
      expect(await ctx.esc.isAdmin(ctx.deployer.address)).to.equal(true);
      expect(await ctx.esc.admins(ctx.deployer.address)).to.equal(false);
      for (const a of [ctx.arbA, ctx.arbB]) expect(await ctx.esc.isAdmin(a.address)).to.equal(true);
      for (const f of [ctx.releaseEoa, ctx.sysWallet, ctx.canceller, ctx.sponsor, ctx.ownerWallet]) {
        expect(await ctx.esc.isAdmin(f.address), f.address).to.equal(false);
      }
    });
    it("still needs DET_OWNER_CONFIRM, and it must name the deployer", async () => {
      const s = await ethers.getSigners();
      await refuses({}, /DET_OWNER_CONFIRM/, { opts: { ownerIsDeployer: true, ownerConfirm: null } });
      await refuses({}, /DET_OWNER_CONFIRM/, { opts: { ownerIsDeployer: true, ownerConfirm: s[5].address } });
    });
  });

  it("lands at the deterministic address with the arbiters in initialize(), never the operator", async () => {
    const ctx = await directFixture();
    const p = await det.plan(hre, { deployer: ctx.deployer.address, admins: [ctx.arbA.address, ctx.arbB.address], name: ctx.deployOpts.name });
    expect(ctx.out.escrow).to.equal(p.proxy);
    expect(p.initArgs.admins).to.deep.equal([ctx.arbA.address, ctx.arbB.address]);
    const operatorPlan = await det.plan(hre, { deployer: ctx.deployer.address, operator: ctx.releaseEoa.address, name: ctx.deployOpts.name });
    expect(ctx.out.escrow).to.not.equal(operatorPlan.proxy);
    expect(await hre.upgrades.erc1967.getImplementationAddress(ctx.out.escrow)).to.equal(p.impl);
    expect(await ctx.esc.admins(ctx.releaseEoa.address)).to.equal(false);
  });

  it("its FeeManager is its own, never another escrow's", async () => {
    const a = await directFixture();
    const b = await directFixture();
    expect(a.out.feeManager).to.not.equal(b.out.feeManager);
    expect(await a.esc.feeManager()).to.equal(a.out.feeManager);
  });

  it("a re-run on a handed-over escrow runs the full read-back, sends nothing and deploys no FeeManager", async () => {
    const ctx = await directFixture();
    const nonce = await ethers.provider.getTransactionCount(ctx.deployer.address);
    const rerun = { ...ctx.deployOpts, deployerKey: undefined, deployer: ctx.deployer.address, feeManager: ctx.out.feeManager, deployBlock: ctx.out.deployBlock };
    const out = await deploy(hre, rerun);
    expect(out.reused).to.equal(true);
    expect(out.escrow).to.equal(ctx.out.escrow);
    expect(out.feeManager).to.equal(ctx.out.feeManager);
    expect(await ethers.provider.getTransactionCount(ctx.deployer.address)).to.equal(nonce);
  });

  it("a re-run on a handed-over escrow fails on a read-back mismatch, or when it cannot read back", async () => {
    const ctx = await directFixture();
    const rerun = { ...ctx.deployOpts, deployerKey: undefined, deployer: ctx.deployer.address, feeManager: ctx.out.feeManager, deployBlock: ctx.out.deployBlock };
    await expect(deploy(hre, { ...rerun, feeManager: undefined })).to.be.rejectedWith(/already handed over — set DIRECT_FEE_MANAGER_ADDRESS and DIRECT_DEPLOY_BLOCK/);
    await expect(deploy(hre, { ...rerun, deployBlock: undefined })).to.be.rejectedWith(/already handed over — set DIRECT_FEE_MANAGER_ADDRESS and DIRECT_DEPLOY_BLOCK/);
    // An admin added after the handover that no isAdmin() list names: only the event scan sees it.
    await (await ctx.esc.connect(ctx.ownerWallet).addAdmin(ctx.stranger.address)).wait();
    await expect(deploy(hre, rerun)).to.be.rejectedWith(/already handed over but its read-back FAILED: .*not exactly the arbiters/);
  });

  it("the FeeManager is deployed and owned by the cold deploy key, never the routine signer", async () => {
    const s = await ethers.getSigners();
    const cold = new ethers.Wallet(deployerKey(12));
    const routineNonce = await ethers.provider.getTransactionCount(s[0].address);
    const ctx = await directFixture({}, { deploy: { deployerKey: deployerKey(12) } });
    // directChain's three mock tokens are the routine signer's only transactions.
    expect(await ethers.provider.getTransactionCount(s[0].address)).to.equal(routineNonce + 3);
    const fmLogs = await ctx.fm.queryFilter(ctx.fm.filters.OwnershipTransferred(), ctx.out.deployBlock);
    expect(fmLogs.map((l) => [l.args.previousOwner, l.args.newOwner])).to.deep.equal([
      [ethers.ZeroAddress, cold.address], [cold.address, ctx.ownerWallet.address],
    ]);
    expect(await ctx.fm.owner()).to.equal(ctx.ownerWallet.address);
  });

  it("a trade on the deployed escrow snapshots the configured live rates", async () => {
    const ctx = await directFixture();
    const seller = ctx.stranger, buyer = ctx.sponsor;
    await ctx.usdt.mint(seller.address, ethers.parseUnits("100", 18));
    const voucher = await depositAndSign({ esc: ctx.esc, usdt: ctx.usdt }, seller, ethers.parseUnits("100", 18));
    const id = ethers.id("direct-rate-snapshot");
    await (await ctx.esc.connect(buyer).openTrade(id, voucher.v, voucher.sig, ethers.parseUnits("10", 18))).wait();
    const t = await ctx.esc.trades(id);
    expect(t.platformFeeBpsSnapshot).to.equal(100n);
    expect(t.gasFeeBpsSnapshot).to.equal(50n);
  });

  describe("refusals (nothing deployed)", () => {
    it("an empty arbiter list", async () => {
      await refuses({ arbiters: [] }, /ARBITER_ADDRESSES is empty/);
    });
    it("a duplicate arbiter", async () => {
      const [, a] = await ethers.getSigners();
      await refuses({ arbiters: [a.address, a.address.toLowerCase()] }, /duplicate/);
    });
    it("an arbiter in DIRECT_FORBIDDEN_ADMINS (the canceller)", async () => {
      const s = await ethers.getSigners();
      await refuses({ arbiters: [s[1].address, s[6].address] }, /arbiter .* is in DIRECT_FORBIDDEN_ADMINS/);
    });
    it("an arbiter that is the operator, the treasury or the deployer", async () => {
      const s = await ethers.getSigners();
      await refuses({ arbiters: [s[1].address, s[4].address] }, /admin .* is the operator/);
      await refuses({ arbiters: [s[3].address] }, /admin .* is the treasury/);
      await refuses({ arbiters: [s[0].address] }, /admin .* is the deployer/);
    });
    it("an arbiter that is the canonical operator / treasury, whatever the roles say", async () => {
      const s = await ethers.getSigners();
      await refuses({ arbiters: [s[1].address, CANONICAL_HOT_KEYS.operator] }, /arbiter .* is the canonical operator/);
      await refuses({ arbiters: [CANONICAL_HOT_KEYS.treasury] }, /arbiter .* is the canonical treasury/);
    });
    it("an owner that is the canonical operator or treasury", async () => {
      for (const hot of [CANONICAL_HOT_KEYS.operator, CANONICAL_HOT_KEYS.treasury]) {
        await refuses({}, /owner .* is a forbidden admin/, { opts: { roles: { ...(await directChain()).roles, owner: hot }, ownerConfirm: hot } });
      }
    });
    it("an arbiter that is a routine deploy key", async () => {
      const s = await ethers.getSigners();
      await refuses({ arbiters: [s[2].address] }, /admin .* is the a routine deploy key/, { opts: { routineAddresses: [s[2].address] } });
    });
    it("an owner that is the routine .env DEPLOYER_PRIVATE_KEY (I-1 probe: refused before any transaction)", async () => {
      const s = await ethers.getSigners();
      const saved = process.env.DEPLOYER_PRIVATE_KEY;
      process.env.DEPLOYER_PRIVATE_KEY = deployerKey(8); // s[8] is now "the routine deploy key"
      try {
        await refuses({}, /DET_OWNER \(the PM\) is a routine deploy key \(\.env DEPLOYER_PRIVATE_KEY/, {
          opts: { roles: { ...(await directChain()).roles, owner: s[8].address }, ownerConfirm: s[8].address },
        });
      } finally {
        if (saved === undefined) delete process.env.DEPLOYER_PRIVATE_KEY; else process.env.DEPLOYER_PRIVATE_KEY = saved;
      }
    });
    it("an owner that is a routine deploy key or the routine hardhat signer", async () => {
      const s = await ethers.getSigners();
      await refuses({}, /DET_OWNER \(the PM\) is a routine deploy key/, {
        opts: { roles: { ...(await directChain()).roles, owner: s[8].address }, ownerConfirm: s[8].address, routineAddresses: [s[8].address] },
      });
      // The cold key is #12 here, so owner = signer #0 is "the routine hardhat signer", not the deployer.
      await refuses({}, /DET_OWNER \(the PM\) is the routine hardhat signer/, {
        opts: { roles: { ...(await directChain()).roles, owner: s[0].address }, ownerConfirm: s[0].address, deployerKey: deployerKey(12) },
      });
    });
    it("a DET_NAME that is not a direct-p2p:* salt", async () => {
      await refuses({}, /DET_NAME operator-p2p:v1 is not a direct-p2p:\* salt/, { opts: { name: det.DEFAULT_NAME } });
    });
    it("owner == deployer", async () => {
      const s = await ethers.getSigners();
      await refuses({}, /DET_OWNER \(the PM\) is the deployer/, { opts: { roles: { owner: s[0].address, operator: s[4].address, treasury: s[3].address }, ownerConfirm: s[0].address } });
    });
    it("a missing or mismatched DET_OWNER_CONFIRM", async () => {
      const s = await ethers.getSigners();
      await refuses({}, /DET_OWNER_CONFIRM/, { opts: { ownerConfirm: null } });
      await refuses({}, /DET_OWNER_CONFIRM/, { opts: { ownerConfirm: s[8].address } });
    });
    it("no deploy key", async () => {
      const s = await ethers.getSigners();
      await refuses({}, /needs DET_DEPLOYER_PRIVATE_KEY/, { opts: { deployerKey: undefined, deployer: s[0].address } });
    });
    it("an owner in DIRECT_FORBIDDEN_ADMINS (isAdmin(owner) is always true)", async () => {
      const s = await ethers.getSigners();
      await refuses({ forbiddenAdmins: [s[5].address] }, /owner .* is a forbidden admin/);
    });
    it("missing or out-of-range rates", async () => {
      await refuses({ rates: null }, /DIRECT_RATES_JSON is required/);
      await refuses({ rates: "{not json" }, /not valid JSON/);
      await refuses({ rates: JSON.stringify({ p2p_platform: { bps: 100 } }) }, /missing the p2p rate/);
      await refuses({ rates: JSON.stringify({ p2p_platform: { bps: 301 }, p2p: { bps: 0 } }) }, /p2p_platform.bps/);
    });
    it("a configured forwarder", async () => {
      await refuses({}, /forwarder/, { top: { forwarder: "0x0000000000000000000000000000000000000001" } });
    });
  });

  describe("asserts catch a tampered value before the handover", () => {
    async function tampered(afterWire, pattern, beforeHandover) {
      let err;
      try {
        await directFixture({}, { afterWire, beforeHandover });
      } catch (e) {
        err = e;
      }
      expect(err, "a tampered value must fail the assertions").to.not.equal(undefined);
      expect(err.message).to.match(pattern);
      const [deployer] = await ethers.getSigners();
      expect(err.deployed.escrow).to.be.a("string");
      const esc = await ethers.getContractAt("P2PEscrowV3", err.deployed.escrow);
      const fm = await ethers.getContractAt("FeeManager", err.deployed.feeManager);
      expect(await esc.owner()).to.equal(deployer.address);
      expect(await fm.owner()).to.equal(deployer.address);
    }

    it("payment window", async () => {
      await tampered(({ esc }) => esc.setPaymentWindow(600).then((tx) => tx.wait()), /paymentWindow\(\) is not 1800/);
    });
    it("a fee rate", async () => {
      await tampered(({ fm }) => fm.setRate("p2p", "USDC", [51, 0, false, true]).then((tx) => tx.wait()), /p2p\/USDC is \(51/);
    });
    it("a forbidden admin", async () => {
      const s = await ethers.getSigners();
      await tampered(({ esc }) => esc.addAdmin(s[6].address).then((tx) => tx.wait()), /forbidden address .* is an escrow admin/);
    });
    it("a forwarder", async () => {
      await tampered(({ esc }) => esc.setTrustedForwarder(ethers.Wallet.createRandom().address).then((tx) => tx.wait()), /trustedForwarder/);
    });
    it("an extra, non-forbidden admin is caught by the pre-handover event scan (I-2)", async () => {
      const s = await ethers.getSigners();
      await tampered(
        ({ esc }) => esc.addAdmin(s[8].address).then((tx) => tx.wait()),
        /read-back before the handover FAILED: .*not exactly the arbiters \(extra: 0x/,
      );
    });
    it("an admin added then removed is still proven by the scan, and the handover proceeds", async () => {
      const s = await ethers.getSigners();
      const ctx = await directFixture({}, {
        afterWire: async ({ esc }) => { await (await esc.addAdmin(s[8].address)).wait(); await (await esc.removeAdmin(s[8].address)).wait(); },
      });
      expect(await ctx.esc.owner()).to.equal(ctx.ownerWallet.address);
    });
    it("an owner that became a routine key during the run is refused right before the handover (I-1 re-check)", async () => {
      const saved = process.env.DEPLOYER_PRIVATE_KEY;
      try {
        await tampered(null, /DET_OWNER \(the PM\) is a routine deploy key/, async () => {
          process.env.DEPLOYER_PRIVATE_KEY = deployerKey(5); // the owner (signer #5)
        });
      } finally {
        if (saved === undefined) delete process.env.DEPLOYER_PRIVATE_KEY; else process.env.DEPLOYER_PRIVATE_KEY = saved;
      }
    });
  });

  it("parseRates fills defaults and refuses inactive rows and unknown features", () => {
    expect(parseRates({ p2p_platform: { bps: 100 }, p2p: { bps: 0 } })).to.deep.equal({
      p2p_platform: { bps: 100, flatUSD: 0, mcgpExempt: false, active: true },
      p2p: { bps: 0, flatUSD: 0, mcgpExempt: false, active: true },
    });
    expect(() => parseRates({ p2p_platform: { bps: 1, active: false }, p2p: { bps: 0 } })).to.throw(/active must be true/);
    expect(() => parseRates({ p2p_platform: { bps: 1 }, p2p: { bps: 0 }, swap: { bps: 1 } })).to.throw(/unknown feature/);
    expect(() => parseRates({ p2p_platform: { bps: 300 }, p2p: { bps: 201 } })).to.throw(/exceeds 500/);
  });

  it("parseRates bounds flatUSD to a safe integer that fits uint64", () => {
    const r = (flatUSD) => parseRates({ p2p_platform: { bps: 1, flatUSD }, p2p: { bps: 0 } });
    expect(r(100000).p2p_platform.flatUSD).to.equal(100000);
    expect(r(Number.MAX_SAFE_INTEGER).p2p_platform.flatUSD).to.equal(Number.MAX_SAFE_INTEGER);
    for (const bad of [2 ** 64, 1e20, Number.MAX_SAFE_INTEGER + 1, -1, 1.5]) {
      expect(() => r(bad), String(bad)).to.throw(/flatUSD must be a non-negative safe integer that fits uint64/);
    }
    expect(() => parseRates('{"p2p_platform":{"bps":1,"flatUSD":18446744073709551616},"p2p":{"bps":0}}')).to.throw(/fits uint64/);
  });
});
