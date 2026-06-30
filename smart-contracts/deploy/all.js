// smart-contracts/deploy/all.js
const { resolveChain, reuseFromLedger } = require("./lib");
const feemanager = require("./feemanager");
const p2p = require("./p2p-escrow");
const product = require("./product-escrow");
const service = require("./service-contact");
const distributor = require("./mcgp-distributor");

async function deployAll(hre, { chain } = {}) {
  const cfg = chain || resolveChain(hre);
  // When `chain` was provided by the caller (test path), forward it so sub-modules
  // skip ledger writes. When called from the CLI (`chain` undefined), omit it so
  // each sub-module resolves the chain itself and persists its ledger entry.
  const passChain = chain ? { chain: cfg } : {};

  // On the CLI path, reuse a contract already in the ledger so a re-run after a
  // mid-sequence failure resumes rather than orphaning fresh proxies. Set
  // FORCE_REDEPLOY=1 to redeploy everything.
  const step = async (key, run) => {
    const existing = reuseFromLedger(hre, key, chain);
    if (existing) {
      console.log(`${key} already deployed (${existing.proxy || existing.address}) — reusing (FORCE_REDEPLOY=1 to redeploy)`);
      return existing;
    }
    return run();
  };

  const out = {};
  out.FeeManager = await step("FeeManager", () => feemanager.deploy(hre, { ...passChain }));
  const fm = out.FeeManager.proxy;
  out.P2PEscrowV3 = await step("P2PEscrowV3", () => p2p.deploy(hre, { ...passChain, feeManager: fm }));
  out.ProductEscrowV2 = await step("ProductEscrowV2", () => product.deploy(hre, { ...passChain, feeManager: fm }));
  out.ServiceContactV2 = await step("ServiceContactV2", () => service.deploy(hre, { ...passChain, feeManager: fm }));
  if (cfg.tokens.MCGP) {
    out.MCGPRewardDistributor = await step("MCGPRewardDistributor", () => distributor.deploy(hre, { ...passChain }));
  } else {
    console.log("MCGP not configured for this network — skipping MCGPRewardDistributor");
  }
  return out;
}

if (require.main === module) {
  deployAll(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deployAll };
