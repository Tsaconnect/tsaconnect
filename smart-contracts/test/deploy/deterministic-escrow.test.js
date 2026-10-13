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
const { HDNodeWallet, Wallet } = require("ethers");
const det = require("../../deploy/deterministic-escrow");
const { deploy, plan } = det;
const { wireEscrow } = require("../../deploy/p2p-escrow");
const { upgrade } = require("../../deploy/upgrade");
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

// The per-run deploy key: hardhat account #0, derived from the test mnemonic.
function hardhatKey(i = 0) {
  const acc = hre.config.networks.hardhat.accounts;
  return HDNodeWallet.fromPhrase(acc.mnemonic, acc.passphrase, `${acc.path}/${i}`).privateKey;
}

const tmpManifest = () => path.join(fs.mkdtempSync(path.join(os.tmpdir(), "det-")), "deterministic.json");

describe("deploy/deterministic-escrow", function () {
  this.timeout(120000);
  let manifestPath, roles, opts, A, B, deployerKey;

  before(async () => {
    manifestPath = tmpManifest();
    deployerKey = hardhatKey(0);
    const [, operator, treasury, , , pm] = await hre.ethers.getSigners();
    roles = { owner: pm.address, operator: operator.address, treasury: treasury.address };
    opts = { roles, ownerConfirm: pm.address, manifestPath, paymentWindowSec: 3600, feeBps: 0, deployerKey };
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
    const m = JSON.parse(fs.readFileSync(manifestPath, "utf8")).local["tsa:P2PEscrowV3:proxy:direct-p2p:v1"];
    expect(m.proxy).to.equal(A.out.proxy);
    expect(m.impl).to.equal(A.out.impl);
    expect(m.initArgs.admins).to.deep.equal([roles.operator]);
    expect(Object.keys(m.networks)).to.include.members([String(A.chainId), String(B.chainId)]);
  });

  it("uses the OpenZeppelin v5 ERC1967Proxy that hardhat-upgrades deploys", async () => {
    const v5 = require("@openzeppelin/upgrades-core/artifacts/@openzeppelin/contracts-v5/proxy/ERC1967/ERC1967Proxy.sol/ERC1967Proxy.json");
    expect(await hre.ethers.provider.getCode(A.out.proxy)).to.equal(v5.deployedBytecode);
    const [deployer] = await hre.ethers.getSigners();
    const regular = await hre.upgrades.deployProxy(await hre.ethers.getContractFactory("P2PEscrowV3"), [deployer.address, [roles.operator]], { kind: "uups" });
    expect(await hre.ethers.provider.getCode(await regular.getAddress())).to.equal(v5.deployedBytecode);
  });

  it("mirrors the contract's fee and window bounds", async () => {
    expect(await A.esc.MAX_FEE_BPS()).to.equal(BigInt(det.ESCROW_LIMITS.MAX_FEE_BPS));
    expect(await A.esc.MIN_PAYMENT_WINDOW()).to.equal(BigInt(det.ESCROW_LIMITS.MIN_PAYMENT_WINDOW));
    expect(await A.esc.MAX_PAYMENT_WINDOW()).to.equal(BigInt(det.ESCROW_LIMITS.MAX_PAYMENT_WINDOW));
  });

  it("re-running on a handed-over chain WITHOUT a key verifies and sends nothing", async () => {
    const [deployer] = await hre.ethers.getSigners();
    const nonce = await hre.ethers.provider.getTransactionCount(deployer.address);
    for (const extra of [{}, { checkOnly: true }, { deployer: deployer.address }]) {
      const out = await deploy(hre, { ...opts, chain: A.chain, ownerConfirm: null, deployerKey: undefined, ...extra });
      expect(out.reused).to.equal(true);
      expect(out.verified).to.equal(true);
      expect(out.proxy).to.equal(A.out.proxy);
    }
    expect(await hre.ethers.provider.getTransactionCount(deployer.address)).to.equal(nonce);
  });

  it("the keyless verify fails loudly when a per-chain value is wrong", async () => {
    // B accepts USDT only; asking it to verify USDC too must fail, not pass silently.
    await expect(deploy(B.h, { ...opts, chain: B.chain, deployerKey: undefined }))
      .to.be.rejectedWith(/USDC .* is not accepted/);
  });

  it("refuses to deploy without the per-run key, before sending anything", async () => {
    const C = await newChain(31350);
    const chain = await chainFor(C);
    const [deployer] = await C.ethers.getSigners();
    const nonce = await C.ethers.provider.getTransactionCount(deployer.address);
    await expect(deploy(C, { ...opts, chain, deployerKey: undefined, deployer: deployer.address, manifestPath: tmpManifest() }))
      .to.be.rejectedWith(/needs DET_DEPLOYER_PRIVATE_KEY/);
    expect(await C.ethers.provider.getTransactionCount(deployer.address)).to.equal(nonce);
    expect(await create2.hasCode(C.ethers.provider, create2.FACTORY)).to.equal(false);
  });

  it("refuses a routine deploy key (.env / hardhat-config account) as the deployer, before sending anything", async () => {
    const C = await newChain(31351);
    const chain = await chainFor(C);
    const [deployer] = await C.ethers.getSigners();
    const nonce = await C.ethers.provider.getTransactionCount(deployer.address);
    await expect(deploy(C, { ...opts, chain, manifestPath: tmpManifest(), routineAddresses: [deployer.address] }))
      .to.be.rejectedWith(/is a routine deploy key/);
    expect(await C.ethers.provider.getTransactionCount(deployer.address)).to.equal(nonce);
  });

  it("routineDeployerAddresses covers hardhat-config accounts and .env, and refuses a stored deploy key", () => {
    const key = hardhatKey(3);
    const addr = new Wallet(key).address;
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "det-env-"));
    const env = path.join(dir, ".env");
    const fake = { network: { config: { accounts: [key] } } };
    expect(det.routineDeployerAddresses(fake, env)).to.include(addr);
    fs.writeFileSync(env, `DEPLOYER_PRIVATE_KEY=${key.slice(2)}\n`);
    expect(det.routineDeployerAddresses({ network: { config: {} } }, env)).to.include(addr);
    fs.writeFileSync(env, `DET_DEPLOYER_PRIVATE_KEY=${key}\n`);
    expect(() => det.routineDeployerAddresses({ network: { config: {} } }, env)).to.throw(/stored in \.env/);
  });

  it("refuses bad DET_FEE_BPS / DET_PAYMENT_WINDOW / DET_FEE_MANAGER / tokens before any send or manifest write", async () => {
    const C = await newChain(31352);
    const chain = await chainFor(C);
    const [deployer, , , , , , stranger] = await C.ethers.getSigners();
    const file = tmpManifest();
    const nonce = await C.ethers.provider.getTransactionCount(deployer.address);
    const cases = [
      [{ feeBps: 501 }, /DET_FEE_BPS/],
      [{ feeBps: "abc" }, /DET_FEE_BPS/],
      [{ feeBps: -1 }, /DET_FEE_BPS/],
      [{ paymentWindowSec: det.ESCROW_LIMITS.MIN_PAYMENT_WINDOW - 1 }, /DET_PAYMENT_WINDOW/],
      [{ paymentWindowSec: det.ESCROW_LIMITS.MAX_PAYMENT_WINDOW + 1 }, /DET_PAYMENT_WINDOW/],
      [{ paymentWindowSec: "30m" }, /DET_PAYMENT_WINDOW/],
      [{ feeManager: "0x1234" }, /DET_FEE_MANAGER is not a valid address/],
      [{ feeManager: stranger.address }, /DET_FEE_MANAGER .* has no code/],
      [{ tokens: ["DAI"] }, /token DAI has no address/],
      [{ chain: { ...chain, tokens: { ...chain.tokens, USDC: stranger.address } } }, /token USDC .* has no code/],
    ];
    for (const [bad, re] of cases) {
      await expect(deploy(C, { ...opts, chain, manifestPath: file, ...bad }), JSON.stringify(bad)).to.be.rejectedWith(re);
    }
    expect(await C.ethers.provider.getTransactionCount(deployer.address)).to.equal(nonce);
    expect(await create2.hasCode(C.ethers.provider, create2.FACTORY)).to.equal(false);
    expect(fs.existsSync(file)).to.equal(false);
  });

  it("keys the manifest by network class: a testnet rehearsal never pins mainnet", async () => {
    const file = tmpManifest();
    const [, , , , , , , rehearsal] = await hre.ethers.getSigners();
    const p = await plan(hre, { deployer: rehearsal.address, operator: roles.operator, name: "direct-p2p:v1" });
    const entry = { ...p, initArgs: p.initArgs, networks: { 97: { network: "bscTestnet" } } };
    for (const k of Object.keys(entry)) if (k.startsWith("_")) delete entry[k];
    create2.writeManifest({ testnet: { [p.proxySaltLabel]: entry } }, file);
    const C = await newChain(31353);
    const chain = await chainFor(C);
    const base = { ...opts, chain, manifestPath: file, checkOnly: true, deployerKey: undefined, deployer: (await C.ethers.getSigners())[0].address };
    await expect(deploy(C, { ...base, networkClass: "testnet" })).to.be.rejectedWith(/would not land at the recorded/);
    const out = await deploy(C, { ...base, networkClass: "mainnet" });
    expect(out.proxy).to.equal(A.out.proxy);
    expect(out.proxy).to.not.equal(p.proxy);
    expect(det.networkClass({ network: { name: "bscMainnet" } })).to.equal("mainnet");
    expect(det.networkClass({ network: { name: "sonicTestnet" } })).to.equal("testnet");
    expect(() => det.networkClass({ network: { name: "polygon" } })).to.throw(/mainnet or a testnet/);
  });

  it("assertManifest refuses an entry with missing initArgs / owner / admins", async () => {
    const [deployer] = await hre.ethers.getSigners();
    const p = await plan(hre, { deployer: deployer.address, operator: roles.operator, name: "direct-p2p:v1" });
    const full = { ...p };
    for (const broken of [{ initArgs: undefined }, { initArgs: { admins: [roles.operator] } }, { initArgs: { owner: deployer.address } }]) {
      expect(() => det.assertManifest({ ...full, ...broken }, p)).to.throw(/refusing.*initArgs.owner \/ initArgs.admins/s);
    }
    expect(() => det.assertManifest(full, p)).to.not.throw();
  });

  it("wireEscrow skips setters already at their target, so a resumed run resends nothing", async () => {
    const [deployer] = await hre.ethers.getSigners();
    const esc = await hre.upgrades.deployProxy(await hre.ethers.getContractFactory("P2PEscrowV3"), [deployer.address, [roles.operator]], { kind: "uups" });
    const wiring = { cfg: A.chain, tokens: { USDT: A.chain.tokens.USDT, USDC: A.chain.tokens.USDC }, systemWallet: roles.treasury };
    const n0 = await hre.ethers.provider.getTransactionCount(deployer.address);
    await wireEscrow(esc, wiring);
    const n1 = await hre.ethers.provider.getTransactionCount(deployer.address);
    expect(n1 - n0).to.equal(6); // systemWallet + 2 × (accept, symbol) + permit2
    await wireEscrow(esc, wiring);
    expect(await hre.ethers.provider.getTransactionCount(deployer.address)).to.equal(n1);
  });

  it("deploy/upgrade.js UPGRADE_DET_NAME upgrades the deterministic proxy, not the main escrow", async () => {
    const signers = await hre.ethers.getSigners();
    const pmSigner = signers.find((s) => s.address === roles.owner);
    const main = await hre.upgrades.deployProxy(await hre.ethers.getContractFactory("P2PEscrowV3"), [roles.owner, [roles.operator]], { kind: "uups" });
    const mainAddr = await main.getAddress();
    const mainImpl = await hre.upgrades.erc1967.getImplementationAddress(mainAddr);
    const { writeLedger, readLedger } = require("../../deploy/lib");
    const ledger = path.join(__dirname, "../../deployments", "hardhat.json");
    const before = fs.existsSync(ledger) ? fs.readFileSync(ledger) : null;
    try {
      // The ledger's P2PEscrowV3 key is the MAIN escrow on this network.
      writeLedger("hardhat", "P2PEscrowV3", { proxy: mainAddr, impl: mainImpl });
      await expect(upgrade(hre, { contract: "FeeManager", detName: "direct-p2p:v1", manifestPath })).to.be.rejectedWith(/only applies to P2PEscrowV3/);
      await expect(upgrade(hre, { contract: "P2PEscrowV3", detName: "nope", manifestPath })).to.be.rejectedWith(/no recorded/);
      const V = await hre.ethers.getContractFactory("P2PEscrowV3UpgradeTest", pmSigner);
      const out = await upgrade(hre, { contract: "P2PEscrowV3", detName: "direct-p2p:v1", manifestPath, factory: V });
      expect(out.proxy).to.equal(A.out.proxy);
      expect(out.impl).to.not.equal(A.out.impl);
      expect(await hre.upgrades.erc1967.getImplementationAddress(A.out.proxy)).to.equal(out.impl);
      expect(await hre.upgrades.erc1967.getImplementationAddress(mainAddr)).to.equal(mainImpl);
      expect(readLedger("hardhat").P2PEscrowV3.proxy).to.equal(mainAddr);
    } finally {
      if (before) fs.writeFileSync(ledger, before);
      else if (fs.existsSync(ledger)) fs.unlinkSync(ledger);
    }
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
    const out = await deploy(C, { ...opts, chain: await chainFor(C), checkOnly: true, deployerKey: undefined, deployer: deployer.address });
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
