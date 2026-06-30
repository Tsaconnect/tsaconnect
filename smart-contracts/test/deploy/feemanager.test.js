// smart-contracts/test/deploy/feemanager.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/feemanager");

describe("deploy/feemanager", () => {
  it("deploys a FeeManager proxy owned by the configured owner", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const fm = await hre.ethers.getContractAt("FeeManager", proxy);
    expect(await fm.owner()).to.equal(chain.owner);
  });
});
