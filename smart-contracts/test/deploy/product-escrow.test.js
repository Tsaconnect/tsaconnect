// smart-contracts/test/deploy/product-escrow.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/product-escrow");

describe("deploy/product-escrow", () => {
  it("deploys ProductEscrowV2 and accepts configured tokens", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("ProductEscrowV2", proxy);
    expect(await esc.owner()).to.equal(chain.owner);
    expect(await esc.acceptedTokens(chain.tokens.USDT)).to.equal(true);
  });
});
