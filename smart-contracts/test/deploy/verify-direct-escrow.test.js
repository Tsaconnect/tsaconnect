const { expect } = require("chai");
const hre = require("hardhat");
const { ethers } = hre;
const { verifyDirectEscrow, expectedFromChain, cliInputs, scanLogs } = require("../../scripts/verify-direct-escrow");
const { directFixture, deployerKey } = require("../helpers/direct");

const mine = (n) => hre.network.provider.send("hardhat_mine", ["0x" + n.toString(16)]);

// verify runs the read-back against a fixture's deployment with the expected
// values built exactly as the CLI builds them (from the chains.js direct block).
async function verify(ctx, { fromBlock = ctx.out.deployBlock, chunk, h = hre, create2, chain = ctx.chain, extra = [], tweak } = {}) {
  const c2 = create2 === undefined ? { deployer: ctx.deployer.address, name: ctx.deployOpts.name } : create2 || undefined;
  const expected = expectedFromChain(chain, ctx.roles, [ctx.deployer.address, ...extra], { create2: c2 });
  if (tweak) tweak(expected);
  return verifyDirectEscrow(h, { escrow: ctx.out.escrow, feeManager: ctx.out.feeManager, fromBlock, expected, chunk });
}

// droppingLogs is an hre whose provider silently drops the logs of the given
// transactions — an RPC that loses part of a chunk.
function droppingLogs(txHashes) {
  const p = ethers.provider;
  const provider = {
    getLogs: async (f) => (await p.getLogs(f)).filter((l) => !txHashes.includes(l.transactionHash)),
    getBlockNumber: () => p.getBlockNumber(),
    getCode: (a) => p.getCode(a),
    getStorage: (a, slot) => p.getStorage(a, slot),
  };
  const h = Object.create(hre);
  h.ethers = { ...hre.ethers, provider };
  return h;
}

