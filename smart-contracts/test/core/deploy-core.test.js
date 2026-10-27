const { expect } = require("chai");
const { ethers } = require("hardhat");
const { buildInitialConfig, checkRoles, ledgerNames } = require("../../deploy/core");
const { chains, CANONICAL_HOT_KEYS } = require("../../deploy/chains");

// deploy/core.js pure parts: the initial FeeConfig rows/limits and the role checks.
describe("deploy/core.js", () => {
  const order = ethers.encodeBytes32String("order");

  it("builds Sonic's initial config in 6-decimal units and BSC's in 18", () => {
    const sonic = buildInitialConfig(chains.sonicMainnet);
    const usdc = ethers.getAddress(chains.sonicMainnet.tokens.USDC);
    const i = sonic.tokens.findIndex((t, j) => t === usdc && sonic.features[j] === order);
    expect(sonic.rates[i]).to.deep.include({ bps: 200, flatFee: 100000n, active: true });
    const li = sonic.limitTokens.indexOf(usdc);
    expect(sonic.limits[li].maxPerOrder).to.equal(1000n * 10n ** 6n);

    const bsc = buildInitialConfig(chains.bscMainnet);
    expect(bsc.features.includes(order)).to.equal(false); // no OrderEscrow on BSC
    const usdt = ethers.getAddress(chains.bscMainnet.tokens.USDT);
    expect(bsc.limits[bsc.limitTokens.indexOf(usdt)].maxOwed).to.equal(20000n * 10n ** 18n);
  });

  it("every token with a rate gets limits", () => {
    for (const net of ["sonicMainnet", "bscMainnet"]) {
      const c = buildInitialConfig(chains[net]);
      for (const t of new Set(c.tokens)) expect(c.limitTokens.includes(t), `${net} ${t}`).to.equal(true);
    }
  });

  it("refuses the deployer, hot keys and forbidden keys in any role or as fee recipient", () => {
    const base = { ...chains.sonicMainnet.core, adminConfirm: null, resolvers: [], pausers: [], forbidden: [] };
    const deployer = ethers.Wallet.createRandom().address;
    const hot = CANONICAL_HOT_KEYS.operator;
    const relayer = ethers.Wallet.createRandom().address;
    expect(() => checkRoles({ ...base, admin: deployer }, deployer)).to.throw(/deployer/);
    expect(() => checkRoles({ ...base, admin: hot }, deployer)).to.throw(/hot key/);
    expect(() => checkRoles({ ...base, resolvers: [hot] }, deployer)).to.throw(/hot key/);
    expect(() => checkRoles({ ...base, pausers: [relayer], forbidden: [relayer] }, deployer)).to.throw(/hot key/);
    expect(() => checkRoles({ ...base, feeRecipient: deployer }, deployer)).to.throw(/fee recipient/);
    expect(() => checkRoles({ ...base, feeRecipient: CANONICAL_HOT_KEYS.treasury }, deployer)).to.throw(/fee recipient/);
    expect(checkRoles(base, deployer).admin).to.equal(ethers.getAddress(base.admin));
  });

  it("a dev set makes the deployer the admin, resolver and pauser, but still refuses hot keys", () => {
    const base = { ...chains.sonicMainnet.core, adminConfirm: null, resolvers: [], pausers: [], forbidden: [] };
    const deployer = ethers.getAddress(ethers.Wallet.createRandom().address);
    const roles = checkRoles(base, deployer, { dev: true });
    expect(roles.admin).to.equal(deployer);
    expect(roles.resolvers).to.deep.equal([deployer]);
    expect(roles.pausers).to.deep.equal([deployer]);
    const resolver = ethers.Wallet.createRandom().address;
    expect(checkRoles({ ...base, resolvers: [resolver] }, deployer, { dev: true }).resolvers).to.deep.equal([ethers.getAddress(resolver)]);
    expect(() => checkRoles(base, CANONICAL_HOT_KEYS.operator, { dev: true })).to.throw(/hot key/);
    expect(() => checkRoles({ ...base, feeRecipient: deployer }, deployer, { dev: true })).to.throw(/fee recipient/);
  });

  it("a dev set never shares ledger names with production", () => {
    const prod = Object.values(ledgerNames(false));
    for (const name of Object.values(ledgerNames(true))) expect(prod).to.not.include(name);
  });
});
