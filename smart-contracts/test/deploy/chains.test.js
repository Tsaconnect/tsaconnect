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

  it("BSC entries carry a liquidityPool block defaulting to the owner's wallet", () => {
    for (const n of ["bscMainnet", "bscTestnet"]) {
      expect(chains[n].liquidityPool.token).to.equal("USDT");
      expect(chains[n].liquidityPool.minDeposit).to.equal(1); // PM 2026-10-15: $1
    }
    expect(chains.sonicMainnet.liquidityPool).to.equal(undefined);
  });

  it("BSC entries carry a direct block (USDT+USDC, 30-minute window)", () => {
    for (const n of ["bscMainnet", "bscTestnet"]) {
      expect(chains[n].direct.tokens).to.deep.equal(["USDT", "USDC"]);
      expect(chains[n].direct.paymentWindowSec).to.equal(1800);
      expect(chains[n].direct.arbiters).to.be.an("array");
      expect(chains[n].direct.forbiddenAdmins).to.be.an("array");
    }
    expect(chains.sonicMainnet.direct).to.equal(undefined);
  });
});
