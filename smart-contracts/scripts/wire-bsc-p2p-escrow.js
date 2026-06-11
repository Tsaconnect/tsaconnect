// Wire the BSC P2P escrow (V3) to the BSC FeeManager: setFeeManager +
// setTokenSymbol for accepted BSC testnet tokens. legacySplitEnabled is flipped
// separately by flip-splits-off-testnet.js. Run AFTER the V3 upgrade.
//
// Run:
//   P2P_ESCROW_PROXY=0x... FEE_MANAGER_ADDRESS=0x... \
//     npx hardhat run scripts/wire-bsc-p2p-escrow.js --network bscTestnet

const { ethers, network } = require("hardhat");

// BSC testnet stables (from tsa-api-go/.env BSC_TESTNET_*).
const TOKENS = [
  { sym: "USDT", addr: process.env.BSC_TESTNET_USDT_ADDRESS || "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7" },
  { sym: "USDC", addr: process.env.BSC_TESTNET_USDC_ADDRESS || "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d" },
];

async function main() {
  const proxy = process.env.P2P_ESCROW_PROXY;
  const fm = process.env.FEE_MANAGER_ADDRESS;
  if (!proxy || !ethers.isAddress(proxy)) throw new Error("Set P2P_ESCROW_PROXY");
  if (!fm || !ethers.isAddress(fm)) throw new Error("Set FEE_MANAGER_ADDRESS");

  const [signer] = await ethers.getSigners();
  const c = await ethers.getContractAt("P2PEscrowV3", proxy);
  console.log("=== wire BSC P2P escrow ===", network.name, "chain", network.config.chainId);
  console.log("proxy:", proxy, "| fm:", fm, "| signer:", signer.address);

  await (await c.setFeeManager(fm)).wait();
  console.log("setFeeManager OK");

  for (const t of TOKENS) {
    let accepted = false;
    try { accepted = await c.acceptedTokens(t.addr); } catch (_) {}
    if (!accepted) { console.log(`  ${t.sym} ${t.addr}: not accepted — skip`); continue; }
    await (await c.setTokenSymbol(t.addr, t.sym)).wait();
    console.log(`  setTokenSymbol ${t.addr} -> ${t.sym} OK`);
  }

  const wired = await c.feeManager();
  console.log("VERIFY feeManager:", wired, wired.toLowerCase() === fm.toLowerCase() ? "✓" : "MISMATCH");
}

main().catch((e) => { console.error(e); process.exit(1); });
