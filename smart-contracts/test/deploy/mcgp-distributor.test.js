// smart-contracts/test/deploy/mcgp-distributor.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/mcgp-distributor");

describe("deploy/mcgp-distributor", () => {
  it("deploys against a configured MCGP token", async () => {
    const chain = await testChain();
    const { address } = await deploy(hre, { chain });
    const d = await hre.ethers.getContractAt("MCGPRewardDistributor", address);
    expect(await d.owner()).to.equal(chain.owner);
  });

  it("fails fast when MCGP is not configured (BSC)", async () => {
    const chain = await testChain({ tokens: { MCGP: null } });
    let err;
    try { await deploy(hre, { chain }); } catch (e) { err = e; }
    expect(err && err.message).to.match(/MCGP not configured/);
  });
});
