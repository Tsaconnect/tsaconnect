// scripts/rehearse-fresh-cutover.js
//
// Dress rehearsal of the CURRENT plan against a fork of live mainnet:
// deploy a fresh commissioned swap owned by engineering, freeze V1, move V1's
// free excess across, import the 13 holders, seal, and prove the flows work.
//
// Differs from rehearse-cutover.js (which upgraded the PM-owned V2 proxy —
// no longer possible; PM transferred V1 only).
//
// Usage:
//   FORK_RPC=https://rpc.soniclabs.com SNAPSHOT=snapshots/<file>.json \
//     npx hardhat run scripts/rehearse-fresh-cutover.js
const fs = require("fs");
const { ethers, upgrades, network } = require("hardhat");

const V1 = "0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52";
const MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
const USDC = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894";
const FORWARDER = "0x569F8a58dFCCaA600d92B641F1654dCEd2dA23e4";
const ENGINEERING = "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9"; // now owns V1
const OPERATOR = "0x3D64a20A645fC8d10c2843A3682E87f2053ae4E7";

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const f = (v) => ethers.formatUnits(v, 18);
const step = (n, s) => console.log(`\n─── ${n}. ${s} ${"─".repeat(Math.max(0, 52 - s.length))}`);

async function impersonate(addr) {
  await network.provider.request({ method: "hardhat_impersonateAccount", params: [addr] });
  await network.provider.send("hardhat_setBalance", [addr, "0x21E19E0C9BAB2400000"]);
  return ethers.getSigner(addr);
}

