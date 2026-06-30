// smart-contracts/test/deploy/all.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deployAll } = require("../../deploy/all");

describe("deploy/all", () => {
  it("deploys the full stack and wires escrows to the FeeManager", async () => {
    const chain = await testChain();
    const out = await deployAll(hre, { chain });
    expect(out.FeeManager.proxy).to.match(/^0x/);
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", out.P2PEscrowV3.proxy);
    expect(await esc.feeManager()).to.equal(out.FeeManager.proxy);
    expect(out.MCGPRewardDistributor.address).to.match(/^0x/);
  });

  it("omits the distributor when MCGP is null (BSC)", async () => {
    const chain = await testChain({ tokens: { MCGP: null } });
    const out = await deployAll(hre, { chain });
    expect(out.MCGPRewardDistributor).to.equal(undefined);
  });
});
