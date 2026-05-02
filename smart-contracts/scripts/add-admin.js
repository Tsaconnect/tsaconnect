// Grant escrow-admin role to an address.
//
// Usage:
//   PROXY=0x... ADMIN=0x... npx hardhat run scripts/add-admin.js --network sonicTestnet
//
// The signer must be the contract owner (DEPLOYER_PRIVATE_KEY in .env).
// To revoke, call removeAdmin via a similar one-off script or hardhat console.

const { ethers, network } = require("hardhat");

async function main() {
  const proxy = process.env.PROXY;
  const admin = process.env.ADMIN;
  if (!proxy || !ethers.isAddress(proxy)) {
    throw new Error("PROXY env var must be a valid address");
  }
  if (!admin || !ethers.isAddress(admin)) {
    throw new Error("ADMIN env var must be a valid address");
  }

  const [signer] = await ethers.getSigners();
  const escrow = await ethers.getContractAt("ProductEscrow", proxy);

  const owner = await escrow.owner();
  console.log("Network:    ", network.name);
  console.log("Proxy:      ", proxy);
  console.log("Owner:      ", owner);
  console.log("Signer:     ", signer.address);
  console.log("Admin to add:", admin);

  if (owner.toLowerCase() !== signer.address.toLowerCase()) {
    throw new Error(
      `Signer ${signer.address} is not the owner ${owner}. addAdmin will revert.`
    );
  }

  if (await escrow.admins(admin)) {
    console.log("Already admin (mapping=true). Nothing to do.");
    return;
  }

  console.log("Sending addAdmin tx...");
  const tx = await escrow.addAdmin(admin);
  const receipt = await tx.wait();
  console.log("Confirmed in block:", receipt.blockNumber);
  console.log("Tx hash:           ", receipt.hash);

  console.log("");
  console.log("Verifying...");
  console.log("  admins(addr):  ", await escrow.admins(admin));
  console.log("  isAdmin(addr): ", await escrow.isAdmin(admin));
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
