// One-shot Sonic testnet setup for P2P:
//   1. Deploys MockERC20 "USDT" (6 decimals) and "USDC" (6 decimals).
//   2. Mints 1,000,000 of each to the deployer.
//   3. Deploys the pool-model P2PEscrow proxy whitelisting MCGP + the two
//      mock stablecoins and sets MCGP as the fee-discount token.
//   4. Prints the env block to paste into tsa-api-go/.env.
//
// MCGP is read from TESTNET_MCGP_ADDRESS (default 0x5176…2b21) — the user's
// existing testnet token; we do not redeploy it.
//
// Run:
//   npx hardhat run scripts/deploy-testnet-p2p-bundle.js --network sonicTestnet
//
// Required env: DEPLOYER_PRIVATE_KEY

const { ethers, network, upgrades } = require("hardhat");

const DEFAULT_TESTNET_MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
const MINT_AMOUNT_HUMAN = "1000000"; // 1M of each stablecoin

async function main() {
  if (network.name !== "sonicTestnet") {
    throw new Error(
      `This script targets sonicTestnet only (got '${network.name}'). Use --network sonicTestnet.`
    );
  }

  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);
  if (balance === 0n) {
    throw new Error(
      "Deployer has zero native S. Fund it from the Sonic testnet faucet first."
    );
  }

  const mcgp = ethers.getAddress(
    process.env.TESTNET_MCGP_ADDRESS || DEFAULT_TESTNET_MCGP
  );

  console.log("=== Sonic Testnet P2P Bundle ===");
  console.log("Network:  ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer: ", deployer.address);
  console.log("Balance:  ", ethers.formatEther(balance), "S");
  console.log("MCGP:     ", mcgp, "(reused, fee-discount token)");
  console.log("");

  console.log("Deploying MockERC20 USDT (6 decimals)...");
  const MockERC20 = await ethers.getContractFactory("MockERC20");
  const usdt = await MockERC20.deploy("Tether USD", "USDT", 6);
  await usdt.waitForDeployment();
  const usdtAddr = await usdt.getAddress();
  console.log("  USDT: ", usdtAddr);

  console.log("Deploying MockERC20 USDC (6 decimals)...");
  const usdc = await MockERC20.deploy("USD Coin", "USDC", 6);
  await usdc.waitForDeployment();
  const usdcAddr = await usdc.getAddress();
  console.log("  USDC: ", usdcAddr);

  const mintAmount = ethers.parseUnits(MINT_AMOUNT_HUMAN, 6);
  console.log("");
  console.log(`Minting ${MINT_AMOUNT_HUMAN} of each to deployer...`);
  let tx = await usdt.mint(deployer.address, mintAmount);
  await tx.wait();
  console.log("  USDT minted.");
  tx = await usdc.mint(deployer.address, mintAmount);
  await tx.wait();
  console.log("  USDC minted.");

  console.log("");
  console.log("Deploying P2PEscrow proxy (pool model + EIP-712 voucher)...");
  const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
  const escrow = await upgrades.deployProxy(
    P2PEscrow,
    [deployer.address, []],
    { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
  );
  await escrow.waitForDeployment();
  const proxyAddr = await escrow.getAddress();
  const implAddr = await upgrades.erc1967.getImplementationAddress(proxyAddr);
  console.log("  Proxy:          ", proxyAddr);
  console.log("  Implementation: ", implAddr);

  console.log("");
  console.log("Configuring tokens on escrow...");
  // setMcgpToken requires MCGP to be whitelisted first, so order matters.
  tx = await escrow.setTokenAcceptance(usdcAddr, true);
  await tx.wait();
  console.log("  setTokenAcceptance: USDC");
  tx = await escrow.setTokenAcceptance(mcgp, true);
  await tx.wait();
  console.log("  setTokenAcceptance: MCGP");
  tx = await escrow.setTokenAcceptance(usdtAddr, true);
  await tx.wait();
  console.log("  setTokenAcceptance: USDT");
  tx = await escrow.setMcgpToken(mcgp);
  await tx.wait();
  console.log("  setMcgpToken:       MCGP");

  console.log("");
  console.log("=== Done. Paste into tsa-api-go/.env ===");
  console.log("");
  console.log("SONIC_RPC_URL=https://rpc.testnet.soniclabs.com");
  console.log("SONIC_CHAIN_ID=14601");
  console.log("MCGP_TOKEN_ADDRESS=" + mcgp);
  console.log("USDT_TOKEN_ADDRESS=" + usdtAddr);
  console.log("USDC_TOKEN_ADDRESS=" + usdcAddr);
  console.log("P2P_ESCROW_ADDRESS=" + proxyAddr);
  console.log("");
  console.log("=== And into smart-contracts/.env (for future re-runs) ===");
  console.log("TESTNET_USDT_ADDRESS=" + usdtAddr);
  console.log("TESTNET_USDC_ADDRESS=" + usdcAddr);
  console.log("");
  console.log("Anyone can call mint(to, amount) on the mock tokens for free.");
  console.log("To faucet 1000 USDT to addr X from a script or browser:");
  console.log("  await usdt.mint('0xYourTestWallet', " + ethers.parseUnits("1000", 6).toString() + ")");
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
