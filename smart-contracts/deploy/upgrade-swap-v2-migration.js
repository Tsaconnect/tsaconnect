// Upgrades the live McgpEntitlementSwapV2 proxy to the revision that adds the
// one-time V1 premium import (`migratePremium` / `closeMigration`).
//
// No re-initializer is needed: both appended slots want their zero defaults
// (`premiumMigrated` empty, `migrationClosed` false — migration OPEN). That is
// the only safe default here; a non-zero `migrationClosed` would seal the
// contract before anything could be imported.
//
// Env:
//   CHECK_ONLY=1   validate the storage layout and exit without upgrading
const { readLedger, writeLedger } = require("./lib");

async function main(hre) {
  const { ethers, upgrades, network } = hre;
  const ledger = readLedger(network.name);
  const proxy = process.env.PROXY || ledger.McgpEntitlementSwapV2?.proxy;
  if (!proxy) throw new Error(`no McgpEntitlementSwapV2 proxy recorded for ${network.name}`);

  const [signer] = await ethers.getSigners();
  const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
  const live = Swap.attach(proxy);

  console.log("── upgrade McgpEntitlementSwapV2 (V1 premium migration) ────────");
  console.log("network        ", network.name);
  console.log("proxy          ", proxy);
  console.log("signer         ", await signer.getAddress());
  console.log("owner on-chain ", await live.owner());

  const before = {
    accounted: (await live.accountedMcgp()).toString(),
    maxDebitPerTx: (await live.maxDebitPerTx()).toString(),
    forwarder: await live.trustedForwarder(),
  };
  console.log("accountedMcgp  ", ethers.formatUnits(before.accounted, 18));

  // Fails loudly if the appended slots would collide with anything existing.
  await upgrades.validateUpgrade(proxy, Swap, { kind: "uups" });
  console.log("storage layout : compatible");

  if (process.env.CHECK_ONLY) return console.log("CHECK_ONLY=1 — nothing sent.");

  const upgraded = await upgrades.upgradeProxy(proxy, Swap, { kind: "uups" });
  await upgraded.waitForDeployment();
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  console.log("new impl       ", impl);

  // State must survive the upgrade untouched, and migration must land OPEN.
  const after = {
    accounted: (await upgraded.accountedMcgp()).toString(),
    maxDebitPerTx: (await upgraded.maxDebitPerTx()).toString(),
    forwarder: await upgraded.trustedForwarder(),
  };
  for (const k of Object.keys(before)) {
    if (String(before[k]).toLowerCase() !== String(after[k]).toLowerCase()) {
      throw new Error(`state drifted across the upgrade: ${k} ${before[k]} → ${after[k]}`);
    }
  }
  const closed = await upgraded.migrationClosed();
  console.log("state preserved: yes");
  console.log("migrationClosed:", closed, closed ? "  ** should be false **" : "(migration open)");
  if (closed) throw new Error("migration is sealed on a freshly upgraded proxy — refusing to continue");

  ledger.McgpEntitlementSwapV2 = { proxy, impl, updatedAt: new Date().toISOString() };
  writeLedger(network.name, ledger);
  console.log("ledger updated.");
}

module.exports = main;

if (require.main === module) {
  main(require("hardhat")).catch((e) => {
    console.error(e);
    process.exit(1);
  });
}