describe("scripts/verify-direct-escrow", () => {
  it("passes a fresh deployment, with the history spread across several 2,000-block chunks", async () => {
    const ctx = await directFixture();
    await mine(4500);
    const { mismatches, report } = await verify(ctx);
    expect(mismatches).to.deep.equal([]);
    expect(report.join("\n")).to.match(/in 2000-block chunks: escrow 6 logs, FeeManager 2 logs/);
  });

  it("chunk boundaries never drop or duplicate a log", async () => {
    const ctx = await directFixture();
    const esc = await ethers.getContractAt("P2PEscrowV3", ctx.out.escrow);
    const toBlock = await ethers.provider.getBlockNumber();
    const args = { address: ctx.out.escrow, topics: [], fromBlock: ctx.out.deployBlock, toBlock };
    const whole = await scanLogs(ethers.provider, { ...args, chunk: 1_000_000 });
    for (const chunk of [1, 2, 3, 7]) {
      const got = await scanLogs(ethers.provider, { ...args, chunk });
      expect(got.map((l) => `${l.blockNumber}:${l.index}`), `chunk ${chunk}`).to.deep.equal(whole.map((l) => `${l.blockNumber}:${l.index}`));
    }
    expect(whole.length).to.be.greaterThan(0);
    expect(whole.map((l) => esc.interface.parseLog(l)?.name)).to.include("OwnershipTransferred");
  });

  describe("tampered admin set", () => {
    it("an extra admin added after the deploy (not on the forbidden list) is caught by the event scan", async () => {
      const ctx = await directFixture();
      await mine(2500); // the tamper lands in a later chunk than the deploy
      await (await ctx.esc.connect(ctx.ownerWallet).addAdmin(ctx.stranger.address)).wait();
      const { mismatches } = await verify(ctx);
      expect(mismatches.join("\n")).to.match(new RegExp(`not exactly the arbiters \\(extra: ${ctx.stranger.address}`));
    });

    it("a forbidden key made admin is caught by both isAdmin() and the event scan", async () => {
      const ctx = await directFixture();
      await (await ctx.esc.connect(ctx.ownerWallet).addAdmin(ctx.canceller.address)).wait();
      const m = (await verify(ctx)).mismatches.join("\n");
      expect(m).to.match(new RegExp(`forbidden address ${ctx.canceller.address} is an escrow admin`));
      expect(m).to.match(/not exactly the arbiters/);
    });

    it("a removed arbiter is caught", async () => {
      const ctx = await directFixture();
      await (await ctx.esc.connect(ctx.ownerWallet).removeAdmin(ctx.arbB.address)).wait();
      const m = (await verify(ctx)).mismatches.join("\n");
      expect(m).to.match(new RegExp(`arbiter ${ctx.arbB.address} is not an escrow admin`));
      expect(m).to.match(new RegExp(`missing: ${ctx.arbB.address}`));
    });
  });

  describe("tampered values", () => {
    it("payment window, a fee rate, a forwarder, a pause and an extra token are each reported", async () => {
      const ctx = await directFixture();
      const ow = ctx.ownerWallet;
      await (await ctx.esc.connect(ow).setPaymentWindow(600)).wait();
      await (await ctx.fm.connect(ow).setRate("p2p_platform", "USDT", [120, 0, false, true])).wait();
      await (await ctx.esc.connect(ow).setTrustedForwarder(ctx.stranger.address)).wait();
      await (await ctx.esc.connect(ow).pause()).wait();
      await (await ctx.esc.connect(ow).setTokenAcceptance(ctx.chain.tokens.MCGP, true)).wait();
      const m = (await verify(ctx)).mismatches.join("\n");
      expect(m).to.match(/paymentWindow\(\) is not 1800/);
      expect(m).to.match(/p2p_platform\/USDT is \(120/);
      expect(m).to.match(/trustedForwarder\(\) is not zero/);
      expect(m).to.match(/escrow is paused/);
      expect(m).to.match(/non-direct token MCGP is accepted/);
      expect(m).to.match(/accepted tokens from events/);
    });

    it("an owner that moved is reported on the contract that moved", async () => {
      const ctx = await directFixture();
      await (await ctx.fm.connect(ctx.ownerWallet).transferOwnership(ctx.stranger.address)).wait();
      const m = (await verify(ctx)).mismatches.join("\n");
      expect(m).to.match(/FeeManager\.owner\(\) is 0x[0-9a-fA-F]+, want/);
      expect(m).to.not.match(/escrow\.owner\(\) is/);
    });
  });

  it("a scan that misses the deploy events fails instead of passing on empty sets", async () => {
    const ctx = await directFixture();
    await mine(10);
    const late = (await ethers.provider.getBlockNumber()) - 2;
    const m = (await verify(ctx, { fromBlock: late })).mismatches.join("\n");
    expect(m).to.match(/escrow: no deploy-time OwnershipTransferred/);
    expect(m).to.match(/FeeManager: no deploy-time OwnershipTransferred/);
    expect(m).to.match(/no AdminAdded event found/);
  });

  it("expectedFromChain refuses missing expectations", async () => {
    const ctx = await directFixture();
    const c = (d) => ({ ...ctx.chain, direct: { ...ctx.chain.direct, ...d } });
    expect(() => expectedFromChain(c({ arbiters: [] }), ctx.roles)).to.throw(/ARBITER_ADDRESSES/);
    expect(() => expectedFromChain(c({ rates: null }), ctx.roles)).to.throw(/DIRECT_RATES_JSON/);
    expect(() => expectedFromChain(c({}), { ...ctx.roles, owner: null })).to.throw(/role owner/);
    expect(() => expectedFromChain({ ...ctx.chain, direct: undefined }, ctx.roles)).to.throw(/no direct block/);
    const e = expectedFromChain(c({}), ctx.roles, [ctx.deployer.address]);
    expect(e.plan.forbidden).to.include.members([ctx.releaseEoa.address, ctx.sysWallet.address, ctx.deployer.address, ctx.canceller.address]);
    expect(e.plan.systemWallet).to.equal(ctx.sysWallet.address);
  });

  it("the operator made admin is caught (the owner's rule: arbiters only)", async () => {
    const ctx = await directFixture();
    await (await ctx.esc.connect(ctx.ownerWallet).addAdmin(ctx.releaseEoa.address)).wait();
    const m = (await verify(ctx)).mismatches.join("\n");
    expect(m).to.match(new RegExp(`forbidden address ${ctx.releaseEoa.address} is an escrow admin`));
    expect(m).to.match(/not exactly the arbiters/);
  });

  describe("event-history integrity (M-3)", () => {
    it("a break in the OwnershipTransferred chain (a dropped log) is reported", async () => {
      const ctx = await directFixture();
      await (await ctx.fm.connect(ctx.ownerWallet).transferOwnership(ctx.stranger.address)).wait();
      const back = await (await ctx.fm.connect(ctx.stranger).transferOwnership(ctx.ownerWallet.address)).wait();
      expect((await verify(ctx)).mismatches).to.deep.equal([]);
      const firstMove = (await ctx.fm.queryFilter(ctx.fm.filters.OwnershipTransferred(ctx.ownerWallet.address, ctx.stranger.address)))[0];
      const m = (await verify(ctx, { h: droppingLogs([firstMove.transactionHash]) })).mismatches.join("\n");
      expect(m).to.match(new RegExp(`FeeManager: OwnershipTransferred history breaks — transfer 2 is from ${ctx.stranger.address}, but the owner before it was ${ctx.ownerWallet.address}`));
      expect(back.status).to.equal(1);
    });

    it("an AdminRemoved with no AdminAdded before it is reported", async () => {
      const ctx = await directFixture();
      const added = await (await ctx.esc.connect(ctx.ownerWallet).addAdmin(ctx.stranger.address)).wait();
      await (await ctx.esc.connect(ctx.ownerWallet).removeAdmin(ctx.stranger.address)).wait();
      expect((await verify(ctx)).mismatches).to.deep.equal([]);
      const m = (await verify(ctx, { h: droppingLogs([added.hash]) })).mismatches.join("\n");
      expect(m).to.match(new RegExp(`AdminRemoved\\(${ctx.stranger.address}\\) at block \\d+ with no AdminAdded before it`));
    });
  });

  describe("identity of the contracts (M-4)", () => {
    it("an escrow that is not the predicted CREATE2 address, or no deployer to predict it from", async () => {
      const ctx = await directFixture();
      const m = (await verify(ctx, { create2: { deployer: ctx.stranger.address, name: ctx.deployOpts.name } })).mismatches.join("\n");
      expect(m).to.match(/is not the predicted CREATE2 address/);
      const wrongName = (await verify(ctx, { create2: { deployer: ctx.deployer.address, name: "direct-p2p:other" } })).mismatches.join("\n");
      expect(wrongName).to.match(/is not the predicted CREATE2 address/);
      const none = (await verify(ctx, { create2: null })).mismatches.join("\n");
      expect(none).to.match(/cannot check the escrow is the predicted CREATE2 address — set DET_DEPLOYER/);
    });

    it("the escrow implementation defaults to the deterministic one, and a different one is reported", async () => {
      const ctx = await directFixture();
      const m = (await verify(ctx, { tweak: (e) => { e.impls = { escrow: ctx.stranger.address } } })).mismatches.join("\n");
      expect(m).to.match(new RegExp(`escrow implementation is 0x[0-9a-fA-F]{40}, expected ${ctx.stranger.address}`));
    });

    it("a FeeManager that is not an ERC1967 proxy is reported", async () => {
      const ctx = await directFixture();
      const impl = await hre.upgrades.erc1967.getImplementationAddress(ctx.out.feeManager);
      const expected = expectedFromChain(ctx.chain, ctx.roles, [ctx.deployer.address], { create2: { deployer: ctx.deployer.address, name: ctx.deployOpts.name } });
      const { mismatches } = await verifyDirectEscrow(hre, { escrow: ctx.out.escrow, feeManager: impl, fromBlock: ctx.out.deployBlock, expected });
      expect(mismatches.join("\n")).to.match(new RegExp(`FeeManager ${impl} is not an OpenZeppelin v5 ERC1967Proxy`));
    });

    it("the main escrow's FeeManager (ledger) is refused as the direct one", async () => {
      const ctx = await directFixture();
      const e = expectedFromChain(ctx.chain, ctx.roles, [ctx.deployer.address], {
        create2: { deployer: ctx.deployer.address, name: ctx.deployOpts.name },
        ledger: { FeeManager: { proxy: ctx.out.feeManager } },
      });
      expect(e.foreignFeeManagers).to.deep.equal([ctx.out.feeManager]);
      const { mismatches } = await verifyDirectEscrow(hre, { escrow: ctx.out.escrow, feeManager: ctx.out.feeManager, fromBlock: ctx.out.deployBlock, expected: e });
      expect(mismatches.join("\n")).to.match(/is the main escrow's FeeManager/);
    });
  });

  describe("routine deploy keys (I-1)", () => {
    it("an owner that is a forbidden / routine key is reported", async () => {
      const ctx = await directFixture();
      const m = (await verify(ctx, { extra: [ctx.ownerWallet.address] })).mismatches.join("\n");
      expect(m).to.match(new RegExp(`the owner ${ctx.ownerWallet.address} is a forbidden / routine deploy key`));
    });

    it("the CLI forbids the .env DEPLOYER_PRIVATE_KEY address and reads deployer + name from the ledger", async () => {
      const ctx = await directFixture();
      const routine = new ethers.Wallet(deployerKey(9)).address;
      const saved = process.env.DEPLOYER_PRIVATE_KEY;
      process.env.DEPLOYER_PRIVATE_KEY = deployerKey(9);
      try {
        const ledger = {
          DirectP2PEscrowV3: { proxy: ctx.out.escrow, impl: ctx.out.impls.escrow, name: ctx.deployOpts.name, deployer: ctx.deployer.address, deployBlock: ctx.out.deployBlock },
          DirectFeeManager: { proxy: ctx.out.feeManager, impl: ctx.out.impls.FeeManager },
        };
        const inputs = cliInputs(hre, { cfg: ctx.chain, ledger, env: {}, roles: ctx.roles });
        expect(inputs.expected.plan.forbidden).to.include.members([routine, ctx.deployer.address]);
        expect(inputs.expected.create2).to.deep.equal({ deployer: ctx.deployer.address, name: ctx.deployOpts.name });
        expect((await verifyDirectEscrow(hre, inputs)).mismatches).to.deep.equal([]);
      } finally {
        if (saved === undefined) delete process.env.DEPLOYER_PRIVATE_KEY; else process.env.DEPLOYER_PRIVATE_KEY = saved;
      }
    });
  });

  it("the canonical operator / treasury and DIRECT_FORBIDDEN_ADMINS are always forbidden (M-1)", async () => {
    const ctx = await directFixture();
    const { CANONICAL_HOT_KEYS } = require("../../deploy/chains");
    const e = expectedFromChain(ctx.chain, ctx.roles);
    expect(e.plan.forbidden).to.include.members([CANONICAL_HOT_KEYS.operator, CANONICAL_HOT_KEYS.treasury, ...ctx.chain.direct.forbiddenAdmins]);
  });
});
