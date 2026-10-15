// smart-contracts/test/helpers/direct.js — a direct P2P escrow deployed by
// deploy/p2p-direct.js (on top of deploy/deterministic-escrow.js) on the
// hardhat network.
const hre = require("hardhat");
const { ethers } = hre;
const { HDNodeWallet } = require("ethers");
const { deploy: deployDirect } = require("../../deploy/p2p-direct");

const DIRECT_WINDOW = 1800;
const AD_VOUCHER_TYPES = { AdVoucher: [
  { name: "seller", type: "address" }, { name: "token", type: "address" },
  { name: "maxAmount", type: "uint256" }, { name: "minPerTrade", type: "uint256" },
  { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
  { name: "nonce", type: "bytes32" },
]};

async function mockToken(sym, decimals) {
  const t = await (await ethers.getContractFactory("MockERC20")).deploy(`Mock ${sym}`, sym, decimals);
  await t.waitForDeployment();
  return t;
}

// depositAndSign funds seller's escrow balance and signs an ad voucher on it.
async function depositAndSign({ esc, usdt }, seller, amount) {
  await (await usdt.connect(seller).approve(await esc.getAddress(), amount)).wait();
  await (await esc.connect(seller).deposit(await usdt.getAddress(), amount)).wait();
  const v = {
    seller: seller.address, token: await usdt.getAddress(), maxAmount: amount * 100n,
    minPerTrade: 0, maxPerTrade: 0, deadline: 4102444800, nonce: ethers.hexlify(ethers.randomBytes(32)),
  };
  const domain = { name: "TSAConnectP2PEscrow", version: "2",
    chainId: (await ethers.provider.getNetwork()).chainId, verifyingContract: await esc.getAddress() };
  return { v, sig: await seller.signTypedData(domain, AD_VOUCHER_TYPES, v) };
}
const RATES = { p2p_platform: { bps: 100 }, p2p: { bps: 50 } };

// Signers: 0 deployer (the per-run deploy key AND the routine FeeManager
// deployer here), 1 arbiter A, 2 arbiter B, 3 treasury (systemWallet), 4
// operator (PM release EOA), 5 owner (PM), 6 canceller, 7 BSC gas sponsor,
// 8 stranger.
async function directSigners() {
  const [deployer, arbA, arbB, sysWallet, releaseEoa, ownerWallet, canceller, sponsor, stranger] = await ethers.getSigners();
  return { deployer, arbA, arbB, sysWallet, releaseEoa, ownerWallet, canceller, sponsor, stranger };
}

// The per-run deploy key: hardhat account #i (default #0, which is also the
// routine hardhat signer here), derived from the test mnemonic.
function deployerKey(i = 0) {
  const acc = hre.config.networks.hardhat.accounts;
  return HDNodeWallet.fromPhrase(acc.mnemonic, acc.passphrase, `${acc.path}/${i}`).privateKey;
}

// Every fixture gets its own proxy salt (DET_NAME): the deterministic address
// is fixed per (deployer, admins, name), and all tests share one chain.
let seq = 0;
const nextName = () => `direct-p2p:test-${++seq}`;

// directChain builds a chains.js-shaped config with fresh 18-dp mock tokens
// (plus a mock MCGP that must NOT be accepted) and the deploy options.
// `direct` overrides any field of the direct block, `opts` any deploy option.
async function directChain(direct = {}, top = {}, opts = {}) {
  const s = await directSigners();
  const usdt = await mockToken("USDT", 18);
  const usdc = await mockToken("USDC", 18);
  const mcgp = await mockToken("MCGP", 18);
  const chain = {
    tokens: { USDT: await usdt.getAddress(), USDC: await usdc.getAddress(), MCGP: await mcgp.getAddress() },
    permit2: "0x000000000022D473030F116dDEE9F6B43aC78BA3", forwarder: null,
    owner: null, admins: [], systemWallet: null,
    direct: {
      tokens: ["USDT", "USDC"], paymentWindowSec: DIRECT_WINDOW,
      arbiters: [s.arbA.address, s.arbB.address],
      forbiddenAdmins: [s.canceller.address, s.sponsor.address],
      rates: JSON.stringify(RATES),
      ...direct,
    },
    ...top,
  };
  const roles = { owner: s.ownerWallet.address, operator: s.releaseEoa.address, treasury: s.sysWallet.address };
  const deployOpts = { chain, roles, ownerConfirm: s.ownerWallet.address, deployerKey: deployerKey(), name: nextName(), ...opts };
  return { ...s, usdt, usdc, mcgp, chain, roles, deployOpts };
}

async function directFixture(direct = {}, opts = {}) {
  const ctx = await directChain(direct, opts.top, opts.deploy);
  const out = await deployDirect(hre, { ...ctx.deployOpts, afterWire: opts.afterWire, beforeHandover: opts.beforeHandover });
  const esc = await ethers.getContractAt("P2PEscrowV3", out.escrow);
  const fm = await ethers.getContractAt("FeeManager", out.feeManager);
  return { ...ctx, out, esc, fm };
}

module.exports = { DIRECT_WINDOW, RATES, directSigners, directChain, directFixture, deployerKey, depositAndSign };
