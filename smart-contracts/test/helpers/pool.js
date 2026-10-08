// smart-contracts/test/helpers/pool.js — a pool deployed by deploy/pool.js on
// the hardhat network, and the LP voucher helpers the pool tests share.
const hre = require("hardhat");
const { ethers } = hre;
const { deploy: deployPool } = require("../../deploy/pool");

const POOL_WINDOW = 3600;
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

// Signers: 0 deployer, 1 release admin, 2 conversion treasury (buyer + system
// wallet), 3 LP A, 4 LP B, 5 the owner's wallet (decision 18), 6 stranger.
async function poolFixture({ tokenDecimals = 18, poolOwner, requireOwner = false, ownerConfirm } = {}) {
  const [deployer, admin, treasury, lpA, lpB, ownerWallet, stranger] = await ethers.getSigners();
  const usdt = await mockToken("USDT", tokenDecimals);
  const usdc = await mockToken("USDC", 18);
  const chain = {
    tokens: { USDT: await usdt.getAddress(), USDC: await usdc.getAddress(), MCGP: null },
    permit2: null, forwarder: null, owner: deployer.address, admins: [deployer.address], systemWallet: deployer.address,
    pool: {
      tokens: ["USDT"], paymentWindowSec: POOL_WINDOW, releaseAdmin: admin.address,
      systemWallet: treasury.address, owner: poolOwner === undefined ? ownerWallet.address : poolOwner, requireOwner,
      ownerConfirm: ownerConfirm === undefined ? (poolOwner === undefined ? ownerWallet.address : poolOwner) : ownerConfirm,
    },
  };
  const out = await deployPool(hre, { chain });
  const esc = await ethers.getContractAt("P2PEscrowV3", out.escrow);
  const fm = await ethers.getContractAt("FeeManager", out.feeManager);
  for (const lp of [lpA, lpB]) await usdt.mint(lp.address, ethers.parseUnits("10000", tokenDecimals));
  return { deployer, admin, treasury, lpA, lpB, ownerWallet, stranger, usdt, usdc, esc, fm, chain, out, tokenDecimals };
}

async function depositAndSign(ctx, lp, amount) {
  const { esc, usdt } = ctx;
  await (await usdt.connect(lp).approve(await esc.getAddress(), amount)).wait();
  await (await esc.connect(lp).deposit(await usdt.getAddress(), amount)).wait();
  const v = {
    seller: lp.address, token: await usdt.getAddress(), maxAmount: amount * 100n,
    minPerTrade: 0, maxPerTrade: 0, deadline: 4102444800, nonce: ethers.hexlify(ethers.randomBytes(32)),
  };
  const domain = { name: "TSAConnectP2PEscrow", version: "2",
    chainId: (await ethers.provider.getNetwork()).chainId, verifyingContract: await esc.getAddress() };
  return { v, sig: await lp.signTypedData(domain, AD_VOUCHER_TYPES, v) };
}

async function openSlice(ctx, voucher, id, amount) {
  return ctx.esc.connect(ctx.treasury).openTrade(id, voucher.v, voucher.sig, amount);
}

module.exports = { POOL_WINDOW, poolFixture, depositAndSign, openSlice, mockToken };
