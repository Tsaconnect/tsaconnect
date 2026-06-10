// Cutover window step: flip legacySplitEnabled=false on all configured escrows
// so the on-chain split stops (100% to system) and the off-chain v2 engine
// becomes the sole splitter. MUST run BEFORE flipping FEE_MODEL_V2_ENABLED=true
// (the backend boot guard requires legacySplitEnabled=false + feeManager set).
//
// Run:
//   P2P=0x... PRODUCT=0x... SERVICE=0x... \
//     npx hardhat run scripts/flip-splits-off-testnet.js --network sonicTestnet

const { ethers, network } = require("hardhat");

const ABI = [
  "function legacySplitEnabled() view returns (bool)",
  "function feeManager() view returns (address)",
  "function setLegacySplitEnabled(bool) external",
  "function owner() view returns (address)",
];

async function main() {
  const targets = [
    ["P2P", process.env.P2P],
    ["Product", process.env.PRODUCT],
    ["Service", process.env.SERVICE],
  ].filter(([, a]) => a);
  if (!targets.length) throw new Error("Set at least one of P2P / PRODUCT / SERVICE");

  const [signer] = await ethers.getSigners();
  console.log("=== flip legacySplitEnabled=false ===");
  console.log("Network:", network.name, "chain", network.config.chainId, "| signer:", signer.address);

  for (const [name, addr] of targets) {
    if (!ethers.isAddress(addr)) throw new Error(`${name}: bad address ${addr}`);
    const c = new ethers.Contract(addr, ABI, signer);
    const owner = await c.owner();
    if (owner.toLowerCase() !== signer.address.toLowerCase()) {
      throw new Error(`${name} ${addr}: signer is not owner (owner=${owner})`);
    }
    const fm = await c.feeManager();
    if (fm === ethers.ZeroAddress) throw new Error(`${name} ${addr}: feeManager not set — wire it first`);
    const before = await c.legacySplitEnabled();
    if (before === false) {
      console.log(`  ${name} ${addr}: already false (skip)`);
      continue;
    }
    await (await c.setLegacySplitEnabled(false)).wait();
    const after = await c.legacySplitEnabled();
    if (after !== false) throw new Error(`${name}: flip did not stick`);
    console.log(`  ${name} ${addr}: legacySplitEnabled true -> false ✓ (feeManager=${fm})`);
  }
  console.log("All set. Now flip FEE_MODEL_V2_ENABLED=true and restart — the boot guard should PASS.");
}

main().catch((e) => { console.error(e); process.exit(1); });
