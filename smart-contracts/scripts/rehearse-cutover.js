// scripts/rehearse-cutover.js
//
// Full dress rehearsal of the V1 → V2 cutover against a fork of live mainnet
// state. Impersonates the owner, so it needs no keys and can never touch the
// real chain — but every contract, balance and holder is the real one.
//
// It walks the exact production sequence and then proves the outcome by
// exercising the flows that were broken: an operator bill debit, a user
// withdrawal of migrated bought premium, and a quote.
//
// Usage:
//   FORK_RPC=https://rpc.soniclabs.com \
//     npx hardhat run scripts/rehearse-cutover.js
//
// Env:
//   SNAPSHOT   snapshot JSON to migrate (required)
//   FORK_RPC   mainnet RPC to fork (required, set on the CLI above)
const fs = require("fs");
const { ethers, upgrades, network } = require("hardhat");

const V1 = "0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52";
const V2 = "0x5e19b7288E624921128742e6305372a0C980D6CE";
const MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
const OWNER = "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96";
const OPERATOR = "0x3D64a20A645fC8d10c2843A3682E87f2053ae4E7";

const PREMIUM_BUY = 0;
const PREMIUM_SELL = 1;
const f = (v) => ethers.formatUnits(v, 18);
const step = (n, s) => console.log(`\n─── ${n}. ${s} ${"─".repeat(Math.max(0, 56 - s.length))}`);

async function impersonate(addr) {
  await network.provider.request({ method: "hardhat_impersonateAccount", params: [addr] });
  await network.provider.send("hardhat_setBalance", [addr, "0x21E19E0C9BAB2400000"]); // 10k ETH for gas
  return await ethers.getSigner(addr);
}

