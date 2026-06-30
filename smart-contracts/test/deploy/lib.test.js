// smart-contracts/test/deploy/lib.test.js
const { expect } = require("chai");
const fs = require("fs");
const path = require("path");
const { resolveChain, requireAddr, readLedger, writeLedger } = require("../../deploy/lib");

describe("deploy/lib", () => {
  it("resolveChain returns the config for a known network", () => {
    const cfg = resolveChain({ network: { name: "sonicTestnet" } });
    expect(cfg.tokens.USDC).to.match(/^0x/);
  });

  it("resolveChain throws on an unknown network", () => {
    expect(() => resolveChain({ network: { name: "nope" } })).to.throw(/no chain config/);
  });

  it("requireAddr returns a present value and throws on null", () => {
    expect(requireAddr({}, "MCGP", "0xabc")).to.equal("0xabc");
    expect(() => requireAddr({}, "MCGP", null)).to.throw(/MCGP not configured/);
  });

  it("writeLedger then readLedger round-trips", () => {
    const net = "hardhat-libtest";
    const file = path.join(__dirname, "../../deployments", `${net}.json`);
    if (fs.existsSync(file)) fs.unlinkSync(file);
    writeLedger(net, "FeeManager", { proxy: "0x1", impl: "0x2" });
    expect(readLedger(net).FeeManager.proxy).to.equal("0x1");
    fs.unlinkSync(file);
  });
});
