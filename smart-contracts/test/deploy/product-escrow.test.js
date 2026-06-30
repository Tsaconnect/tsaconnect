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

  it("wires as deployer then transfers ownership to a separate owner", async () => {
    const [, second] = await hre.ethers.getSigners();
    const owner2 = await second.getAddress();
    const chain = await testChain({ owner: owner2 });
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("ProductEscrowV2", proxy);
    expect(await esc.owner()).to.equal(owner2);
    expect(await esc.acceptedTokens(chain.tokens.USDT)).to.equal(true);
  });
});
