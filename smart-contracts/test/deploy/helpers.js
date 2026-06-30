// smart-contracts/test/deploy/helpers.js
const { ethers } = require("hardhat");
const { CANONICAL_PERMIT2 } = require("../../deploy/chains");

async function deployMockTokens(decimals = 18) {
  const Mock = await ethers.getContractFactory("MockERC20");
  const mk = async (name, sym) => {
    const t = await Mock.deploy(name, sym, decimals); // ctor: (string name, string symbol, uint8 decimals_)
    await t.waitForDeployment();
    return await t.getAddress();
  };
  return {
    USDT: await mk("Mock USDT", "USDT"),
    USDC: await mk("Mock USDC", "USDC"),
    MCGP: await mk("Mock MCGP", "MCGP"),
  };
}

async function testChain(overrides = {}) {
  const [signer] = await ethers.getSigners();
  const me = await signer.getAddress();
  const tokens = await deployMockTokens();
  const base = {
    tokens,
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: me,
    admins: [me],
    systemWallet: me,
  };
  return { ...base, ...overrides, tokens: { ...tokens, ...(overrides.tokens || {}) } };
}

module.exports = { deployMockTokens, testChain };
