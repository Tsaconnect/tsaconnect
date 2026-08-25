// scripts/swapv2-admin.js
//
// Owner-key maintenance for McgpEntitlementSwapV2: authorize the backend
// operator, wire the trusted forwarder, and hand ownership to the standard
// admin key. Dry-run by default — set EXECUTE=1 to broadcast.
//
// ORDER MATTERS. transferOwnership runs LAST because every other action here
// is onlyOwner: hand the key over first and the rest become impossible without
// the new holder.
//
// ONE-STEP OWNERSHIP. V2 is OwnableUpgradeable, not Ownable2Step — the transfer
// lands immediately with no acceptance step from the recipient. A wrong address
// permanently loses `_authorizeUpgrade`, `fund`/`withdraw`, and operator
// management on the contract holding every MCGP entitlement balance. There is
// no recovery: `renounceOwnership` is disabled, but a transfer to a typo is not.
// Hence the confirmation gate below.
//
// Usage:
//   # 1. dry run — prints the plan and every precondition, sends nothing
//   NEW_OWNER=0x… OPERATOR=0x… npx hardhat run scripts/swapv2-admin.js --network sonicMainnet
//
//   # 2. execute (signer must be the CURRENT owner)
//   EXECUTE=1 CONFIRM_OWNER=0x… NEW_OWNER=0x… OPERATOR=0x… \
//     npx hardhat run scripts/swapv2-admin.js --network sonicMainnet
//
// Env:
//   OPERATOR        backend IT operator EOA to authorize (skip: unset)
//   REVOKE_OPERATOR operator to de-authorize — use this to strip the deploy key
//                   after it is no longer the owner (skip: unset)
//   FORWARDER       trusted forwarder to set (skip: unset)
//   NEW_OWNER       ownership transfer target (skip: unset)
//   PROXY           swap proxy to act on (default: live 0x0240cF8b…87e4)
//   EXECUTE=1       broadcast instead of dry-running
//   CONFIRM_OWNER   must equal NEW_OWNER exactly — the typo guard
const { ethers } = require("hardhat");

// PROXY defaults to the LIVE commissioned swap. The old 0x5e19… deploy is
// PM-owned and abandoned; pointing this script at it by default would print a
// plan nobody holds the key to execute.
const PROXY = process.env.PROXY || "0x0240cF8bF1fC2636DA3a8d2a123439E78C2287e4";

const ABI = [
  "function owner() view returns (address)",
  "function operators(address) view returns (bool)",
  "function trustedForwarder() view returns (address)",
  "function paused() view returns (bool)",
  "function maxDebitPerTx() view returns (uint256)",
  "function addOperator(address op)",
  "function removeOperator(address op)",
  "function setTrustedForwarder(address fwd)",
  "function transferOwnership(address newOwner)",
];

const execute = process.env.EXECUTE === "1";

function requireAddress(label, v) {
  if (!v) return null;
  if (!ethers.isAddress(v)) throw new Error(`${label} is not a valid address: ${v}`);
  return ethers.getAddress(v);
}

