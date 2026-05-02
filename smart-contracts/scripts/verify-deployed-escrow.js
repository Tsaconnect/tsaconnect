// Read-only sanity checks against a deployed ProductEscrow proxy.
// Usage: PROXY=0x... npx hardhat run scripts/verify-deployed-escrow.js --network sonicTestnet
const { ethers, network } = require("hardhat");

async function main() {
  const proxy = process.env.PROXY;
  if (!proxy || !ethers.isAddress(proxy)) {
    throw new Error("PROXY env var must be set to the proxy address");
  }
  const escrow = await ethers.getContractAt("ProductEscrow", proxy);
  const [signer] = await ethers.getSigners();

  const mcgp =
    process.env.MCGP || "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
  const usdc =
    process.env.USDC || "0x29219dd400f2Bf60E5a23d13Be72B486D4038894";

  console.log("Network:", network.name);
  console.log("Proxy:  ", proxy);
  console.log("");
  console.log("owner():             ", await escrow.owner());
  console.log("isAdmin(deployer):   ", await escrow.isAdmin(signer.address));
  console.log("isAdmin(zeroAddr):   ", await escrow.isAdmin(ethers.ZeroAddress));
  console.log("mcgpToken():         ", await escrow.mcgpToken());
  console.log("acceptedTokens(MCGP):", await escrow.acceptedTokens(mcgp));
  console.log("acceptedTokens(USDC):", await escrow.acceptedTokens(usdc));
  console.log("paused():            ", await escrow.paused());
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
