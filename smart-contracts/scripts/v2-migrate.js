// scripts/v2-migrate.js
//
// Imports a V1 snapshot into V2 via migratePremium, in batches, with a
// verification pass that compares every migrated balance back against the
// snapshot. Dry-run by default.
//
// Preconditions it ENFORCES (refuses to run otherwise):
//   - snapshot totals are internally consistent (sum == accountedMcgp)
//   - snapshot has no pendingSettle (in-flight debits are not migratable)
//   - V1 is paused AND its accountedMcgp still matches the snapshot, re-checked
//     again after the final batch and before sealing
//   - V2 holds enough MCGP to back the imported total
//   - migration is not already sealed
//
// PAUSING THE SOURCE IS NOT A FREEZE. On the V1 swap, `withdrawPremiumToWallet`
// is the one exit WITHOUT `whenNotPaused`, so a bought-premium holder can still
// pull real MCGP out of a paused contract. Migration COPIES balances and never
// zeroes the source, so between snapshot and seal a holder can withdraw on the
// source and keep the imported balance on the target — spending the same
// entitlement twice. The equality re-check below is what catches that; it is a
// detector, not a prevention. A future migration should either use a source
// whose every exit is pause-gated, or zero the source ledger before sealing.
//
// Usage:
//   SNAPSHOT=snapshots/v1-premium-146-77022657.json \
//     npx hardhat run scripts/v2-migrate.js --network sonicMainnet
//
//   EXECUTE=1 SNAPSHOT=… npx hardhat run scripts/v2-migrate.js --network sonicMainnet
//
// Env:
//   SNAPSHOT    path to the JSON from v1-snapshot.js (required)
//   BATCH       holders per transaction (default 25)
//   EXECUTE=1   broadcast
//   SEAL=1      call closeMigration() once verification passes
//   SKIP_PAUSE_CHECK=1  proceed even if V1 is unpaused (rehearsal only)
const fs = require("fs");
const { ethers } = require("hardhat");

// TARGET is the swap being migrated INTO. The old 0x5e19… proxy is PM-owned and
// abandoned; the live target is the engineering-owned deploy, so this must be
// passed explicitly rather than defaulted to something nobody can sign for.
const V2 = process.env.TARGET || process.env.V2_PROXY;
const V1 = process.env.V1_PROXY || "0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52";

const V2_ABI = [
  "function owner() view returns (address)",
  "function accountedMcgp() view returns (uint256)",
  "function migrationClosed() view returns (bool)",
  "function premiumMigrated(address) view returns (bool)",
  "function premiumBought(address) view returns (uint256)",
  "function premiumCredited(address) view returns (uint256)",
  "function backing() view returns (uint256 held, uint256 owed)",
  "function migratePremium(address[] users, uint256[] bought, uint256[] credited)",
  "function closeMigration()",
];
const V1_ABI = ["function paused() view returns (bool)", "function accountedMcgp() view returns (uint256)"];

const execute = process.env.EXECUTE === "1";
const f = (v) => ethers.formatUnits(v, 18);

