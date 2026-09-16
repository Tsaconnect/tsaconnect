// smart-contracts/deploy/mcgp-swap-v2.js
//
// Deploys the shared TSAForwarder and McgpEntitlementSwapV2 — DARK.
//
// "Dark" means the contract is deployed but inert: `trustedForwarder` and
// `feeManager` both stay zero, so V2 behaves exactly like the deployed V1 and
// nothing points at it. Gasless is switched on later by a single owner tx
// (setTrustedForwarder), which keeps the never-run-in-production relay path off
// the same release as the deployment itself.
//
// Ownership: the contract is initialized with the DEPLOYER as owner so this
// script can seed phases and operators, then handed to the real owner with
// transferOwnership at cutover — the same wire-as-deployer-then-transfer pattern
// used for the escrows (#59). It is NOT transferred here.
//
// Env:
//   CHECK_ONLY=1              log the resolved config and exit without deploying
//   OWNER_ADDRESS             owner at initialize (default: deployer)
//   V2_MIN_CREDIT             min operator credit, MCGP (default 0 = no dust floor)
//   V2_MAX_CREDIT_PER_TX      per-credit ceiling, MCGP (default 10000)
//   V2_CREDIT_WINDOW_SECONDS  rolling window (default 3600)
//   V2_CREDIT_WINDOW_CAP      credited per window, MCGP (default 50000)
//   FORWARDER_NAME            EIP-712 domain name (default TSAConnectForwarder)
//
// The three ceilings MUST be non-zero: V2 rejects zero because in V1 zero meant
// "unlimited", and the live V1 on Sonic mainnet is currently running with all
// caps at zero — i.e. an unbounded operator credit. Defaults below are ~500x
// headroom over observed usage (the whole V1 ledger is ~198 MCGP) while still
// bounding a compromised operator key.

const { resolveChain, requireAddr, writeLedger, readLedger } = require("./lib");

const DEFAULTS = {
  minCredit: "0",
  maxCreditPerTx: "10000",
  creditWindowSeconds: "3600",
  creditWindowCap: "50000",
  forwarderName: "TSAConnectForwarder",
};

async function deploy(hre, { chain } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = process.env.OWNER_ADDRESS || cfg.owner || (await deployer.getAddress());

  const mcgp = requireAddr(cfg, "MCGP", cfg.tokens.MCGP);
  const usdc = requireAddr(cfg, "USDC", cfg.tokens.USDC);

  const caps = {
    minCredit: ethers.parseUnits(process.env.V2_MIN_CREDIT || DEFAULTS.minCredit, 18),
    maxCreditPerTx: ethers.parseUnits(process.env.V2_MAX_CREDIT_PER_TX || DEFAULTS.maxCreditPerTx, 18),
    creditWindow: BigInt(process.env.V2_CREDIT_WINDOW_SECONDS || DEFAULTS.creditWindowSeconds),
    creditWindowCap: ethers.parseUnits(process.env.V2_CREDIT_WINDOW_CAP || DEFAULTS.creditWindowCap, 18),
  };
  const forwarderName = process.env.FORWARDER_NAME || DEFAULTS.forwarderName;

  const ledger = readLedger(network.name);
  const existingForwarder = ledger.TSAForwarder?.address || null;

  console.log("── McgpEntitlementSwapV2 (dark) ──────────────────────────────");
  console.log("network         ", network.name);
  console.log("deployer        ", await deployer.getAddress());
  console.log("owner @init     ", owner, owner === (await deployer.getAddress()) ? "(deployer — transfer at cutover)" : "");
  console.log("MCGP            ", mcgp);
  console.log("USDC            ", usdc);
  console.log("minCredit       ", ethers.formatUnits(caps.minCredit, 18));
  console.log("maxCreditPerTx  ", ethers.formatUnits(caps.maxCreditPerTx, 18));
  console.log("creditWindow    ", caps.creditWindow.toString(), "s");
  console.log("creditWindowCap ", ethers.formatUnits(caps.creditWindowCap, 18));
  console.log("forwarder       ", existingForwarder || `(will deploy, name="${forwarderName}")`);
  console.log("trustedForwarder  0x0 (DARK — flipped separately)");
  console.log("feeManager        0x0 (fees deferred to a later upgrade)");

  if (process.env.CHECK_ONLY) {
    console.log("CHECK_ONLY=1 — nothing deployed.");
    return { checkedOnly: true };
  }

  // 1. Forwarder — shared across contracts, so reuse it if the ledger has one.
  let forwarderAddr = existingForwarder;
  if (!forwarderAddr) {
    const Forwarder = await ethers.getContractFactory("TSAForwarder");
    const fwd = await Forwarder.deploy(forwarderName);
    await fwd.waitForDeployment();
    forwarderAddr = await fwd.getAddress();
    writeLedger(network.name, "TSAForwarder", { address: forwarderAddr });
    console.log(`TSAForwarder deployed at ${forwarderAddr}`);
  } else {
    console.log(`TSAForwarder reused from ledger: ${forwarderAddr}`);
  }

  // 2. Swap V2 proxy, dark.
  const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
  const swap = await upgrades.deployProxy(
    Swap,
    [owner, mcgp, usdc, caps.minCredit, caps.maxCreditPerTx, caps.creditWindow, caps.creditWindowCap],
    { kind: "uups" },
  );
  await swap.waitForDeployment();

  const proxy = await swap.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  writeLedger(network.name, "McgpEntitlementSwapV2", { proxy, impl });

  // 3. Prove it really is dark and correctly parameterised before anyone wires it.
  const checks = {
    owner: await swap.owner(),
    trustedForwarder: await swap.trustedForwarder(),
    feeManager: await swap.feeManager(),
    mcgpToken: await swap.mcgpToken(),
    usdcToken: await swap.usdcToken(),
    maxCreditPerTx: (await swap.maxCreditPerTx()).toString(),
    creditWindow: (await swap.creditWindow()).toString(),
    creditWindowCap: (await swap.creditWindowCap()).toString(),
    paused: await swap.paused(),
  };
  console.log("post-deploy reads:", checks);

  const zero = "0x0000000000000000000000000000000000000000";
  const problems = [];
  if (checks.trustedForwarder !== zero) problems.push("trustedForwarder is NOT zero — not dark");
  if (checks.feeManager !== zero) problems.push("feeManager is NOT zero — not dark");
  if (checks.owner.toLowerCase() !== owner.toLowerCase()) problems.push("owner mismatch");
  if (checks.mcgpToken.toLowerCase() !== mcgp.toLowerCase()) problems.push("MCGP mismatch");
  if (checks.usdcToken.toLowerCase() !== usdc.toLowerCase()) problems.push("USDC mismatch");
  if (checks.maxCreditPerTx === "0" || checks.creditWindow === "0" || checks.creditWindowCap === "0") {
    problems.push("a credit cap is zero — operator rate-limits would be OFF");
  }
  if (problems.length) {
    throw new Error(`post-deploy verification FAILED:\n  - ${problems.join("\n  - ")}`);
  }

  console.log(`McgpEntitlementSwapV2 proxy=${proxy} impl=${impl} (dark, verified)`);
  return { proxy, impl, forwarder: forwarderAddr };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => {
    console.error(e);
    process.exit(1);
  });
}

module.exports = { deploy };
