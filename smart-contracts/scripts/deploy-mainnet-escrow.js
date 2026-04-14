const { ethers } = require("hardhat");

function requiredAddress(name, fallback) {
  const value = process.env[name] || fallback;
  if (!value || !ethers.isAddress(value)) {
    throw new Error(`Missing or invalid ${name}`);
  }
  return value;
}

async function main() {
  const [deployer] = await ethers.getSigners();
  const owner = requiredAddress(
    "SYSTEM_WALLET_ADDRESS",
    "0xaF326D5D242C9A55590540f14658adDDd3586A8d"
  );
  const mcgp = requiredAddress(
    "MCGP_TOKEN_ADDRESS",
    "0x517600323e5E2938207fA2e2e915B9D80e5B2b21"
  );
  const usdt = requiredAddress(
    "MAINNET_USDT_ADDRESS",
    "0x6047828dc181963ba44974801ff68e538da5eaf9"
  );
  const usdc = requiredAddress(
    "MAINNET_USDC_ADDRESS",
    "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
  );

  const balance = await ethers.provider.getBalance(deployer.address);

  console.log("Network:        Sonic Mainnet");
  console.log("Deployer:       ", deployer.address);
  console.log("Balance:        ", ethers.formatEther(balance), "S");
  console.log("Owner:          ", owner);
  console.log("MCGP:           ", mcgp);
  console.log("USDT:           ", usdt);
  console.log("USDC:           ", usdc);

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1, exiting before deployment");
    return;
  }

  const ProductEscrow = await ethers.getContractFactory("ProductEscrow");
  const escrow = await ProductEscrow.deploy(deployer.address);
  await escrow.waitForDeployment();

  const escrowAddress = await escrow.getAddress();
  console.log("ProductEscrow:  ", escrowAddress);

  let tx = await escrow.setMcgpToken(mcgp);
  await tx.wait();
  console.log("Configured MCGP token");

  tx = await escrow.setTokenAcceptance(usdt, true);
  await tx.wait();
  console.log("Accepted USDT");

  tx = await escrow.setTokenAcceptance(usdc, true);
  await tx.wait();
  console.log("Accepted USDC");

  tx = await escrow.setTokenAcceptance(mcgp, true);
  await tx.wait();
  console.log("Accepted MCGP");

  if (owner.toLowerCase() !== deployer.address.toLowerCase()) {
    tx = await escrow.transferOwnership(owner);
    await tx.wait();
    console.log("Transferred ownership to system wallet");
  }

  console.log("\nDeployment complete");
  console.log("PRODUCT_ESCROW_ADDRESS=" + escrowAddress);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
