const { ethers, upgrades } = require("hardhat");

async function main() {
  const [deployer] = await ethers.getSigners();
  console.log("Deploying with:", deployer.address);
  console.log("Balance:", ethers.formatEther(await ethers.provider.getBalance(deployer.address)), "S");

  // 1. Deploy Mock Tokens
  const MockERC20 = await ethers.getContractFactory("contracts/mocks/MockERC20.sol:MockERC20");

  const mockUSDC = await MockERC20.deploy("Mock USDC", "USDC", 6);
  await mockUSDC.waitForDeployment();
  console.log("MockUSDC deployed:", await mockUSDC.getAddress());

  const mockMCGP = await MockERC20.deploy("Mock MCGP", "MCGP", 18);
  await mockMCGP.waitForDeployment();
  console.log("MockMCGP deployed:", await mockMCGP.getAddress());

  // 2. Deploy ProductEscrow (UUPS proxy)
  const ProductEscrow = await ethers.getContractFactory("ProductEscrow");
  const escrow = await upgrades.deployProxy(
    ProductEscrow,
    [deployer.address, []],
    { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
  );
  await escrow.waitForDeployment();
  console.log("ProductEscrow proxy deployed:", await escrow.getAddress());

  // 3. Deploy ServiceContact
  const ServiceContact = await ethers.getContractFactory("ServiceContact");
  const service = await ServiceContact.deploy(deployer.address);
  await service.waitForDeployment();
  console.log("ServiceContact deployed:", await service.getAddress());

  // 4. Configure ProductEscrow
  let tx = await escrow.setMcgpToken(await mockMCGP.getAddress());
  await tx.wait();
  console.log("ProductEscrow: MCGP token set");

  tx = await escrow.setTokenAcceptance(await mockUSDC.getAddress(), true);
  await tx.wait();
  console.log("ProductEscrow: USDC accepted");

  tx = await escrow.setTokenAcceptance(await mockMCGP.getAddress(), true);
  await tx.wait();
  console.log("ProductEscrow: MCGP accepted");

  // 5. Configure ServiceContact
  tx = await service.setTokenAcceptance(await mockUSDC.getAddress(), true);
  await tx.wait();
  console.log("ServiceContact: USDC accepted");

  // 6. Mint test tokens to deployer
  tx = await mockUSDC.mint(deployer.address, ethers.parseUnits("100000", 6));
  await tx.wait();
  console.log("Minted 100,000 USDC to deployer");

  tx = await mockMCGP.mint(deployer.address, ethers.parseUnits("1000000", 18));
  await tx.wait();
  console.log("Minted 1,000,000 MCGP to deployer");

  // Summary
  console.log("\n=== Deployment Summary ===");
  console.log("Network:        Sonic Testnet");
  console.log("Deployer:      ", deployer.address);
  console.log("MockUSDC:      ", await mockUSDC.getAddress());
  console.log("MockMCGP:      ", await mockMCGP.getAddress());
  console.log("ProductEscrow: ", await escrow.getAddress());
  console.log("ServiceContact:", await service.getAddress());
  console.log("========================\n");
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
