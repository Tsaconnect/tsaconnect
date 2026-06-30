// smart-contracts/test/deploy/chains.test.js
const { expect } = require("chai");
const { chains, CANONICAL_PERMIT2 } = require("../../deploy/chains");

describe("deploy/chains config", () => {
  const REQUIRED = ["sonicMainnet", "sonicTestnet", "bscMainnet", "bscTestnet"];

  it("defines all four networks", () => {
    for (const n of REQUIRED) expect(chains[n], n).to.be.an("object");
  });

  it("has USDT/USDC for every network", () => {
    for (const n of REQUIRED) {
      expect(chains[n].tokens.USDT, `${n} USDT`).to.match(/^0x[0-9a-fA-F]{40}$/);
      expect(chains[n].tokens.USDC, `${n} USDC`).to.match(/^0x[0-9a-fA-F]{40}$/);
    }
  });

  it("has MCGP on Sonic but null on BSC", () => {
    expect(chains.sonicMainnet.tokens.MCGP).to.match(/^0x[0-9a-fA-F]{40}$/);
    expect(chains.sonicTestnet.tokens.MCGP).to.match(/^0x[0-9a-fA-F]{40}$/);
    expect(chains.bscMainnet.tokens.MCGP).to.equal(null);
    expect(chains.bscTestnet.tokens.MCGP).to.equal(null);
  });

  it("uses canonical Permit2 on BSC", () => {
    expect(chains.bscMainnet.permit2).to.equal(CANONICAL_PERMIT2);
  });
});
