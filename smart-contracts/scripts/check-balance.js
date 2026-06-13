// Quick balance check on whichever network --network points at.
const { ethers, network } = require("hardhat");

async function main() {
  const [signer] = await ethers.getSigners();
  if (!signer) {
    throw new Error(
      "No signer — set DEPLOYER_PRIVATE_KEY in .env so hardhat has an account."
    );
  }
  const bal = await ethers.provider.getBalance(signer.address);
  console.log(`Network:  ${network.name} (chain ${network.config.chainId})`);
  console.log(`Address:  ${signer.address}`);
  console.log(`Balance:  ${ethers.formatEther(bal)} (native)`);
}

main().catch((e) => { console.error(e); process.exit(1); });
