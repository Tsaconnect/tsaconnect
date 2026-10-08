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

  it("BSC entries carry a pool block (USDT, 60-minute window); mainnet requires an owner wallet", () => {
    for (const n of ["bscMainnet", "bscTestnet"]) {
      expect(chains[n].pool.tokens).to.deep.equal(["USDT"]);
      expect(chains[n].pool.paymentWindowSec).to.equal(3600);
    }
    expect(chains.bscMainnet.pool.requireOwner).to.equal(true);
    expect(chains.bscTestnet.pool.requireOwner).to.equal(false);
    expect(chains.sonicMainnet.pool).to.equal(undefined);
  });
});
