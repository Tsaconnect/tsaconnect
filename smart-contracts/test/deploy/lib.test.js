// smart-contracts/test/deploy/lib.test.js
const { expect } = require("chai");
const fs = require("fs");
const path = require("path");
const {
  resolveChain, validateAddresses, requireAddr, readLedger, writeLedger, reuseFromLedger, handOverOwnership,
} = require("../../deploy/lib");

const VALID = "0x000000000022D473030F116dDEE9F6B43aC78BA3"; // canonical Permit2
const ledgerFile = (net) => path.join(__dirname, "../../deployments", `${net}.json`);

describe("deploy/lib", () => {
  it("resolveChain returns the config for a known network", () => {
    const cfg = resolveChain({ network: { name: "sonicTestnet" } });
    expect(cfg.tokens.USDC).to.match(/^0x/);
  });

  it("resolveChain throws on an unknown network", () => {
    expect(() => resolveChain({ network: { name: "nope" } })).to.throw(/no chain config/);
  });

  it("resolveChain validates the configured addresses (every shipped network)", () => {
    for (const net of ["sonicMainnet", "sonicTestnet", "bscMainnet", "bscTestnet"]) {
      expect(() => resolveChain({ network: { name: net } }), net).to.not.throw();
    }
  });

  describe("validateAddresses", () => {
    const base = { tokens: { USDT: VALID, USDC: VALID, MCGP: null }, permit2: VALID, forwarder: null, owner: null, admins: [], systemWallet: null };

    it("passes a config of valid/null addresses", () => {
      expect(() => validateAddresses(base, "x")).to.not.throw();
    });

    it("throws with the field label on a malformed address", () => {
      const bad = { ...base, tokens: { ...base.tokens, USDT: "0xnotanaddress" } };
      expect(() => validateAddresses(bad, "bscTestnet")).to.throw(/tokens\.USDT on bscTestnet/);
    });

    it("validates each admin entry", () => {
      const bad = { ...base, admins: [VALID, "0xbad"] };
      expect(() => validateAddresses(bad, "x")).to.throw(/admins\[1\]/);
    });
  });

  describe("requireAddr", () => {
    it("returns a valid address", () => {
      expect(requireAddr({}, "MCGP", VALID)).to.equal(VALID);
    });
    it("throws 'not configured' on null", () => {
      expect(() => requireAddr({}, "MCGP", null)).to.throw(/MCGP not configured/);
    });
    it("throws 'not a valid address' on a malformed value", () => {
      expect(() => requireAddr({}, "MCGP", "0xabc")).to.throw(/MCGP is not a valid address/);
    });
  });

  describe("ledger", () => {
    it("writeLedger then readLedger round-trips", () => {
      const net = "hardhat-libtest";
      if (fs.existsSync(ledgerFile(net))) fs.unlinkSync(ledgerFile(net));
      writeLedger(net, "FeeManager", { proxy: "0x1", impl: "0x2" });
      expect(readLedger(net).FeeManager.proxy).to.equal("0x1");
      fs.unlinkSync(ledgerFile(net));
    });

    it("readLedger throws a clear error on a corrupt file", () => {
      const net = "hardhat-corrupt";
      fs.mkdirSync(path.dirname(ledgerFile(net)), { recursive: true });
      fs.writeFileSync(ledgerFile(net), "{ not json");
      try {
        expect(() => readLedger(net)).to.throw(/ledger .* is corrupt/);
      } finally {
        fs.unlinkSync(ledgerFile(net));
      }
    });
  });

  describe("reuseFromLedger", () => {
    const net = "hardhat-reuse";
    const hre = { network: { name: net } };
    afterEach(() => { if (fs.existsSync(ledgerFile(net))) fs.unlinkSync(ledgerFile(net)); delete process.env.FORCE_REDEPLOY; });

    it("returns the ledger entry on the CLI path when present", () => {
      writeLedger(net, "FeeManager", { proxy: "0xFEE" });
      expect(reuseFromLedger(hre, "FeeManager", undefined).proxy).to.equal("0xFEE");
    });

    it("returns null when no entry exists", () => {
      expect(reuseFromLedger(hre, "FeeManager", undefined)).to.equal(null);
    });

    it("returns null on the test path (chain provided)", () => {
      writeLedger(net, "FeeManager", { proxy: "0xFEE" });
      expect(reuseFromLedger(hre, "FeeManager", { tokens: {} })).to.equal(null);
    });

    it("returns null when FORCE_REDEPLOY is set", () => {
      writeLedger(net, "FeeManager", { proxy: "0xFEE" });
      process.env.FORCE_REDEPLOY = "1";
      expect(reuseFromLedger(hre, "FeeManager", undefined)).to.equal(null);
    });
  });

  describe("handOverOwnership", () => {
    const OWNER = "0x000000000000000000000000000000000000dEaD";
    const fake = ({ owner = VALID, transferErr, readErrAfterTransfer } = {}) => {
      let current = owner, transferred = false, sent = 0;
      return {
        sent: () => sent,
        owner: async () => {
          if (transferred && readErrAfterTransfer) throw new Error("rpc down");
          return current;
        },
        transferOwnership: async (to) => {
          sent++;
          transferred = true;
          if (transferErr) throw transferErr;
          current = to;
          return { wait: async () => {} };
        },
      };
    };

    it("sends nothing for a contract already owned by finalOwner", async () => {
      const c = fake({ owner: OWNER });
      await handOverOwnership("t", [c], OWNER);
      expect(c.sent()).to.equal(0);
    });

    it("transfers and reads back", async () => {
      const c = fake();
      await handOverOwnership("t", [c], OWNER);
      expect(c.sent()).to.equal(1);
      expect(await c.owner()).to.equal(OWNER);
    });

    it("a failing read-back never masks the transfer error", async () => {
      const c = fake({ transferErr: new Error("transfer reverted"), readErrAfterTransfer: true });
      await expect(handOverOwnership("t", [c], OWNER)).to.be.rejectedWith(/transfer reverted/);
    });

    it("throws when an owner does not end at finalOwner", async () => {
      const c = fake();
      c.transferOwnership = async () => ({ wait: async () => {} }); // silently does nothing
      await expect(handOverOwnership("t", [c], OWNER)).to.be.rejectedWith(/owner\(\) is not/);
    });
  });
});
