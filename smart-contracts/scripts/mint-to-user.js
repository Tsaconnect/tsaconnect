const { ethers } = require("hardhat");

async function main() {
  const userAddress = process.env.USER_ADDRESS;
  if (!userAddress) {
    console.error("Usage: USER_ADDRESS=0x... npx hardhat run scripts/mint-to-user.js --network sonicTestnet");
    process.exit(1);
  }

  const MockERC20 = await ethers.getContractFactory("contracts/mocks/MockERC20.sol:MockERC20");

  // New deployed mock token addresses
  const USDC_ADDRESS = "0xEdD690BA962B9D484e248a14Bc3Cde2acB93C654";
  const MCGP_ADDRESS = "0xb5B073BF578fE24311ED50e540D9d632f1679261";

  const usdc = MockERC20.attach(USDC_ADDRESS);
  const mcgp = MockERC20.attach(MCGP_ADDRESS);

  console.log("Minting to:", userAddress);

  let tx = await usdc.mint(userAddress, ethers.parseUnits("10000", 6));
  await tx.wait();
  console.log("Minted 10,000 USDC");

  tx = await mcgp.mint(userAddress, ethers.parseUnits("100000", 18));
  await tx.wait();
  console.log("Minted 100,000 MCGP");

  console.log("Done!");
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
