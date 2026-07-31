// Upgrades the live McgpEntitlementSwapV2 proxy to the operator-debit revision
// and runs its one-shot re-initializer.
//
// `upgradeProxy` does NOT re-run `initialize`, so the newly appended debit-cap
// slots would read zero. Zero fails closed here (every debitPremium would revert
// on the cap check) rather than opening a hole — but it would still be wrong, so
// initializeDebitCaps is called in the same run via the `call` option, making the
// upgrade and the initialisation a single atomic step.
//
// Env:
//   CHECK_ONLY=1              validate the layout and exit without upgrading
//   V2_MAX_DEBIT_PER_TX       per-call ceiling, MCGP (default 5000)
//   V2_DEBIT_WINDOW_SECONDS   rolling window (default 3600)
//   V2_DEBIT_WINDOW_CAP       debited per window, MCGP (default 25000)

const { readLedger, writeLedger } = require("./lib");

const DEFAULTS = { perTx: "5000", window: "3600", windowCap: "25000" };

async function main(hre) {
  const { ethers, upgrades, network } = hre;
  const ledger = readLedger(network.name);
  const proxy = process.env.PROXY || ledger.McgpEntitlementSwapV2?.proxy;
  if (!proxy) throw new Error(`no McgpEntitlementSwapV2 proxy recorded for ${network.name}`);

  const [signer] = await ethers.getSigners();
  const perTx = ethers.parseUnits(process.env.V2_MAX_DEBIT_PER_TX || DEFAULTS.perTx, 18);
  const window = BigInt(process.env.V2_DEBIT_WINDOW_SECONDS || DEFAULTS.window);
  const windowCap = ethers.parseUnits(process.env.V2_DEBIT_WINDOW_CAP || DEFAULTS.windowCap, 18);

  const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2");
  const live = Swap.attach(proxy);

  console.log("── upgrade McgpEntitlementSwapV2 (operator debit) ──────────────");
  console.log("network        ", network.name);
  console.log("proxy          ", proxy);
  console.log("signer         ", await signer.getAddress());
  console.log("owner on-chain ", await live.owner());
  console.log("maxDebitPerTx  ", ethers.formatUnits(perTx, 18));
  console.log("debitWindow    ", window.toString(), "s");
  console.log("debitWindowCap ", ethers.formatUnits(windowCap, 18));

  // Fails loudly if the appended slots would collide with anything existing.
  await upgrades.validateUpgrade(proxy, Swap, { kind: "uups" });
  console.log("storage layout : compatible");

  if (process.env.CHECK_ONLY) return console.log("CHECK_ONLY=1 — nothing sent.");

  const before = {
    accounted: (await live.accountedMcgp()).toString(),
    forwarder: await live.trustedForwarder(),
    maxCredit: (await live.maxCreditPerTx()).toString(),
  };

  const implBefore = await upgrades.erc1967.getImplementationAddress(proxy);

  const upgraded = await upgrades.upgradeProxy(proxy, Swap, {
    call: { fn: "initializeDebitCaps", args: [perTx, window, windowCap] },
  });
  await upgraded.waitForDeployment();

  // Read the ERC-1967 slot straight from storage. getImplementationAddress can
  // return the PRE-upgrade address here — it did on the first run, and the stale
  // value went into the ledger, which is the one record of what is deployed.
  const IMPL_SLOT = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc";
  const raw = await ethers.provider.getStorage(proxy, IMPL_SLOT);
  const impl = ethers.getAddress("0x" + raw.slice(26));
  if (impl.toLowerCase() === implBefore.toLowerCase()) {
    throw new Error(`implementation did not change (still ${impl}) — upgrade did not take`);
  }
  writeLedger(network.name, "McgpEntitlementSwapV2", { proxy, impl });

  // Prove the ledger survived and the new slots actually took.
  const after = {
    accounted: (await upgraded.accountedMcgp()).toString(),
    forwarder: await upgraded.trustedForwarder(),
    maxCredit: (await upgraded.maxCreditPerTx()).toString(),
    maxDebit: (await upgraded.maxDebitPerTx()).toString(),
    debitWindow: (await upgraded.debitWindow()).toString(),
    debitWindowCap: (await upgraded.debitWindowCap()).toString(),
  };
  console.log("post-upgrade   :", after);

  const problems = [];
  if (after.accounted !== before.accounted) problems.push("accountedMcgp changed across the upgrade");
  if (after.forwarder !== before.forwarder) problems.push("trustedForwarder changed across the upgrade");
  if (after.maxCredit !== before.maxCredit) problems.push("credit caps changed across the upgrade");
  if (after.maxDebit === "0" || after.debitWindow === "0" || after.debitWindowCap === "0") {
    problems.push("a debit cap is zero — the re-initializer did not run");
  }
  if (problems.length) throw new Error("post-upgrade verification FAILED:\n  - " + problems.join("\n  - "));

  console.log(`\nupgraded: proxy=${proxy} impl=${impl}`);
}

main(require("hardhat")).catch((e) => {
  console.error(e);
  process.exit(1);
});
