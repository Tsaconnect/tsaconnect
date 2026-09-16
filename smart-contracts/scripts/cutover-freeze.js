// scripts/cutover-freeze.js
//
// Step 1 of the V1 → new-swap cutover: freeze V1 and move its free excess MCGP
// into the new swap so the import has backing. Dry-run by default.
//
// Pausing V1 is USER-VISIBLE DOWNTIME — buy, sell, spend and withdraw all
// revert until the backend is repointed at the new swap. Run this at the start
// of a maintenance window, not casually.
//
// Pause comes FIRST so the snapshot taken next cannot go stale: a balance that
// moves between snapshot and import would be silently mis-migrated. Owner
// `withdraw` is not pause-gated, so funding still works afterwards.
//
// Usage:
//   TARGET=0x0240… npx hardhat run scripts/cutover-freeze.js --network sonicMainnet
//   EXECUTE=1 TARGET=0x0240… npx hardhat run scripts/cutover-freeze.js --network sonicMainnet
//
// Env:
//   TARGET     new swap proxy to fund (required)
//   BUFFER     extra MCGP beyond the imported total, as working inventory for
//              new IT buys (default 500)
//   EXECUTE=1  broadcast
const { ethers } = require("hardhat");

const V1 = process.env.V1_PROXY || "0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52";
const MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";

const execute = process.env.EXECUTE === "1";
const f = (v) => ethers.formatUnits(v, 18);

async function main() {
  const target = process.env.TARGET;
  if (!target || !ethers.isAddress(target)) throw new Error("TARGET=<new swap proxy> is required");
  const buffer = ethers.parseUnits(process.env.BUFFER || "500", 18);

  const [signer] = await ethers.getSigners();
  const net = await ethers.provider.getNetwork();
  const v1 = new ethers.Contract(V1, [
    "function owner() view returns (address)",
    "function paused() view returns (bool)",
    "function pause()",
    "function backing() view returns (uint256 held, uint256 owed)",
    "function accountedMcgp() view returns (uint256)",
    "function withdraw(address,address,uint256)",
  ], signer);
  const swap = new ethers.Contract(target, [
    "function owner() view returns (address)",
    "function migrationClosed() view returns (bool)",
    "function accountedMcgp() view returns (uint256)",
    "function fund(address,uint256)",
    "function quote(uint8,uint256) view returns (uint256)",
    "function operators(address) view returns (bool)",
  ], signer);
  const mcgp = new ethers.Contract(MCGP, [
    "function balanceOf(address) view returns (uint256)",
    "function approve(address,uint256) returns (bool)",
  ], signer);

  console.log(`\n=== cutover step 1: freeze V1 + fund the new swap — ${execute ? "EXECUTE" : "DRY RUN"} ===`);
  console.log(`network: ${net.name} (chainId ${net.chainId})`);
  console.log(`signer : ${signer.address}`);
  console.log(`V1     : ${V1}`);
  console.log(`target : ${target}`);

  // ── preconditions ─────────────────────────────────────────────────
  const v1Owner = await v1.owner();
  const swapOwner = await swap.owner();
  console.log(`\nV1 owner    : ${v1Owner}`);
  console.log(`target owner: ${swapOwner}`);
  for (const [label, o] of [["V1", v1Owner], ["target", swapOwner]]) {
    if (o.toLowerCase() !== signer.address.toLowerCase()) {
      console.log(`⚠️  signer does not own ${label} — its actions would revert`);
      if (execute) throw new Error(`refusing to broadcast: signer does not own ${label}`);
    }
  }
  if (await swap.migrationClosed()) throw new Error("target migration is SEALED — it cannot receive the import");
  const one = ethers.parseUnits("1", 18);
  try {
    console.log(`target quotes: buy ${ethers.formatUnits(await swap.quote(0, one), 6)} / sell ${ethers.formatUnits(await swap.quote(1, one), 6)} USDC per MCGP`);
  } catch {
    throw new Error("target cannot quote — configure price phases before cutting over");
  }

  const [held, owed] = await v1.backing();
  const excess = held > owed ? held - owed : 0n;
  const move = owed + buffer;
  console.log(`\nV1 held  : ${f(held)}`);
  console.log(`V1 owed  : ${f(owed)}   <- this is what will be imported`);
  console.log(`V1 excess: ${f(excess)}   <- withdrawable`);
  console.log(`to move  : ${f(move)}   (owed + ${f(buffer)} buffer)`);
  if (move > excess) {
    throw new Error(`V1's free excess (${f(excess)}) is less than needed (${f(move)}). Lower BUFFER.`);
  }

  console.log(`\n--- plan ---`);
  console.log(`1. ${(await v1.paused()) ? "SKIP  V1 already paused" : "pause V1  (USER DOWNTIME BEGINS)"}`);
  console.log(`2. withdraw ${f(move)} MCGP from V1 → ${signer.address}`);
  console.log(`3. approve + fund ${f(move)} MCGP → ${target}`);

  if (!execute) {
    console.log(`\nDRY RUN — nothing sent. Re-run with EXECUTE=1.`);
    console.log(`NEXT after executing: re-run v1-snapshot.js (it must be taken while V1 is paused).`);
    return;
  }

  if (!(await v1.paused())) {
    console.log(`\npausing V1…`);
    const tx = await v1.pause();
    console.log(`  tx: ${tx.hash}`);
    await tx.wait();
  }
  console.log(`  V1 paused = ${await v1.paused()}`);

  console.log(`\nwithdrawing ${f(move)} MCGP from V1…`);
  let tx = await v1.withdraw(MCGP, signer.address, move);
  console.log(`  tx: ${tx.hash}`);
  await tx.wait();

  console.log(`\nfunding the new swap…`);
  tx = await mcgp.approve(target, move);
  await tx.wait();
  tx = await swap.fund(MCGP, move);
  console.log(`  tx: ${tx.hash}`);
  await tx.wait();

  console.log(`\n--- post-state ---`);
  console.log(`V1 paused        : ${await v1.paused()}`);
  console.log(`V1 accountedMcgp : ${f(await v1.accountedMcgp())}  (unchanged — user balances intact)`);
  console.log(`target MCGP held : ${f(await mcgp.balanceOf(target))}`);
  console.log(`target owes      : ${f(await swap.accountedMcgp())}`);
  console.log(`\nNEXT: npx hardhat run scripts/v1-snapshot.js --network ${net.name}`);
  console.log(`      (must be taken NOW, while V1 is paused)`);
}

main().catch((e) => {
  console.error(`\nFAILED: ${e.message}`);
  process.exit(1);
});
