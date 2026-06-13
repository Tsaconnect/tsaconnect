// One-shot BSC testnet setup for P2P (P2PEscrowV2, MCGP-less variant):
//   1. Deploys MockERC20 USDT and USDC (both 18 decimals — BSC convention).
//   2. Mints 1,000,000 of each to the deployer.
//   3. Deploys the P2PEscrowV2 proxy (pool model + EIP-712 voucher).
//   4. Sets the system wallet and whitelists both stables.
//   5. Skips setMcgpToken — V2 permits mcgpToken == address(0), which disables
//      the fee-discount path (MCGP not on BSC testnet).
//   6. Prints the env block to paste into tsa-api-go/.env.
//
// Mirrors deploy-bsc-testnet-p2p-bundle.js (V1) but targets P2PEscrowV2, the
// same contract live on Sonic testnet (deployed 2026-06-02).
//
// Run:
//   npx hardhat run scripts/deploy-bsc-testnet-p2p-v2-bundle.js --network bscTestnet
//
// Required env: DEPLOYER_PRIVATE_KEY (with tBNB — grab from
// https://www.bnbchain.org/en/testnet-faucet)
// Optional env (default to deployer):
//   OWNER_ADDRESS          — contract owner (default: deployer)
//   SYSTEM_WALLET_ADDRESS  — fee/system wallet (default: deployer)
//   ADMIN_ADDRESSES        — comma-separated initial admins (default: none)

const { ethers, network, upgrades } = require("hardhat");

const MINT_AMOUNT_HUMAN = "1000000"; // 1M of each stable
const STABLE_DECIMALS = 18; // BSC convention — real BSC USDT/USDC are both 18

async function main() {
  if (network.name !== "bscTestnet") {
    throw new Error(
      `This script targets bscTestnet only (got '${network.name}'). Use --network bscTestnet.`
    );
  }

  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);
  if (balance === 0n) {
    throw new Error(
      `Deployer ${deployer.address} has zero tBNB. Faucet at ` +
        "https://www.bnbchain.org/en/testnet-faucet"
    );
  }

  const owner = process.env.OWNER_ADDRESS || deployer.address;
  const systemWallet = process.env.SYSTEM_WALLET_ADDRESS || deployer.address;
  const admins = (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean);

  console.log("=== BSC Testnet P2P Bundle — P2PEscrowV2 (MCGP-less) ===");
  console.log("Network:      ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer:     ", deployer.address);
  console.log("Balance:      ", ethers.formatEther(balance), "tBNB");
  console.log("Owner:        ", owner);
  console.log("System wallet:", systemWallet);
  console.log("Admins:       ", admins.length ? admins.join(", ") : "(none)");
  console.log("");

  const MockERC20 = await ethers.getContractFactory("MockERC20");

  console.log("Deploying MockERC20 USDT (18 decimals)...");
  const usdt = await MockERC20.deploy("Tether USD", "USDT", STABLE_DECIMALS);
  await usdt.waitForDeployment();
  const usdtAddr = await usdt.getAddress();
  console.log("  USDT: ", usdtAddr);

  console.log("Deploying MockERC20 USDC (18 decimals)...");
  const usdc = await MockERC20.deploy("USD Coin", "USDC", STABLE_DECIMALS);
  await usdc.waitForDeployment();
  const usdcAddr = await usdc.getAddress();
  console.log("  USDC: ", usdcAddr);

  const mintAmount = ethers.parseUnits(MINT_AMOUNT_HUMAN, STABLE_DECIMALS);
  console.log("");
  console.log(`Minting ${MINT_AMOUNT_HUMAN} of each to deployer...`);
  let tx = await usdt.mint(deployer.address, mintAmount);
  await tx.wait();
  console.log("  USDT minted.");
  tx = await usdc.mint(deployer.address, mintAmount);
  await tx.wait();
  console.log("  USDC minted.");

  console.log("");
  console.log("Deploying P2PEscrowV2 proxy (pool model + EIP-712 voucher)...");
  const P2PEscrowV2 = await ethers.getContractFactory("P2PEscrowV2");
  const escrow = await upgrades.deployProxy(
    P2PEscrowV2,
    [owner, admins],
    { kind: "uups" }
  );
  await escrow.waitForDeployment();
  const proxyAddr = await escrow.getAddress();
  const implAddr = await upgrades.erc1967.getImplementationAddress(proxyAddr);
  console.log("  Proxy:          ", proxyAddr);
  console.log("  Implementation: ", implAddr);

  console.log("");
  console.log("Configuring escrow...");
  tx = await escrow.setSystemWallet(systemWallet);
  await tx.wait();
  console.log("  setSystemWallet:", systemWallet);
  tx = await escrow.setTokenAcceptance(usdtAddr, true);
  await tx.wait();
  console.log("  setTokenAcceptance: USDT");
  tx = await escrow.setTokenAcceptance(usdcAddr, true);
  await tx.wait();
  console.log("  setTokenAcceptance: USDC");
  console.log("  (setMcgpToken skipped — mcgpToken stays address(0), fee discount disabled)");

  console.log("");
  console.log("=== Done. Paste into tsa-api-go/.env ===");
  console.log("");
  console.log("# Backend env block for BSC testnet P2P (V2)");
  console.log("SONIC_RPC_URL=" + (process.env.BSC_TESTNET_RPC || "https://data-seed-prebsc-1-s1.binance.org:8545"));
  console.log("SONIC_CHAIN_ID=97");
  console.log("# MCGP_TOKEN_ADDRESS unset — no MCGP on BSC testnet, fee discount disabled");
  console.log("USDT_TOKEN_ADDRESS=" + usdtAddr);
  console.log("USDC_TOKEN_ADDRESS=" + usdcAddr);
  console.log("P2P_ESCROW_ADDRESS=" + proxyAddr);
  console.log("");
  console.log("# Comment out / unset SONIC_NETWORK so the per-field overrides above win,");
  console.log("# OR (better) generalize the config switch to support BSC — pending.");
  console.log("");
  console.log("Notes:");
  console.log("  • USDT/USDC are 18 decimals on BSC — confirm frontend TOKEN_DECIMALS");
  console.log("    handles per-chain decimals (Sonic USDT is 6, BSC is 18).");
  console.log("  • Both mocks have public mint(to, amount) — anyone can self-faucet:");
  console.log("    await usdt.mint('0xYourTestWallet', " + ethers.parseUnits("1000", STABLE_DECIMALS).toString() + ")");
  console.log("  • Repoint the migration sub-project's preset p2pEscrow to", proxyAddr);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
