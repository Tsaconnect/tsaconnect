// smart-contracts/deploy/all.js
const { resolveChain } = require("./lib");
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

  const out = {};
  out.FeeManager = await feemanager.deploy(hre, { ...passChain });
  const fm = out.FeeManager.proxy;
  out.P2PEscrowV3 = await p2p.deploy(hre, { ...passChain, feeManager: fm });
  out.ProductEscrowV2 = await product.deploy(hre, { ...passChain, feeManager: fm });
  out.ServiceContactV2 = await service.deploy(hre, { ...passChain, feeManager: fm });
  if (cfg.tokens.MCGP) {
    out.MCGPRewardDistributor = await distributor.deploy(hre, { ...passChain });
  } else {
    console.log("MCGP not configured for this network — skipping MCGPRewardDistributor");
  }
  return out;
}

if (require.main === module) {
  deployAll(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deployAll };
