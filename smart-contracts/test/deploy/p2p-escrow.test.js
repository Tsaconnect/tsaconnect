// smart-contracts/test/deploy/p2p-escrow.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/p2p-escrow");

describe("deploy/p2p-escrow", () => {
  it("deploys P2PEscrowV3, wires system wallet + tokens, sets MCGP when present", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", proxy);
    expect(await esc.owner()).to.equal(chain.owner);
    expect(await esc.systemWallet()).to.equal(chain.systemWallet);
    expect(await esc.acceptedTokens(chain.tokens.USDC)).to.equal(true);
    expect(await esc.mcgpToken()).to.equal(chain.tokens.MCGP);
  });

  it("skips MCGP wiring when MCGP is null (BSC)", async () => {
    const chain = await testChain({ tokens: { MCGP: null } });
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", proxy);
    expect(await esc.mcgpToken()).to.equal(hre.ethers.ZeroAddress);
  });

  it("falls back systemWallet to owner when unset", async () => {
    const chain = await testChain({ systemWallet: null });
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", proxy);
    expect(await esc.systemWallet()).to.equal(chain.owner);
  });

  it("wires as deployer then transfers ownership to a separate owner", async () => {
    const [, second] = await hre.ethers.getSigners();
    const owner2 = await second.getAddress();
    const chain = await testChain({ owner: owner2 }); // owner != deployer (the mainnet case)
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", proxy);
    expect(await esc.owner()).to.equal(owner2); // ownership handed over after wiring
    expect(await esc.systemWallet()).to.equal(chain.systemWallet); // wiring succeeded (deployer owned it)
    expect(await esc.acceptedTokens(chain.tokens.USDC)).to.equal(true);
  });
});
