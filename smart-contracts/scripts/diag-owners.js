// Read-only ownership audit across every deployed TSA contract on one network.
// Reports owner / pending owner / admin-ish roles and the EIP-1967 impl slot so
// a proxy pointing somewhere unexpected is visible too.
const { ethers } = require("hardhat");

const IMPL_SLOT = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc";
const ABI = [
  "function owner() view returns (address)",
  "function pendingOwner() view returns (address)",
  "function admin() view returns (address)",
  "function paused() view returns (bool)",
];

const TARGETS = {
  sonicMainnet: [
    ["FeeManager", "0xfaF9Fd9a1a75fEF9030B1be426c5330C85f326c4"],
    ["P2PEscrowV3", "0x7dAdae104930da7B62EA185fa697fCFA7f9eC324"],
    ["ProductEscrowV2", "0x42C3c60F40c48497D023166C6088e27872D63fcb"],
    ["ServiceContactV2", "0xAb3574501d41940cD860Ecbfc535BFa6D9e13Ff3"],
    ["MCGPRewardDistributor", "0xFAFdB325262675B719F91Da7cb9E49eF4Ed4C105"],
    ["TSAForwarder", "0x569F8a58dFCCaA600d92B641F1654dCEd2dA23e4"],
    ["McgpEntitlementSwapV2", "0x5e19b7288E624921128742e6305372a0C980D6CE"],
    ["McgpEntitlementSwap (V1)", "0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52"],
    ["MCGP token", "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"],
  ],
  sonicTestnet: [
    ["FeeManager", "0xEd443D4Ec0d320975B38C4125AB9225aB2500b75"],
    ["P2PEscrowV3", "0x9c7Ba5B1714ac4dE90928032Fb73b744CD012394"],
    ["ProductEscrowV2", "0xB8477148FA3aD5c7310558bF9a645db629E6a47a"],
    ["ServiceContactV2", "0xBE6F85697aF39c32542fFE1bC6B6AEc6f88Ac2Ea"],
    ["MCGPRewardDistributor", "0xd4A5711309def2631bDD17B6a539F51A79A060E4"],
  ],
};

async function readOne(name, addr) {
  const out = { name, addr, owner: "-", extra: [] };
  const code = await ethers.provider.getCode(addr);
  if (code === "0x") { out.owner = "NO CODE"; return out; }
  const c = new ethers.Contract(addr, ABI, ethers.provider);
  try { out.owner = await c.owner(); } catch { out.owner = "(no owner())"; }
  try { const p = await c.pendingOwner(); if (p !== ethers.ZeroAddress) out.extra.push(`pendingOwner=${p}`); } catch {}
  try { if (await c.paused()) out.extra.push("PAUSED"); } catch {}
  const slot = await ethers.provider.getStorage(addr, IMPL_SLOT);
  if (slot !== ethers.ZeroHash) out.extra.push(`impl=${ethers.getAddress("0x" + slot.slice(26))}`);
  return out;
}

async function main() {
  const net = process.env.HARDHAT_NETWORK || "sonicMainnet";
  const list = TARGETS[net] || [];
  console.log(`\n=== ownership audit: ${net} (chainId ${(await ethers.provider.getNetwork()).chainId}) ===\n`);
  const rows = [];
  for (const [name, addr] of list) rows.push(await readOne(name, addr));
  const w = Math.max(...rows.map((r) => r.name.length));
  const byOwner = {};
  for (const r of rows) {
    console.log(`${r.name.padEnd(w)}  ${r.addr}  owner=${r.owner}${r.extra.length ? "  [" + r.extra.join(" ") + "]" : ""}`);
    if (r.owner.startsWith("0x")) (byOwner[r.owner] ||= []).push(r.name);
  }
  console.log("\n--- distinct owners ---");
  for (const [o, names] of Object.entries(byOwner)) console.log(`${o}  →  ${names.join(", ")}`);
}
main().catch((e) => { console.error(e.message); process.exit(1); });
