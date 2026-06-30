// smart-contracts/test/deploy/service-contact.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/service-contact");

describe("deploy/service-contact", () => {
  it("deploys ServiceContactV2 and accepts configured tokens", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const sc = await hre.ethers.getContractAt("ServiceContactV2", proxy);
    expect(await sc.owner()).to.equal(chain.owner);
    expect(await sc.acceptedTokens(chain.tokens.USDC)).to.equal(true);
  });
});
