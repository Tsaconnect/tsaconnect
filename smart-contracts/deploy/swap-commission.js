// smart-contracts/deploy/swap-commission.js
//
// Deploys a FULLY COMMISSIONED McgpEntitlementSwapV2 proxy — owned by the
// deployer, priced, with the backend operator authorized and debit caps set —
// ready to receive the V1 balance migration.
//
// WHY A FRESH DEPLOY. The existing V2 proxy (0x5e19…D6CE) is owned by a key
// engineering does not hold, so it cannot be upgraded to add `migratePremium`.
// V1's ownership WAS transferred to engineering (2026-08-04), which is what
// makes this path viable: pause V1, withdraw its free excess, fund this
// contract, migrate. `deploy/mcgp-swap-v2.js` is not reused because it predates
// the debit caps and still passes 7 args to a 10-arg initialize.
//
// Deployed LIVE, not dark: the forwarder is wired here because the gasless
// path is already proven on the old proxy, and a second owner transaction per
// environment is a step that gets forgotten.
//
// Env:
//   CHECK_ONLY=1   print the resolved config and exit
//   OPERATOR       backend operator to authorize (default: the live IT operator)
//   FORWARDER      trusted forwarder (default: TSAForwarder from the ledger)
//   BUY_PRICE      USDC 6dp per MCGP for PremiumBuy  (default 20000 = $0.02)
//   SELL_PRICE     USDC 6dp per MCGP for PremiumSell (default 18000 = $0.018)
const { resolveChain, requireAddr, writeLedger, readLedger } = require("./lib");

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;

const DEFAULTS = {
  minCredit: "0",
  maxCreditPerTx: "10000",
  creditWindowSeconds: "3600",
  creditWindowCap: "50000",
  maxDebitPerTx: "5000",
  debitWindowSeconds: "3600",
  debitWindowCap: "25000",
  operator: "0x3D64a20A645fC8d10c2843A3682E87f2053ae4E7",
  buyPrice: "20000",
  sellPrice: "18000",
};

async function deploy(hre, { chain } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const deployerAddr = await deployer.getAddress();

  const mcgp = requireAddr(cfg, "MCGP", cfg.tokens.MCGP);
  const usdc = requireAddr(cfg, "USDC", cfg.tokens.USDC);
  const ledger = readLedger(network.name);
  const forwarder = process.env.FORWARDER || ledger.TSAForwarder?.address || ethers.ZeroAddress;
  const operator = ethers.getAddress(process.env.OPERATOR || DEFAULTS.operator);
  const buyPrice = BigInt(process.env.BUY_PRICE || DEFAULTS.buyPrice);
  const sellPrice = BigInt(process.env.SELL_PRICE || DEFAULTS.sellPrice);

  const p = (k) => ethers.parseUnits(process.env[`V2_${k.toUpperCase()}`] || DEFAULTS[k], 18);
  const caps = [
    p("minCredit"),
    p("maxCreditPerTx"),
    BigInt(process.env.V2_CREDIT_WINDOW_SECONDS || DEFAULTS.creditWindowSeconds),
    p("creditWindowCap"),
    p("maxDebitPerTx"),
    BigInt(process.env.V2_DEBIT_WINDOW_SECONDS || DEFAULTS.debitWindowSeconds),
    p("debitWindowCap"),
  ];

  console.log("── McgpEntitlementSwapV2 — fresh commissioned deploy ───────────");
  console.log("network        ", network.name);
  console.log("deployer/owner ", deployerAddr);
  console.log("MCGP / USDC    ", mcgp, "/", usdc);
  console.log("operator       ", operator);
  console.log("forwarder      ", forwarder === ethers.ZeroAddress ? "0x0 (gasless off)" : forwarder);
  console.log("prices         ", `buy ${Number(buyPrice) / 1e6} / sell ${Number(sellPrice) / 1e6} USDC per MCGP`);
  console.log("credit caps    ", caps.slice(0, 4).map(String).join(", "));
  console.log("debit caps     ", caps.slice(4).map(String).join(", "));

  if (process.env.CHECK_ONLY) {
    console.log("CHECK_ONLY=1 — nothing deployed.");
    return { checkedOnly: true };
  }

  const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
  const swap = await upgrades.deployProxy(Swap, [deployerAddr, mcgp, usdc, ...caps], { kind: "uups" });
  await swap.waitForDeployment();
  const proxy = await swap.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  console.log(`\nproxy ${proxy}\nimpl  ${impl}`);

  // ── commission ────────────────────────────────────────────────────
  console.log("\nconfiguring…");
  await (await swap.addPhase(PREMIUM_BUY, buyPrice, "premium buy")).wait();
  await (await swap.addPhase(PREMIUM_SELL, sellPrice, "premium sell")).wait();
  await (await swap.setActivePhase(PREMIUM_BUY, 0)).wait();
  await (await swap.setActivePhase(PREMIUM_SELL, 0)).wait();
  console.log("  phases set");
  await (await swap.addOperator(operator)).wait();
  console.log("  operator authorized");
  if (forwarder !== ethers.ZeroAddress) {
    await (await swap.setTrustedForwarder(forwarder)).wait();
    console.log("  forwarder wired");
  }

  // ── verify ────────────────────────────────────────────────────────
  const one = ethers.parseUnits("1", 18);
  const checks = {
    owner: await swap.owner(),
    operator: await swap.operators(operator),
    trustedForwarder: await swap.trustedForwarder(),
    quoteBuy: (await swap.quote(PREMIUM_BUY, one)).toString(),
    quoteSell: (await swap.quote(PREMIUM_SELL, one)).toString(),
    maxDebitPerTx: (await swap.maxDebitPerTx()).toString(),
    migrationClosed: await swap.migrationClosed(),
    accountedMcgp: (await swap.accountedMcgp()).toString(),
    paused: await swap.paused(),
  };
  console.log("\npost-deploy reads:", checks);

  const problems = [];
  if (checks.owner.toLowerCase() !== deployerAddr.toLowerCase()) problems.push("owner is not the deployer");
  if (!checks.operator) problems.push("operator NOT authorized — bills and IT would revert");
  if (checks.quoteBuy !== buyPrice.toString()) problems.push("PremiumBuy quote wrong");
  if (checks.quoteSell !== sellPrice.toString()) problems.push("PremiumSell quote wrong");
  if (checks.maxDebitPerTx === "0") problems.push("maxDebitPerTx is 0 — every debit would revert");
  if (checks.migrationClosed) problems.push("migration is already sealed — cannot import V1 balances");
  if (checks.accountedMcgp !== "0") problems.push("fresh contract already owes MCGP");
  if (problems.length) throw new Error(`verification FAILED:\n  - ${problems.join("\n  - ")}`);

  // Preserve the abandoned PM-owned proxy rather than overwriting its record.
  if (ledger.McgpEntitlementSwapV2 && ledger.McgpEntitlementSwapV2.proxy !== proxy) {
    writeLedger(network.name, "McgpEntitlementSwapV2_abandoned", {
      ...ledger.McgpEntitlementSwapV2,
      note: "PM-owned, not upgradeable by engineering; superseded",
    });
  }
  writeLedger(network.name, "McgpEntitlementSwapV2", { proxy, impl });

  console.log(`\nCOMMISSIONED. proxy=${proxy}`);
  console.log("Next: pause V1 → snapshot → fund → migrate → seal → repoint the backend.");
  return { proxy, impl };
}

module.exports = deploy;

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => {
    console.error(e);
    process.exit(1);
  });
}
