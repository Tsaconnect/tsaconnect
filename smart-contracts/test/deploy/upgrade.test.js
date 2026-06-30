// smart-contracts/test/deploy/upgrade.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/feemanager");
const { upgrade } = require("../../deploy/upgrade");

describe("deploy/upgrade", () => {
  it("upgrades an existing FeeManager proxy in place", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const { proxy: same, impl } = await upgrade(hre, { contract: "FeeManager", proxy });
    expect(same).to.equal(proxy);
    expect(impl).to.match(/^0x[0-9a-fA-F]{40}$/);
  });
});