async function main() {
  const snapPath = process.env.SNAPSHOT;
  if (!snapPath) throw new Error("SNAPSHOT=<path to v1-snapshot.js output> is required");
  if (!V2 || !ethers.isAddress(V2)) throw new Error("TARGET=<swap proxy to migrate into> is required");
  const snap = JSON.parse(fs.readFileSync(snapPath, "utf8"));

  const [signer] = await ethers.getSigners();
  const net = await ethers.provider.getNetwork();
  const v2 = new ethers.Contract(V2, V2_ABI, signer);
  const v1 = new ethers.Contract(V1, V1_ABI, ethers.provider);

  console.log(`\n=== V1 → V2 premium migration — ${execute ? "EXECUTE" : "DRY RUN"} ===`);
  console.log(`network:  ${net.name} (chainId ${net.chainId})`);
  console.log(`snapshot: ${snapPath} (block ${snap.block}, ${snap.holders.length} holders)`);
  console.log(`V2:       ${V2}`);
  console.log(`signer:   ${signer.address}`);

  if (Number(net.chainId) !== snap.chainId) {
    throw new Error(`snapshot is for chainId ${snap.chainId}, connected to ${net.chainId}`);
  }

  // ── snapshot self-consistency ─────────────────────────────────────
  let sumBought = 0n;
  let sumCredited = 0n;
  let sumPending = 0n;
  for (const h of snap.holders) {
    sumBought += BigInt(h.bought);
    sumCredited += BigInt(h.credited);
    sumPending += BigInt(h.pendingSettle || "0");
  }
  const importTotal = sumBought + sumCredited;
  if (sumBought + sumCredited + sumPending !== BigInt(snap.totals.accountedMcgp)) {
    throw new Error("snapshot is internally inconsistent — regenerate it");
  }
  if (sumPending > 0n) {
    throw new Error(
      `snapshot has ${f(sumPending)} MCGP in pendingSettle. In-flight debits cannot be ` +
        `migrated (the debits[ref] record lives on V1). Settle/reverse them, then re-snapshot.`,
    );
  }
  console.log(`\nto import: ${f(importTotal)} MCGP  (bought ${f(sumBought)} + credited ${f(sumCredited)})`);

  // ── preconditions ─────────────────────────────────────────────────
  const v1Paused = await v1.paused();
  console.log(`V1 paused: ${v1Paused}`);
  if (!v1Paused && !process.env.SKIP_PAUSE_CHECK) {
    throw new Error(
      "V1 is NOT paused — balances can still move and the snapshot would go stale mid-migration.\n" +
        "Pause V1 first (SKIP_PAUSE_CHECK=1 to override for a rehearsal).",
    );
  }
  if (BigInt(snap.totals.accountedMcgp) !== (await v1.accountedMcgp())) {
    throw new Error("V1 accountedMcgp has CHANGED since the snapshot — re-snapshot before migrating");
  }

  const owner = await v2.owner();
  if (owner.toLowerCase() !== signer.address.toLowerCase()) {
    console.log(`⚠️  signer is not the V2 owner (${owner})`);
    if (execute) throw new Error("refusing to broadcast: signer is not the owner");
  }
  if (await v2.migrationClosed()) throw new Error("migration is already sealed on V2");

  const [held, owed] = await v2.backing();
  const needed = owed + importTotal;
  console.log(`\nV2 MCGP held: ${f(held)}`);
  console.log(`V2 owed now : ${f(owed)}`);
  console.log(`owed after  : ${f(needed)}`);
  if (held < needed) {
    throw new Error(
      `V2 is underfunded: holds ${f(held)}, needs ${f(needed)} — short ${f(needed - held)} MCGP. Fund it first.`,
    );
  }
  console.log(`funding OK  : surplus after import ${f(held - needed)} MCGP`);

  // ── record pre-state ──────────────────────────────────────────────
  // V2 already holds a little entitlement from smoke tests, and migration ADDS
  // to existing balances. Verification therefore compares deltas, not absolutes.
  const accountedBefore = await v2.accountedMcgp();
  const preBalances = {};
  for (const h of snap.holders) {
    preBalances[h.user] = await Promise.all([v2.premiumBought(h.user), v2.premiumCredited(h.user)]);
    const [pb, pc] = preBalances[h.user];
    if (pb > 0n || pc > 0n) {
      console.log(`  note: ${h.user} already holds bought=${f(pb)} credited=${f(pc)} on V2 — import will ADD`);
    }
  }

  // ── skip anyone already migrated (resumable) ──────────────────────
  // Verification below compares against what THIS run imports, so a re-run
  // (every holder already migrated) must expect a delta of zero rather than
  // another full import — otherwise a successful, complete migration looks
  // like a mismatch on the second invocation.
  const pending = [];
  const alreadyMigrated = new Set();
  for (const h of snap.holders) {
    if (await v2.premiumMigrated(h.user)) {
      alreadyMigrated.add(h.user);
      console.log(`  already migrated, skipping: ${h.user}`);
      continue;
    }
    pending.push(h);
  }
  const importTotalThisRun = pending.reduce(
    (s, h) => s + BigInt(h.bought) + BigInt(h.credited),
    0n,
  );
  if (pending.length === 0) {
    console.log("\nnothing left to migrate.");
  } else {
    const size = Number(process.env.BATCH || 25);
    const batches = [];
    for (let i = 0; i < pending.length; i += size) batches.push(pending.slice(i, i + size));
    console.log(`\n${pending.length} holders in ${batches.length} batch(es) of up to ${size}`);

    if (!execute) {
      console.log("\nDRY RUN — nothing sent. Re-run with EXECUTE=1.");
    } else {
      for (const [i, batch] of batches.entries()) {
        console.log(`\nbatch ${i + 1}/${batches.length} (${batch.length} holders)`);
        const tx = await v2.migratePremium(
          batch.map((h) => h.user),
          batch.map((h) => h.bought),
          batch.map((h) => h.credited),
        );
        console.log(`  tx: ${tx.hash}`);
        const rcpt = await tx.wait();
        console.log(`  mined in block ${rcpt.blockNumber} (status ${rcpt.status})`);
        if (rcpt.status !== 1) throw new Error("batch reverted");
      }
    }
  }

  // ── verification ──────────────────────────────────────────────────
  if (!execute) return;
  console.log(`\n--- verifying every holder against the snapshot ---`);
  let bad = 0;
  for (const h of snap.holders) {
    const [b, c] = await Promise.all([v2.premiumBought(h.user), v2.premiumCredited(h.user)]);
    const [pb, pc] = preBalances[h.user];
    // A holder skipped as already-migrated contributes nothing this run.
    const add = alreadyMigrated.has(h.user) ? [0n, 0n] : [BigInt(h.bought), BigInt(h.credited)];
    const wantB = pb + add[0];
    const wantC = pc + add[1];
    if (b !== wantB || c !== wantC) {
      console.log(`  MISMATCH ${h.user}: on-chain bought=${b} credited=${c}, expected ${wantB}/${wantC}`);
      bad++;
    }
  }
  const accountedAfter = await v2.accountedMcgp();
  const [heldAfter, owedAfter] = await v2.backing();
  console.log(`\naccountedMcgp before: ${accountedBefore}`);
  console.log(`imported this run   : ${importTotalThisRun}`);
  console.log(`accountedMcgp after : ${accountedAfter}`);
  console.log(`snapshot total owed : ${snap.totals.accountedMcgp}`);
  console.log(`V2 backing: held ${f(heldAfter)} >= owed ${f(owedAfter)} ? ${heldAfter >= owedAfter}`);

  if (accountedAfter !== accountedBefore + importTotalThisRun) {
    throw new Error("accountedMcgp delta != imported total — investigate before sealing");
  }
  if (bad > 0) throw new Error(`${bad} holder(s) do not match the snapshot`);

  // Re-assert against the SOURCE, not just the snapshot. The pre-flight check
  // ran before the batches; a holder withdrawing from V1 mid-migration (which
  // the pause does not prevent) would otherwise be imported at a stale balance
  // and sealed in permanently, with the verification above still reporting a
  // clean match because it only ever compares to the snapshot.
  const v1AccountedNow = await v1.accountedMcgp();
  if (v1AccountedNow !== BigInt(snap.totals.accountedMcgp)) {
    throw new Error(
      `SOURCE LEDGER MOVED during migration: V1 accountedMcgp is now ${v1AccountedNow}, ` +
        `snapshot had ${snap.totals.accountedMcgp}. Do NOT seal — re-snapshot and reconcile ` +
        `the difference before continuing.`,
    );
  }
  console.log(`source unchanged: V1 accountedMcgp still ${v1AccountedNow}`);
  if (heldAfter < owedAfter) throw new Error("V2 is UNDERBACKED after migration");
  console.log(`\nALL ${snap.holders.length} HOLDERS MATCH.`);

  if (process.env.SEAL === "1") {
    console.log(`\nsealing migration…`);
    const tx = await v2.closeMigration();
    console.log(`  tx: ${tx.hash}`);
    await tx.wait();
    console.log(`  migrationClosed = ${await v2.migrationClosed()}`);
  } else {
    console.log(`\nNOT sealed. Re-run with SEAL=1 once you are satisfied — after sealing,`);
    console.log(`bought premium can only ever come from a real USDC payment again.`);
  }
}

main().catch((e) => {
  console.error(`\nFAILED: ${e.message}`);
  process.exit(1);
});
