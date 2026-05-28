const { ethers, upgrades } = require("hardhat");

async function main() {
  const owner = process.env.OWNER_ADDRESS;
  const admins = (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean);
  const systemWallet = process.env.SYSTEM_WALLET_ADDRESS;
  if (!owner || !systemWallet) throw new Error("Set OWNER_ADDRESS and SYSTEM_WALLET_ADDRESS");

  const Factory = await ethers.getContractFactory("P2PEscrowV2");
  const escrow = await upgrades.deployProxy(Factory, [owner, admins], { kind: "uups" });
  await escrow.waitForDeployment();
  const addr = await escrow.getAddress();
  console.log("P2PEscrowV2 proxy:", addr);

  if (process.env.CHECK_ONLY === "1") { console.log("CHECK_ONLY — skipping config txs"); return; }
  await (await escrow.setSystemWallet(systemWallet)).wait();
  for (const t of (process.env.ACCEPTED_TOKENS || "").split(",").filter(Boolean)) {
    await (await escrow.setTokenAcceptance(t, true)).wait();
    console.log("accepted:", t);
  }
  if (process.env.MCGP_TOKEN_ADDRESS) await (await escrow.setMcgpToken(process.env.MCGP_TOKEN_ADDRESS)).wait();
  console.log("Done. Repoint sonicPresets[*].p2pEscrow to", addr, "in the migration sub-project.");
}

main().catch((e) => { console.error(e); process.exit(1); });