async function main() {
  if (!process.env.FORK_RPC) throw new Error("FORK_RPC is required");
  const snap = JSON.parse(fs.readFileSync(process.env.SNAPSHOT, "utf8"));
  console.log(`=== FRESH-DEPLOY CUTOVER REHEARSAL (fork) ===`);
  console.log(`snapshot: ${snap.holders.length} holders, ${f(snap.totals.accountedMcgp)} MCGP owed`);

  // Mine one local block before reading anything. At the fork head, Hardhat
  // treats calls as "historical" and demands a hardfork activation table for
  // chain 146, which it has no built-in entry for; once a local block exists,
  // reads resolve against local state and the question disappears.
  await network.provider.send("hardhat_mine", ["0x1"]);

  const eng = await impersonate(ENGINEERING);
  const operator = await impersonate(OPERATOR);
  const mcgp = new ethers.Contract(MCGP, [
    "function balanceOf(address) view returns (uint256)",
    "function approve(address,uint256) returns (bool)",
  ], eng);
  const v1 = new ethers.Contract(V1, [
    "function pause()", "function paused() view returns (bool)",
    "function owner() view returns (address)",
    "function withdraw(address,address,uint256)",
    "function backing() view returns (uint256,uint256)",
  ], eng);

  // ── 0. confirm engineering really owns V1 ─────────────────────────
  step(0, "verify V1 ownership");
  const v1Owner = await v1.owner();
  console.log(`   V1 owner = ${v1Owner}`);
  if (v1Owner.toLowerCase() !== ENGINEERING.toLowerCase()) {
    throw new Error("V1 is not owned by the engineering key — the whole plan depends on this");
  }

  // ── 1. deploy commissioned swap ───────────────────────────────────
  step(1, "deploy fresh commissioned swap");
  const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2", eng);
  const caps = [
    0n,
    ethers.parseUnits("10000", 18),
    3600n,
    ethers.parseUnits("50000", 18),
    ethers.parseUnits("5000", 18),
    3600n,
    ethers.parseUnits("25000", 18),
  ];
  const swap = await upgrades.deployProxy(Swap, [ENGINEERING, MCGP, USDC, ...caps], { kind: "uups" });
  await swap.waitForDeployment();
  const proxy = await swap.getAddress();
  await (await swap.addPhase(PREMIUM_BUY, 20000, "premium buy")).wait();
  await (await swap.addPhase(PREMIUM_SELL, 18000, "premium sell")).wait();
  await (await swap.setActivePhase(PREMIUM_BUY, 0)).wait();
  await (await swap.setActivePhase(PREMIUM_SELL, 0)).wait();
  await (await swap.addOperator(OPERATOR)).wait();
  await (await swap.setTrustedForwarder(FORWARDER)).wait();
  const one = ethers.parseUnits("1", 18);
  console.log(`   proxy ${proxy}`);
  console.log(`   owner ${await swap.owner()}`);
  console.log(`   quote buy ${ethers.formatUnits(await swap.quote(PREMIUM_BUY, one), 6)} / sell ${ethers.formatUnits(await swap.quote(PREMIUM_SELL, one), 6)} USDC`);
  console.log(`   operator authorized: ${await swap.operators(OPERATOR)}`);

  // ── 2. freeze V1 ──────────────────────────────────────────────────
  step(2, "pause V1");
  if (!(await v1.paused())) await (await v1.pause()).wait();
  console.log(`   V1 paused = ${await v1.paused()}`);

  // ── 3. fund from V1's excess ──────────────────────────────────────
  step(3, "move V1 free excess → new swap");
  const [v1Held, v1Owed] = await v1.backing();
  const excess = v1Held - v1Owed;
  const need = BigInt(snap.totals.accountedMcgp);
  const move = need + ethers.parseUnits("500", 18); // import + working inventory
  console.log(`   V1 held ${f(v1Held)}, owed ${f(v1Owed)}, excess ${f(excess)}`);
  if (move > excess) throw new Error("not enough free excess on V1");
  await (await v1.withdraw(MCGP, ENGINEERING, move)).wait();
  await (await mcgp.approve(proxy, move)).wait();
  await (await swap.fund(MCGP, move)).wait();
  console.log(`   moved ${f(move)} MCGP; new swap holds ${f(await mcgp.balanceOf(proxy))}`);

  // ── 4. migrate ────────────────────────────────────────────────────
  step(4, "import all holders");
  await (await swap.migratePremium(
    snap.holders.map((h) => h.user),
    snap.holders.map((h) => h.bought),
    snap.holders.map((h) => h.credited),
  )).wait();
  const accounted = await swap.accountedMcgp();
  console.log(`   accountedMcgp = ${accounted}`);
  console.log(`   snapshot total= ${snap.totals.accountedMcgp}`);
  if (accounted.toString() !== snap.totals.accountedMcgp) throw new Error("TOTAL MISMATCH");
  let bad = 0;
  for (const h of snap.holders) {
    const [b, c] = await Promise.all([swap.premiumBought(h.user), swap.premiumCredited(h.user)]);
    if (b.toString() !== h.bought || c.toString() !== h.credited) bad++;
  }
  const [held, owed] = await swap.backing();
  console.log(`   per-holder mismatches: ${bad}`);
  console.log(`   backing: held ${f(held)} >= owed ${f(owed)} ? ${held >= owed}`);
  if (bad || held < owed) throw new Error("MIGRATION VERIFICATION FAILED");

  // ── 5. seal ───────────────────────────────────────────────────────
  step(5, "seal migration");
  await (await swap.closeMigration()).wait();
  let sealed = false;
  try { await swap.migratePremium([ENGINEERING], [1], [0]); } catch { sealed = true; }
  console.log(`   migrationClosed = ${await swap.migrationClosed()}, further imports rejected: ${sealed}`);
  if (!sealed) throw new Error("SEAL DID NOT HOLD");

  // ── 6. prove the flows ────────────────────────────────────────────
  step(6, "exercise real flows");
  const holder = snap.holders.find((h) => BigInt(h.credited) > ethers.parseUnits("5", 18));
  const ref = ethers.keccak256(ethers.toUtf8Bytes("mcgp-bill:rehearsal"));
  await (await swap.connect(operator).debitSpendable(holder.user, ethers.parseUnits("5", 18), 0, ref)).wait();
  await (await swap.connect(operator).settleDebit(ref)).wait();
  console.log(`   (a) bills debit+settle on ${holder.user} — OK`);

  const buyer = snap.holders.find((h) => BigInt(h.bought) >= ethers.parseUnits("2", 18));
  const bSigner = await impersonate(buyer.user);
  const before = await mcgp.balanceOf(buyer.user);
  await (await swap.connect(bSigner).withdrawPremiumToWallet(ethers.parseUnits("2", 18))).wait();
  console.log(`   (b) migrated BOUGHT withdrawn: wallet ${f(before)} → ${f(await mcgp.balanceOf(buyer.user))}`);

  const credOnly = snap.holders.find((h) => BigInt(h.bought) === 0n && BigInt(h.credited) > 0n);
  const cSigner = await impersonate(credOnly.user);
  let blocked = false;
  try { await swap.connect(cSigner).withdrawPremiumToWallet(1n); } catch { blocked = true; }
  console.log(`   (c) credited-only blocked from withdrawing: ${blocked}`);
  if (!blocked) throw new Error("SPEND-ONLY INVARIANT BROKEN");

  const [fh, fo] = await swap.backing();
  console.log(`\n=== REHEARSAL PASSED ===`);
  console.log(`new swap ${proxy}`);
  console.log(`backing: held ${f(fh)}  owed ${f(fo)}  surplus ${f(fh - fo)}`);
}

main().catch((e) => {
  console.error(`\nREHEARSAL FAILED: ${e.message}`);
  process.exit(1);
});
