// Deploys BSC test stablecoins for P2P testnet: MockERC20 "USDT" + "USDC" at
// 18 decimals — matching real BSC (Binance-Peg stables are 18) AND the backend's
// per-(chain,token) decimals (tokenDecimalsFor returns 18 for BSC). Do NOT use
// 6 decimals here (that's the Sonic convention) or amounts will be off by 1e12.
//
// Mints a large balance to the deployer (and to MINT_TO, if set, e.g. a tester
// wallet). Feed the printed addresses into the escrow deploy's ACCEPTED_TOKENS
// and the backend's BSC_USDT_TOKEN_ADDRESS / BSC_USDC_TOKEN_ADDRESS.
//
//   npx hardhat run scripts/deploy-bsc-test-tokens.js --network bscTestnet
//   # local validation (no tBNB): --network hardhat  or  --network localhost
const { ethers } = require("hardhat");

async function main() {
  const [deployer] = await ethers.getSigners();
  console.log("Deployer:", deployer.address);

  const MockERC20 = await ethers.getContractFactory("MockERC20");

  console.log("Deploying MockERC20 USDT (18 decimals)...");
  const usdt = await MockERC20.deploy("Tether USD", "USDT", 18);
  await usdt.waitForDeployment();
  const usdtAddr = await usdt.getAddress();

  console.log("Deploying MockERC20 USDC (18 decimals)...");
  const usdc = await MockERC20.deploy("USD Coin", "USDC", 18);
  await usdc.waitForDeployment();
  const usdcAddr = await usdc.getAddress();

  const human = process.env.MINT_AMOUNT || "1000000";
  const mintAmt = ethers.parseUnits(human, 18);
  const to = process.env.MINT_TO || deployer.address;
  console.log(`Minting ${human} of each to ${to}...`);
  await (await usdt.mint(to, mintAmt)).wait();
  await (await usdc.mint(to, mintAmt)).wait();

  console.log("\n=== BSC test tokens (18 decimals) ===");
  console.log("  USDT:", usdtAddr);
  console.log("  USDC:", usdcAddr);
  console.log("\n--- escrow deploy ---");
  console.log(`  ACCEPTED_TOKENS=${usdtAddr},${usdcAddr}`);
  console.log("--- backend env ---");
  console.log(`  BSC_USDT_TOKEN_ADDRESS=${usdtAddr}`);
  console.log(`  BSC_USDC_TOKEN_ADDRESS=${usdcAddr}`);
}

main().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
