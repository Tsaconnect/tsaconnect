// smart-contracts/test/deploy/deterministic-escrow.test.js
//
// Two (three) separate in-process hardhat chains with different chain ids, each
// with the REAL Arachnid factory installed from its presigned tx, must put the
// deterministic P2PEscrowV3 at the same implementation + proxy address.
const { expect } = require("chai");
const fs = require("fs");
const os = require("os");
const path = require("path");
const hre = require("hardhat");
const { deploy, plan } = require("../../deploy/deterministic-escrow");
const create2 = require("../../deploy/create2");
const { CANONICAL_PERMIT2 } = require("../../deploy/chains");

// newChain builds a second hardhat-network provider with its own chain id and
// an hre view (ethers + the upgrades functions the script uses) bound to it.
async function newChain(chainId) {
  const { createProvider } = require("hardhat/internal/core/providers/construction");
  const { HardhatEthersProvider } = require("@nomicfoundation/hardhat-ethers/internal/hardhat-ethers-provider");
  const helpers = require("@nomicfoundation/hardhat-ethers/internal/helpers");
  const { makeForceImport } = require("@openzeppelin/hardhat-upgrades/dist/force-import");
  const { makeUpgradeProxy } = require("@openzeppelin/hardhat-upgrades/dist/upgrade-proxy");
  const { getImplementationAddress } = require("@openzeppelin/upgrades-core");

  const netCfg = { ...hre.config.networks.hardhat, chainId };
  const provider = await createProvider({ ...hre.config, networks: { ...hre.config.networks, hardhat: netCfg } }, "hardhat", hre.artifacts);
  const h = Object.create(hre);
  h.network = { name: "hardhat", config: netCfg, provider };
  h.ethers = {
    ...hre.ethers,
    provider: new HardhatEthersProvider(provider, "hardhat"),
    getSigners: () => helpers.getSigners(h),
    getContractFactory: (...a) => helpers.getContractFactory(h, ...a),
    getContractAt: (...a) => helpers.getContractAt(h, ...a),
  };
  h.upgrades = {
    forceImport: makeForceImport(h),
    upgradeProxy: makeUpgradeProxy(h, false),
    erc1967: { getImplementationAddress: (a) => getImplementationAddress(provider, a) },
  };
  return h;
}

async function chainFor(h) {
  const Mock = await h.ethers.getContractFactory("MockERC20");
  const mk = async (sym) => (await (await Mock.deploy(`Mock ${sym}`, sym, 18)).waitForDeployment()).getAddress();
  return { tokens: { USDT: await mk("USDT"), USDC: await mk("USDC"), MCGP: null }, permit2: CANONICAL_PERMIT2, forwarder: null };
}