async function main() {
  if (!process.env.FORK_RPC) throw new Error("FORK_RPC is required (this must run on a fork)");
  const snap = JSON.parse(fs.readFileSync(process.env.SNAPSHOT, "utf8"));

  const net = await ethers.provider.getNetwork();
  console.log(`=== CUTOVER REHEARSAL on a fork (chainId ${net.chainId}) ===`);
  console.log(`snapshot: ${snap.holders.length} holders, ${f(snap.totals.accountedMcgp)} MCGP owed`);

  const owner = await impersonate(OWNER);
  const operator = await impersonate(OPERATOR);
  const mcgp = new ethers.Contract(MCGP, [
    "function balanceOf(address) view returns (uint256)",
    "function transfer(address,uint256) returns (bool)",
    "function approve(address,uint256) returns (bool)",
  ], owner);

  const Swap = await ethers.getContractFactory("McgpEntitlementSwapV2", owner);
  const V1_ABI = [
    "function pause()", "function paused() view returns (bool)",
    "function withdraw(address token, address to, uint256 amount)",
    "function accountedMcgp() view returns (uint256)",
    "function backing() view returns (uint256,uint256)",
    "function owner() view returns (address)",
  ];
  const v1 = new ethers.Contract(V1, V1_ABI, owner);

  // ── 1. upgrade V2 ─────────────────────────────────────────────────
  step(1, "upgrade V2 (adds migratePremium)");
  // Deploy the implementation and call upgradeToAndCall directly rather than
  // going through the OZ plugin: the plugin reads historical proxy state, which
  // a fork cannot serve. The on-chain effect is identical — this is exactly what
  // upgradeProxy ends up calling. Layout safety is covered separately by
  // `validateUpgrade` in the real upgrade script and by the __gap guard test.
  const impl = await Swap.deploy();
  await impl.waitForDeployment();
  const proxy = new ethers.Contract(V2, ["function upgradeToAndCall(address,bytes) payable"], owner);
  await (await proxy.upgradeToAndCall(await impl.getAddress(), "0x")).wait();
  const v2 = Swap.attach(V2).connect(owner);
  console.log(`   new impl ${await impl.getAddress()}`);
  console.log(`   upgraded; migrationClosed = ${await v2.migrationClosed()}`);

  // ── 2. price phases ───────────────────────────────────────────────
  step(2, "configure price phases");
  const one = ethers.parseUnits("1", 18);
  let quoteWorked = false;
  try { await v2.quote(PREMIUM_SELL, one); quoteWorked = true; } catch {}
  if (!quoteWorked) {
    await (await v2.addPhase(PREMIUM_BUY, 20000, "premium buy")).wait();   // $0.02
    await (await v2.addPhase(PREMIUM_SELL, 18000, "premium sell")).wait(); // $0.018
    await (await v2.setActivePhase(PREMIUM_BUY, 0)).wait();
    await (await v2.setActivePhase(PREMIUM_SELL, 0)).wait();
  }
  console.log(`   quote(PremiumBuy , 1 MCGP) = ${ethers.formatUnits(await v2.quote(PREMIUM_BUY, one), 6)} USDC`);
  console.log(`   quote(PremiumSell, 1 MCGP) = ${ethers.formatUnits(await v2.quote(PREMIUM_SELL, one), 6)} USDC`);

  // ── 3. freeze V1 ──────────────────────────────────────────────────
  step(3, "pause V1 so balances cannot move");
  if (!(await v1.paused())) await (await v1.pause()).wait();
  console.log(`   V1 paused = ${await v1.paused()}`);

  // ── 4. move MCGP from V1 excess into V2 ───────────────────────────
  step(4, "fund V2 from V1's free excess");
  const [v1Held, v1Owed] = await v1.backing();
  const excess = v1Held - v1Owed;
  const need = BigInt(snap.totals.accountedMcgp);
  const transfer = need > excess ? excess : need + ethers.parseUnits("100", 18); // import + buffer
  console.log(`   V1 held ${f(v1Held)}, owed ${f(v1Owed)}, free excess ${f(excess)}`);
  console.log(`   moving ${f(transfer)} MCGP → V2`);
  await (await v1.withdraw(MCGP, OWNER, transfer)).wait();
  await (await mcgp.approve(V2, transfer)).wait();
  await (await v2.fund(MCGP, transfer)).wait();
  console.log(`   V2 now holds ${f(await mcgp.balanceOf(V2))} MCGP`);

  // ── 5. migrate ────────────────────────────────────────────────────
  step(5, "migrate all holders");
  // V2 is NOT empty: 2.1 MCGP of smoke-test credits predate the migration
  // (4 addresses, none of them V1 holders). So the invariant is on the DELTA,
  // not on the absolute total — migration adds to whatever was already there.
  const accountedBefore = await v2.accountedMcgp();
  const importTotal = snap.holders.reduce((s, h) => s + BigInt(h.bought) + BigInt(h.credited), 0n);
  const preBalances = {};
  for (const h of snap.holders) {
    preBalances[h.user] = await Promise.all([v2.premiumBought(h.user), v2.premiumCredited(h.user)]);
  }

  await (await v2.migratePremium(
    snap.holders.map((h) => h.user),
    snap.holders.map((h) => h.bought),
    snap.holders.map((h) => h.credited),
  )).wait();

  const accounted = await v2.accountedMcgp();
  console.log(`   accountedMcgp before = ${f(accountedBefore)}  (pre-existing test credits)`);
  console.log(`   imported             = ${f(importTotal)}`);
  console.log(`   accountedMcgp after  = ${f(accounted)}`);
  if (accounted !== accountedBefore + importTotal) throw new Error("TOTAL MISMATCH on the delta");

  let bad = 0;
  for (const h of snap.holders) {
    const [b, c] = await Promise.all([v2.premiumBought(h.user), v2.premiumCredited(h.user)]);
    const [pb, pc] = preBalances[h.user];
    if (b !== pb + BigInt(h.bought) || c !== pc + BigInt(h.credited)) bad++;
  }
  const [held, owed] = await v2.backing();
  console.log(`   per-holder mismatches: ${bad}`);
  console.log(`   backing: held ${f(held)} >= owed ${f(owed)} ? ${held >= owed}`);
  if (bad || held < owed) throw new Error("MIGRATION VERIFICATION FAILED");

  // ── 6. seal ───────────────────────────────────────────────────────
  step(6, "seal migration");
  await (await v2.closeMigration()).wait();
  console.log(`   migrationClosed = ${await v2.migrationClosed()}`);
  let sealHeld = false;
  try { await v2.migratePremium([OWNER], [1], [0]); } catch { sealHeld = true; }
  console.log(`   further migration rejected: ${sealHeld}`);
  if (!sealHeld) throw new Error("SEAL DID NOT HOLD");

  // ── 7. prove the flows that were broken now work ──────────────────
  step(7, "exercise the real flows post-cutover");

  // (a) the bills path: operator debit → settle, on a REAL migrated holder
  const holder = snap.holders.find((h) => BigInt(h.credited) > ethers.parseUnits("5", 18));
  const before = await v2.premiumBalanceOf(holder.user);
  const ref = ethers.keccak256(ethers.toUtf8Bytes("mcgp-bill:rehearsal"));
  await (await v2.connect(operator).debitSpendable(holder.user, ethers.parseUnits("5", 18), 0, ref)).wait();
  console.log(`   (a) bills debit on ${holder.user}`);
  console.log(`       balance ${f(before)} → ${f(await v2.premiumBalanceOf(holder.user))}, pendingSettle ${f(await v2.pendingSettle(holder.user))}`);
  await (await v2.connect(operator).settleDebit(ref)).wait();
  console.log(`       settled; accountedMcgp ${f(await v2.accountedMcgp())}`);

  // (b) a migrated BOUGHT holder can still withdraw to wallet (self-exitable)
  const buyer = snap.holders.find((h) => BigInt(h.bought) >= ethers.parseUnits("2", 18));
  const buyerSigner = await impersonate(buyer.user);
  const walletBefore = await mcgp.balanceOf(buyer.user);
  await (await v2.connect(buyerSigner).withdrawPremiumToWallet(ethers.parseUnits("2", 18))).wait();
  console.log(`   (b) ${buyer.user} withdrew 2 MCGP of migrated BOUGHT premium`);
  console.log(`       wallet ${f(walletBefore)} → ${f(await mcgp.balanceOf(buyer.user))}`);

  // (c) credited premium must remain spend-only
  const credOnly = snap.holders.find((h) => BigInt(h.bought) === 0n && BigInt(h.credited) > 0n);
  const credSigner = await impersonate(credOnly.user);
  let blocked = false;
  try { await v2.connect(credSigner).withdrawPremiumToWallet(1n); } catch { blocked = true; }
  console.log(`   (c) credited-only holder blocked from withdrawing: ${blocked}`);
  if (!blocked) throw new Error("SPEND-ONLY INVARIANT BROKEN");

  const [fHeld, fOwed] = await v2.backing();
  console.log(`\n=== REHEARSAL PASSED ===`);
  console.log(`final V2 backing: held ${f(fHeld)}  owed ${f(fOwed)}  surplus ${f(fHeld - fOwed)}`);
}

main().catch((e) => {
  console.error(`\nREHEARSAL FAILED: ${e.message}`);
  process.exit(1);
});