async function main() {
  const operator = requireAddress("OPERATOR", process.env.OPERATOR);
  const revokeOperator = requireAddress("REVOKE_OPERATOR", process.env.REVOKE_OPERATOR);
  const forwarder = requireAddress("FORWARDER", process.env.FORWARDER);
  const newOwner = requireAddress("NEW_OWNER", process.env.NEW_OWNER);

  const [signer] = await ethers.getSigners();
  const net = await ethers.provider.getNetwork();
  const c = new ethers.Contract(PROXY, ABI, signer);

  const currentOwner = await c.owner();
  console.log(`\n=== McgpEntitlementSwapV2 admin — ${execute ? "EXECUTE" : "DRY RUN"} ===`);
  console.log(`network:       ${net.name} (chainId ${net.chainId})`);
  console.log(`proxy:         ${PROXY}`);
  console.log(`current owner: ${currentOwner}`);
  console.log(`signer:        ${signer.address}`);
  console.log(`paused:        ${await c.paused()}`);

  const signerIsOwner = signer.address.toLowerCase() === currentOwner.toLowerCase();
  if (!signerIsOwner) {
    console.log(`\n⚠️  SIGNER IS NOT THE OWNER — every action below would revert.`);
    if (execute) throw new Error("refusing to broadcast: signer is not the owner");
  }

  // ── plan ────────────────────────────────────────────────────────────
  const plan = [];

  if (operator) {
    const already = await c.operators(operator);
    plan.push({
      label: `addOperator(${operator})`,
      skip: already,
      note: already ? "already authorized — skipping" : "authorizes the backend debit path (bills, IT)",
      send: () => c.addOperator(operator),
    });
  }

  // Revoke BEFORE the ownership transfer: removeOperator is onlyOwnerOrGuardian,
  // so doing it here means the outgoing key strips its own debit rights rather
  // than leaving that chore (and the exposure) to the new owner.
  if (revokeOperator) {
    const active = await c.operators(revokeOperator);
    plan.push({
      label: `removeOperator(${revokeOperator})`,
      skip: !active,
      note: active
        ? "revokes debitPremium/creditPremium rights over every user's entitlement"
        : "not an operator — skipping",
      send: () => c.removeOperator(revokeOperator),
    });
  }

  if (forwarder) {
    const current = await c.trustedForwarder();
    const same = current.toLowerCase() === forwarder.toLowerCase();
    plan.push({
      label: `setTrustedForwarder(${forwarder})`,
      skip: same,
      note: same ? "already set — skipping" : `enables gasless user-signed spend (was ${current})`,
      send: () => c.setTrustedForwarder(forwarder),
    });
  }

  // Ownership LAST: it revokes the signer's ability to do anything above.
  if (newOwner) {
    const same = newOwner.toLowerCase() === currentOwner.toLowerCase();
    plan.push({
      label: `transferOwnership(${newOwner})`,
      skip: same,
      note: same
        ? "already the owner — skipping"
        : "IRREVERSIBLE, one-step: upgrade + fund/withdraw + operator rights move immediately",
      send: () => c.transferOwnership(newOwner),
      irreversible: true,
    });
  }

  if (plan.length === 0) {
    console.log("\nNothing to do. Set OPERATOR / FORWARDER / NEW_OWNER.");
    return;
  }

  console.log(`\n--- plan (${plan.length} action${plan.length > 1 ? "s" : ""}, in order) ---`);
  for (const [i, p] of plan.entries()) {
    console.log(`${i + 1}. ${p.skip ? "SKIP " : "SEND "} ${p.label}`);
    console.log(`      ${p.note}`);
  }

  // ── typo guard on the irreversible action ───────────────────────────
  const transfer = plan.find((p) => p.irreversible && !p.skip);
  if (transfer && execute) {
    const confirm = requireAddress("CONFIRM_OWNER", process.env.CONFIRM_OWNER);
    if (!confirm || confirm.toLowerCase() !== newOwner.toLowerCase()) {
      throw new Error(
        "ownership transfer requires CONFIRM_OWNER to exactly match NEW_OWNER.\n" +
          `  NEW_OWNER=${newOwner}\n  CONFIRM_OWNER=${confirm || "(unset)"}`
      );
    }
    const code = await ethers.provider.getCode(newOwner);
    const bal = await ethers.provider.getBalance(newOwner);
    console.log(`\nnew owner is ${code === "0x" ? "an EOA" : "a CONTRACT"}, balance ${ethers.formatEther(bal)} S`);
    if (code === "0x" && bal === 0n) {
      throw new Error("refusing: new owner is an unfunded EOA and could not pay gas to act as owner");
    }
  }

  if (!execute) {
    console.log("\nDRY RUN — nothing was sent. Re-run with EXECUTE=1 (plus CONFIRM_OWNER for a transfer).");
    return;
  }

  // ── execute ─────────────────────────────────────────────────────────
  for (const p of plan) {
    if (p.skip) {
      console.log(`\nskipped: ${p.label}`);
      continue;
    }
    console.log(`\nsending: ${p.label}`);
    const tx = await p.send();
    console.log(`  tx: ${tx.hash}`);
    const rcpt = await tx.wait();
    console.log(`  mined in block ${rcpt.blockNumber} (status ${rcpt.status})`);
    if (rcpt.status !== 1) throw new Error(`transaction reverted: ${p.label}`);
  }

  // ── verify ──────────────────────────────────────────────────────────
  console.log("\n--- post-state ---");
  console.log(`owner:            ${await c.owner()}`);
  console.log(`trustedForwarder: ${await c.trustedForwarder()}`);
  if (operator) console.log(`operator ${operator}: ${await c.operators(operator)}`);
  if (revokeOperator) console.log(`operator ${revokeOperator}: ${await c.operators(revokeOperator)}  (want false)`);
  console.log(`maxDebitPerTx:    ${ethers.formatUnits(await c.maxDebitPerTx(), 18)} MCGP`);
}

main().catch((e) => {
  console.error(`\nFAILED: ${e.message}`);
  process.exit(1);
});

// ─────────────────────────────────────────────────────────────────────
// NOTE (2026-08-04): after the ownership consolidation, BOTH swap proxies
// are owned by 0x62bA…1b96, which engineering does not hold. Every action
// in this script is onlyOwner, so it can only be run by that key until the
// pending transfer request (docs/pm/2026-08-04-swap-ownership-request.md)
// is signed. The dry run still works from any key and is the safe way to
// preview the plan.