describe("deploy/deterministic-escrow", function () {
  this.timeout(120000);
  let manifestPath, roles, opts, A, B;

  before(async () => {
    manifestPath = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "det-")), "deterministic.json");
    const [, operator, treasury, , , pm] = await hre.ethers.getSigners();
    roles = { owner: pm.address, operator: operator.address, treasury: treasury.address };
    opts = { roles, ownerConfirm: pm.address, manifestPath, paymentWindowSec: 3600, feeBps: 0 };
    A = { h: hre };
    B = { h: await newChain(31338) };
    for (const c of [A, B]) {
      c.chainId = (await c.h.ethers.provider.getNetwork()).chainId;
      c.chain = await chainFor(c.h);
      // Per-chain setup differs (B accepts USDT only); the addresses must not.
      c.out = await deploy(c.h, { ...opts, chain: c.chain, tokens: c === B ? ["USDT"] : undefined });
      c.esc = await c.h.ethers.getContractAt("P2PEscrowV3", c.out.proxy);
    }
  });

  it("lands the same implementation and proxy on two chains with different chain ids", async () => {
    expect(A.chainId).to.not.equal(B.chainId);
    expect(B.out.impl).to.equal(A.out.impl);
    expect(B.out.proxy).to.equal(A.out.proxy);
    const [deployer] = await hre.ethers.getSigners();
    const p = await plan(hre, { deployer: deployer.address, operator: roles.operator, name: "direct-p2p:v1" });
    expect(A.out.proxy).to.equal(p.proxy);
    expect(A.out.impl).to.equal(p.impl);
    for (const c of [A, B]) {
      expect(await c.h.upgrades.erc1967.getImplementationAddress(c.out.proxy)).to.equal(A.out.impl);
      expect(await create2.hasCode(c.h.ethers.provider, create2.FACTORY)).to.equal(true);
    }
  });

  it("wires each chain and hands ownership to the PM", async () => {
    for (const c of [A, B]) {
      expect(await c.esc.owner()).to.equal(roles.owner);
      expect(await c.esc.isAdmin(roles.operator)).to.equal(true);
      expect(await c.esc.systemWallet()).to.equal(roles.treasury);
      expect(await c.esc.paymentWindow()).to.equal(3600n);
      expect(await c.esc.feeBps()).to.equal(0n);
      expect(await c.esc.permit2()).to.equal(CANONICAL_PERMIT2);
      expect(await c.esc.acceptedTokens(c.chain.tokens.USDT)).to.equal(true);
      expect(await c.esc.tokenSymbol(c.chain.tokens.USDT)).to.equal("USDT");
    }
    // Tokens are per chain: A accepts every configured token, B only USDT.
    expect(await A.esc.acceptedTokens(A.chain.tokens.USDC)).to.equal(true);
    expect(await B.esc.acceptedTokens(B.chain.tokens.USDC)).to.equal(false);
  });

  it("records the salt in the manifest with both chains", async () => {
    const m = JSON.parse(fs.readFileSync(manifestPath, "utf8"))["tsa:P2PEscrowV3:proxy:direct-p2p:v1"];
    expect(m.proxy).to.equal(A.out.proxy);
    expect(m.impl).to.equal(A.out.impl);
    expect(m.initArgs.admins).to.deep.equal([roles.operator]);
    expect(Object.keys(m.networks)).to.include.members([String(A.chainId), String(B.chainId)]);
  });

  it("re-running on a handed-over chain verifies without sending anything", async () => {
    const [deployer] = await hre.ethers.getSigners();
    const nonce = await hre.ethers.provider.getTransactionCount(deployer.address);
    const out = await deploy(hre, { ...opts, chain: A.chain, ownerConfirm: null });
    expect(out.reused).to.equal(true);
    expect(out.proxy).to.equal(A.out.proxy);
    expect(await hre.ethers.provider.getTransactionCount(deployer.address)).to.equal(nonce);
  });

  it("refuses init args that differ from the manifest before sending anything", async () => {
    const C = await newChain(31339);
    const chain = await chainFor(C);
    const [deployer, , , , , , stranger] = await C.ethers.getSigners();
    const nonce = await C.ethers.provider.getTransactionCount(deployer.address);
    await expect(deploy(C, { ...opts, chain, roles: { ...roles, operator: stranger.address } }))
      .to.be.rejectedWith(/refusing.*would not land at the recorded/s);
    expect(await C.ethers.provider.getTransactionCount(deployer.address)).to.equal(nonce);
  });

  it("refuses without DET_OWNER_CONFIRM, before deploying anything", async () => {
    const C = await newChain(31340);
    const chain = await chainFor(C);
    await expect(deploy(C, { ...opts, chain, ownerConfirm: null })).to.be.rejectedWith(/DET_OWNER_CONFIRM/);
    expect(await create2.hasCode(C.ethers.provider, A.out.proxy)).to.equal(false);
  });

  it("refuses a hot server key (operator or treasury) as the deployer, before deploying anything", async () => {
    const C = await newChain(31341);
    const chain = await chainFor(C);
    const [deployer] = await C.ethers.getSigners();
    for (const role of ["operator", "treasury"]) {
      const hot = { ...roles, [role]: deployer.address };
      await expect(deploy(C, { ...opts, roles: hot, chain })).to.be.rejectedWith(new RegExp(`deployer .* is the ${role}`));
    }
  });

  it("a front-run CREATE2 only pays our gas: the deployer still owns and wires the proxy", async () => {
    const C = await newChain(31341);
    const chain = await chainFor(C);
    const [deployer, , , , , , stranger] = await C.ethers.getSigners();
    const p = await plan(C, { deployer: deployer.address, operator: roles.operator, name: "direct-p2p:v1" });
    await create2.ensureFactory(C);
    await create2.deployCreate2(C, stranger, { salt: p.implSalt, initCode: p._implInitCode, label: "impl" });
    await create2.deployCreate2(C, stranger, { salt: p.proxySalt, initCode: p._proxyInitCode, label: "proxy" });
    const esc = await C.ethers.getContractAt("P2PEscrowV3", p.proxy);
    expect(await esc.owner()).to.equal(deployer.address);
    const out = await deploy(C, { ...opts, chain });
    expect(out.proxy).to.equal(A.out.proxy);
    expect(await esc.owner()).to.equal(roles.owner);
    expect(await esc.systemWallet()).to.equal(roles.treasury);
  });

  it("fails clearly when the factory is missing on a live network", async () => {
    const C = await newChain(31342);
    const live = Object.create(C);
    live.network = { ...C.network, name: "bscTestnet" };
    await expect(create2.ensureFactory(live)).to.be.rejectedWith(/has no code on bscTestnet/);
  });

  it("CHECK_ONLY prints the addresses and sends nothing", async () => {
    const C = await newChain(31343);
    const [deployer] = await C.ethers.getSigners();
    const out = await deploy(C, { ...opts, chain: await chainFor(C), checkOnly: true });
    expect(out.checkOnly).to.equal(true);
    expect(out.proxy).to.equal(A.out.proxy);
    expect(out.exists.factory).to.equal(false);
    expect(await C.ethers.provider.getTransactionCount(deployer.address)).to.equal(2); // the two mock tokens
  });

  it("upgrades through the OpenZeppelin plugin after forceImport", async () => {
    for (const c of [A, B]) {
      const signers = await c.h.ethers.getSigners();
      const pmSigner = signers.find((s) => s.address === roles.owner);
      const V = await c.h.ethers.getContractFactory("P2PEscrowV3UpgradeTest", pmSigner);
      const up = await c.h.upgrades.upgradeProxy(c.out.proxy, V, { kind: "uups" });
      expect(await up.getAddress()).to.equal(c.out.proxy);
      expect(await up.upgradeMarker()).to.equal("v3-upgrade-test");
      expect(await up.systemWallet()).to.equal(roles.treasury); // state survives
      expect(await up.owner()).to.equal(roles.owner);
      expect(await c.h.upgrades.erc1967.getImplementationAddress(c.out.proxy)).to.not.equal(A.out.impl);
    }
  });
});
