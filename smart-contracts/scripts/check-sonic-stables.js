// Probes the configured USDT and USDC contracts on Sonic mainnet to
// confirm they're real, deployed, ERC20-compliant tokens. Reports symbol,
// decimals, total supply. If the address isn't a contract, reports that
// so we know to remove it from the supported-tokens list.
const { ethers, network } = require("hardhat");

const ERC20_ABI = [
  "function symbol() view returns (string)",
  "function decimals() view returns (uint8)",
  "function name() view returns (string)",
  "function totalSupply() view returns (uint256)",
];

const TARGETS = [
  { label: "Sonic mainnet USDT (configured)", addr: "0x6047828dc181963ba44974801ff68e538da5eaf9" },
  { label: "Sonic mainnet USDC (configured)", addr: "0x29219dd400f2Bf60E5a23d13Be72B486D4038894" },
];

async function main() {
  if (network.name !== "sonicMainnet") {
    throw new Error(`Run with --network sonicMainnet, got ${network.name}`);
  }
  console.log(`Network: ${network.name} (chain ${network.config.chainId})`);
  console.log("");

  for (const t of TARGETS) {
    console.log(`${t.label}`);
    console.log(`  Address:  ${t.addr}`);
    const code = await ethers.provider.getCode(t.addr);
    if (code === "0x") {
      console.log("  ❌ No contract at this address.");
      console.log("");
      continue;
    }
    try {
      const c = new ethers.Contract(t.addr, ERC20_ABI, ethers.provider);
      const [sym, dec, name, supply] = await Promise.all([
        c.symbol(),
        c.decimals(),
        c.name(),
        c.totalSupply(),
      ]);
      const supplyHuman = ethers.formatUnits(supply, dec);
      console.log(`  ✅ ${name} (${sym}), ${dec} decimals, supply=${supplyHuman}`);
    } catch (e) {
      console.log(`  ⚠️  Has bytecode but ERC20 ABI calls failed: ${e.shortMessage || e.message}`);
    }
    console.log("");
  }
}

main().catch((e) => { console.error(e); process.exit(1); });
