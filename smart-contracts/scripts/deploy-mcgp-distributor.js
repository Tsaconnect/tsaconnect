// Deploys MCGPRewardDistributor — a standard CUMULATIVE merkle distributor for MCGP rewards.
// See docs/superpowers/specs/2026-06-07-fee-tp-overhaul-design.md (SP6, §5).
//
// NOT upgradeable by design (small, standard, auditable). Constructor args:
//   - MCGP token address (the ERC-20 paid out)
//   - owner (the admin that publishes merkle roots, pauses, rescues unallocated funds)
//
// Run:
//   MCGP_TOKEN_ADDRESS=0x... OWNER_ADDRESS=0x... \
//     npx hardhat run scripts/deploy-mcgp-distributor.js --network sonicTestnet
//
// Required env:
//   MCGP_TOKEN_ADDRESS  — the MCGP ERC-20 to distribute (must be non-zero)
// Optional env:
//   OWNER_ADDRESS       — distributor owner / root publisher (default: deployer)
//   CHECK_ONLY=1        — dry-run: validate inputs + print plan, do not deploy
//   FUND_AMOUNT         — if set (human units, 18 dp), transfer that much MCGP from
//                         the deployer into the distributor after deploy (optional)

const { ethers, network } = require("hardhat");

function isAddress(a) {
  try {
    return !!a && ethers.getAddress(a) === ethers.getAddress(a);
  } catch {
    return false;
  }
}

async function main() {
  const [deployer] = await ethers.getSigners();

  const mcgpToken = process.env.MCGP_TOKEN_ADDRESS;
  const owner = process.env.OWNER_ADDRESS || deployer.address;
  const checkOnly = process.env.CHECK_ONLY === "1";
  const fundAmountHuman = process.env.FUND_AMOUNT;

  if (!isAddress(mcgpToken)) {
    throw new Error("MCGP_TOKEN_ADDRESS is required and must be a valid address.");
  }
  if (mcgpToken === ethers.ZeroAddress) {
    throw new Error("MCGP_TOKEN_ADDRESS must be non-zero.");
  }
  if (!isAddress(owner)) {
    throw new Error("OWNER_ADDRESS must be a valid address.");
  }

  console.log("=== Deploy MCGPRewardDistributor ===");
  console.log("Network:   ", network.name, "(chain", (network.config.chainId ?? "?") + ")");
  console.log("Deployer:  ", deployer.address);
  console.log("MCGP token:", mcgpToken);
  console.log("Owner:     ", owner);
  console.log("Mode:      ", checkOnly ? "CHECK_ONLY (dry-run)" : "DEPLOY");
  console.log("");

  if (checkOnly) {
    console.log("CHECK_ONLY=1 — inputs valid, not deploying. Exiting.");
    return;
  }

  const Dist = await ethers.getContractFactory("MCGPRewardDistributor");
  const dist = await Dist.deploy(mcgpToken, owner);
  await dist.waitForDeployment();
  const addr = await dist.getAddress();

  console.log("Deployed MCGPRewardDistributor at:", addr);
  console.log("  mcgp() :", await dist.mcgp());
  console.log("  owner():", await dist.owner());

  if (fundAmountHuman) {
    const amount = ethers.parseUnits(fundAmountHuman, 18);
    const mcgp = await ethers.getContractAt("@openzeppelin/contracts/token/ERC20/IERC20.sol:IERC20", mcgpToken);
    console.log("");
    console.log(`Funding distributor with ${fundAmountHuman} MCGP from deployer...`);
    const tx = await mcgp.transfer(addr, amount);
    await tx.wait();
    console.log("  Funded. Distributor MCGP balance:", (await mcgp.balanceOf(addr)).toString());
  }

  console.log("");
  console.log("=== Next steps ===");
  console.log("  1. Treasury funds the distributor: transfer MCGP to", addr, "(or call fund(amount) after approve).");
  console.log("  2. Backend (SP5) computes cumulative claimable MCGP and builds the merkle root.");
  console.log("  3. Admin/owner calls setMerkleRoot(root) each epoch.");
  console.log("  4. Users call claim(account, cumulativeAmount, proof) (app-signed).");
  console.log("");
  console.log("Paste into tsa-api-go/.env:");
  console.log("MCGP_REWARD_DISTRIBUTOR_ADDRESS=" + addr);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
