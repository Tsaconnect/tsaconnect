// scripts/v1-snapshot.js
//
// Snapshot every premium balance on the V1 swap proxy to JSON, for import into
// V2 via migratePremium.
//
// Read-only. Writes exact WEI strings, never formatted decimals — a snapshot
// transcribed from human-readable output was already wrong by 2,075 wei once,
// and every wei of discrepancy is somebody's money.
//
// Holder discovery is by event scan (PremiumBought / PremiumCredited /
// PremiumSpent, both sides of a spend), then the snapshot VERIFIES it found
// everyone: Σ(bought + credited + pendingSettle) must equal accountedMcgp
// exactly. If it doesn't, a holder was missed and the file is not written.
//
// Usage:
//   npx hardhat run scripts/v1-snapshot.js --network sonicMainnet
//   OUT=snapshots/v1-2026-08-04.json npx hardhat run scripts/v1-snapshot.js --network sonicMainnet
const fs = require("fs");
const path = require("path");
const { ethers } = require("hardhat");

const V1 = process.env.V1_PROXY || "0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52";

const ABI = [
  "event PremiumBought(address indexed buyer, uint256 mcgpAmount, uint256 usdcAmount)",
  "event PremiumCredited(address indexed user, uint256 mcgpAmount, bytes32 indexed ref)",
  "event PremiumSpent(address indexed from, address indexed to, uint256 mcgpAmount, bytes32 indexed ref)",
  "event PremiumDebitPending(address indexed user, uint256 mcgpAmount, bytes32 indexed ref)",
  "event LegacySeeded(address indexed user, uint256 entitlement)",
  "function premiumBought(address) view returns (uint256)",
  "function premiumCredited(address) view returns (uint256)",
  "function pendingSettle(address) view returns (uint256)",
  "function accountedMcgp() view returns (uint256)",
  "function paused() view returns (bool)",
];

const CHUNK = 100_000;

// findDeploymentBlock binary-searches the first block where the proxy has code.
// Scanning to genesis costs hundreds of pointless round trips on a chain this
// long; the contract cannot have emitted anything before it existed.
async function findDeploymentBlock(addr, latest) {
  let lo = 0;
  let hi = latest;
  while (lo < hi) {
    const mid = Math.floor((lo + hi) / 2);
    const code = await ethers.provider.getCode(addr, mid);
    if (code === "0x") lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

async function main() {
  const c = new ethers.Contract(V1, ABI, ethers.provider);
  const net = await ethers.provider.getNetwork();
  const block = await ethers.provider.getBlockNumber();

  console.log(`\n=== V1 premium snapshot ===`);
  console.log(`network: ${net.name} (chainId ${net.chainId})`);
  console.log(`proxy:   ${V1}`);
  console.log(`block:   ${block}`);
  console.log(`paused:  ${await c.paused()}  <- should be TRUE at cutover, so balances cannot move`);

  // ── discover holders ──────────────────────────────────────────────
  const holders = new Set();
  const filters = [
    c.filters.PremiumBought(),
    c.filters.PremiumCredited(),
    c.filters.PremiumSpent(),
    c.filters.PremiumDebitPending(),
    c.filters.LegacySeeded(),
  ];
  const deployedAt = process.env.FROM_BLOCK
    ? Number(process.env.FROM_BLOCK)
    : await findDeploymentBlock(V1, block);
  console.log(`deployed at block ${deployedAt}`);

  process.stdout.write("scanning logs");
  let to = block;
  while (to >= deployedAt) {
    const from = Math.max(deployedAt, to - CHUNK);
    for (const f of filters) {
      try {
        for (const ev of await c.queryFilter(f, from, to)) {
          for (const a of ev.args) {
            if (typeof a === "string" && ethers.isAddress(a)) holders.add(ethers.getAddress(a));
          }
        }
      } catch {
        /* provider range limits — next chunk */
      }
    }
    process.stdout.write(".");
    to = from - 1;
  }
  console.log(` found ${holders.size} candidate addresses`);

  // ── read exact balances ───────────────────────────────────────────
  const rows = [];
  let sumBought = 0n;
  let sumCredited = 0n;
  let sumPending = 0n;
  for (const user of holders) {
    const [bought, credited, pending] = await Promise.all([
      c.premiumBought(user),
      c.premiumCredited(user),
      c.pendingSettle(user),
    ]);
    if (bought === 0n && credited === 0n && pending === 0n) continue;
    sumBought += bought;
    sumCredited += credited;
    sumPending += pending;
    rows.push({
      user,
      bought: bought.toString(),
      credited: credited.toString(),
      pendingSettle: pending.toString(),
    });
  }
  rows.sort((a, b) => (BigInt(b.bought) + BigInt(b.credited) > BigInt(a.bought) + BigInt(a.credited) ? 1 : -1));

  const accounted = await c.accountedMcgp();
  const total = sumBought + sumCredited + sumPending;
  const f = (v) => ethers.formatUnits(v, 18);

  console.log(`\nholders with a balance: ${rows.length}`);
  for (const r of rows) {
    console.log(`  ${r.user}  bought=${f(r.bought)}  credited=${f(r.credited)}` +
      (r.pendingSettle !== "0" ? `  PENDING=${f(r.pendingSettle)}` : ""));
  }
  console.log(`\nsum bought    : ${sumBought}  (${f(sumBought)})`);
  console.log(`sum credited  : ${sumCredited}  (${f(sumCredited)})`);
  console.log(`sum pending   : ${sumPending}  (${f(sumPending)})`);
  console.log(`sum total     : ${total}`);
  console.log(`accountedMcgp : ${accounted}`);

  // ── completeness proof ────────────────────────────────────────────
  if (total !== accounted) {
    const diff = accounted > total ? accounted - total : total - accounted;
    throw new Error(
      `INCOMPLETE SNAPSHOT: holder sum != accountedMcgp (off by ${diff} wei / ${f(diff)} MCGP).\n` +
        `A holder was missed by the event scan. Do NOT migrate from this file.`,
    );
  }
  console.log(`COMPLETE: holder sum == accountedMcgp exactly.`);

  if (sumPending > 0n) {
    console.log(
      `\n⚠️  pendingSettle is non-zero. In-flight debits are NOT migratable — the\n` +
        `   debits[ref] record lives on V1. Settle or reverse them, then re-snapshot.`,
    );
  }

  const out = path.resolve(process.env.OUT || `snapshots/v1-premium-${net.chainId}-${block}.json`);
  fs.mkdirSync(path.dirname(out), { recursive: true });
  fs.writeFileSync(
    out,
    JSON.stringify(
      {
        network: net.name,
        chainId: Number(net.chainId),
        v1Proxy: V1,
        block,
        takenAtPaused: await c.paused(),
        totals: {
          bought: sumBought.toString(),
          credited: sumCredited.toString(),
          pendingSettle: sumPending.toString(),
          accountedMcgp: accounted.toString(),
        },
        holders: rows,
      },
      null,
      2,
    ),
  );
  console.log(`\nwrote ${out}`);
}

main().catch((e) => {
  console.error(`\nFAILED: ${e.message}`);
  process.exit(1);
});
