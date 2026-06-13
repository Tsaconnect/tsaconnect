// Wires a P2PEscrowV3 proxy to a FeeManager (runbook §2.D / §3.D):
//   - setFeeManager(FM)
//   - setTokenSymbol(token, symbol) for each ACCEPTED token
// Then verifies that fee resolution still yields TODAY's behavior:
//   - feeManager() == FM
//   - for each non-MCGP token: FeeManager has NO active (p2p_platform, symbol)
//     row yet, so the escrow's _snapshotFeeBps falls back to legacy feeBps
//     (the "never release fee-free" guard) → platform == feeBps, gas == 0.
//     This is the runbook's "live quote == today's rate" gate. The real PM
//     rates (per-symbol) are applied only AFTER the flag flips (§2.H.3).
//
// Owner-only txs (testnet: deployer EOA; mainnet: Safe).
//
// Run:
//   P2P_ESCROW_PROXY=0x... FEE_MANAGER_ADDRESS=0x... \
//     npx hardhat run scripts/wire-p2p-escrow-feemanager.js --network sonicTestnet
//   add CHECK_ONLY=1 to print the plan without sending txs.

const { ethers, network } = require("hardhat");

// token address -> symbol. Sonic-testnet stables + MCGP.
const TOKENS = [
  { sym: "USDT", addr: "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad" },
  { sym: "USDC", addr: "0x685CD180e603A15507f575E7A347610b83d36d41" },
  { sym: "MCGP", addr: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21" },
];

async function main() {
  const proxy = process.env.P2P_ESCROW_PROXY;
  const fm = process.env.FEE_MANAGER_ADDRESS;
  if (!proxy || !ethers.isAddress(proxy)) throw new Error("Set P2P_ESCROW_PROXY");
  if (!fm || !ethers.isAddress(fm)) throw new Error("Set FEE_MANAGER_ADDRESS");
  const checkOnly = process.env.CHECK_ONLY === "1";

  const [signer] = await ethers.getSigners();
  const escrow = await ethers.getContractAt("P2PEscrowV3", proxy);
  const feeMgr = await ethers.getContractAt("FeeManager", fm);
  const owner = await escrow.owner();

  console.log("=== Wire P2PEscrowV3 → FeeManager ===");
  console.log("Network:    ", network.name, "(chain", network.config.chainId + ")");
  console.log("Escrow:     ", proxy);
  console.log("FeeManager: ", fm);
  console.log("Owner:      ", owner, signer.address.toLowerCase() === owner.toLowerCase() ? "(signer is owner ✓)" : "(signer NOT owner ✗)");
  console.log("feeBps (legacy):", (await escrow.feeBps()).toString());
  console.log("legacySplitEnabled:", await escrow.legacySplitEnabled());
  console.log("Mode:       ", checkOnly ? "CHECK_ONLY" : "WRITE");
  console.log("");

  // Determine which tokens are accepted (only those get a symbol).
  const accepted = [];
  for (const t of TOKENS) {
    const ok = await escrow.acceptedTokens(t.addr);
    console.log(`  ${t.sym} ${t.addr} accepted=${ok}`);
    if (ok) accepted.push(t);
  }
  if (accepted.length === 0) console.log("  (no accepted tokens found — symbols would be a no-op)");

  if (checkOnly) {
    console.log("\nCHECK_ONLY=1 — would setFeeManager + setTokenSymbol for accepted tokens. Exiting.");
    return;
  }

  console.log("\nsetFeeManager...");
  await (await escrow.setFeeManager(fm)).wait();
  console.log("  feeManager() ->", await escrow.feeManager());

  for (const t of accepted) {
    await (await escrow.setTokenSymbol(t.addr, t.sym)).wait();
    console.log(`  setTokenSymbol ${t.sym} -> "${await escrow.tokenSymbol(t.addr)}"`);
  }

  // Verify TODAY's behavior is preserved: non-MCGP tokens must fall back to
  // legacy feeBps because no per-symbol p2p row is active in the FeeManager yet.
  console.log("\nVerify fee resolution == today (fallback to legacy feeBps):");
  const legacy = Number(await escrow.feeBps());
  let allGood = true;
  for (const t of accepted) {
    const [, , , pActive] = await feeMgr.rateFor("p2p_platform", t.sym);
    const [, , , gActive] = await feeMgr.rateFor("p2p", t.sym);
    const isMcgp = t.addr.toLowerCase() === "0x517600323e5E2938207fA2e2e915B9D80e5B2b21".toLowerCase();
    // Expected snapshot: platform = isMcgp?0:(pActive?seed:legacy); here pActive=false → legacy (or 0 for mcgp).
    const expectedPlatform = isMcgp ? 0 : (pActive ? "seeded" : legacy);
    const expectedGas = gActive ? "seeded" : 0;
    const ok = !pActive && !gActive; // not yet seeded per-symbol → legacy fallback (zero behavior change)
    if (!ok) allGood = false;
    console.log(`  ${t.sym}: p2p_platform.active=${pActive} p2p.active=${gActive} -> platform=${expectedPlatform} gas=${expectedGas} ${ok ? "(== today ✓)" : "(per-symbol row present — NOT pre-cutover state ✗)"}`);
  }
  console.log("");
  if (!allGood) throw new Error("A per-symbol p2p row is already active — escrow would NOT charge today's rate. Investigate before cutover.");
  console.log("Wired. feeManager set, symbols set, fee resolution still == today (legacy feeBps via fallback).");
  console.log("Next (cutover, OUT OF SCOPE here): backend env + boot guard, then setLegacySplitEnabled(false) + flag + PM rates.");
}

main().catch((e) => { console.error(e); process.exit(1); });
