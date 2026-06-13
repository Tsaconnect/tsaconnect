// Reports which of a list of addresses actually have bytecode on the
// active network. "no contract" means the address is a plain EOA / has
// never had a contract deployed at it.
const { ethers, network } = require("hardhat");

const ADDRS = [
  { label: "P2PEscrow proxy (testnet deploy)", addr: "0xadB543F2AD95ad38243d36E46530fE1c80c70Fe5" },
  { label: "P2PEscrow impl   (testnet deploy)", addr: "0x057B7124a98CC86F8C072FB86c8117E76a6da7d1" },
  { label: "MockUSDT          (testnet deploy)", addr: "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad" },
  { label: "MockUSDC          (testnet deploy)", addr: "0x685CD180e603A15507f575E7A347610b83d36d41" },
  { label: "MCGP token (default both networks)", addr: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21" },
];

async function main() {
  console.log(`Network: ${network.name} (chain ${network.config.chainId})\n`);
  for (const { label, addr } of ADDRS) {
    const code = await ethers.provider.getCode(addr);
    const isContract = code !== "0x";
    const marker = isContract ? "✅ contract" : "❌ NO CODE  ";
    console.log(`${marker}  ${addr}  ${label}`);
  }
}

main().catch((e) => { console.error(e); process.exit(1); });
