// Sonic testnet deploy for the upgradeable ProductEscrow.
//
// Mirrors deploy-mainnet-escrow.js but targets the testnet network and
// defaults to the canonical testnet token addresses already used by the
// API. Use this to validate the new UUPS + multi-admin flow end-to-end
// before promoting to mainnet.
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY     - the EOA that will sign the deploy
//
// Optional env (with sensible testnet defaults):
//   TESTNET_MCGP_ADDRESS     - default 0x517600323e5E2938207fA2e2e915B9D80e5B2b21
//   TESTNET_USDC_ADDRESS     - default 0x29219dd400f2Bf60E5a23d13Be72B486D4038894
//   TESTNET_USDT_ADDRESS     - whitelisted only if set
//   INITIAL_ADMINS           - CSV; owner is implicitly admin if empty
//   OWNER_AFTER_DEPLOY       - if set, ownership transferred after token config
//   CHECK_ONLY=1             - dry-run; print plan and exit before any tx
//
// Run:
//   npx hardhat run scripts/deploy-testnet-upgradeable.js --network sonicTestnet

const { ethers, network, upgrades } = require("hardhat");

const DEFAULT_MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
const DEFAULT_USDC = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894";

function readAddress(envName, fallback) {
  const v = process.env[envName] || fallback;
  if (!v) throw new Error(`Missing required address: ${envName}`);
  if (!ethers.isAddress(v)) throw new Error(`Invalid address for ${envName}: ${v}`);
  return ethers.getAddress(v);
}

function optionalAddress(envName) {
  const v = process.env[envName];
  if (!v) return null;
  if (!ethers.isAddress(v)) throw new Error(`Invalid address for ${envName}: ${v}`);
  return ethers.getAddress(v);
}

function parseAdminsEnv() {
  const raw = process.env.INITIAL_ADMINS;
  if (!raw) return [];
  return raw
    .split(",")
    .map((a) => a.trim())
    .filter(Boolean)
    .map((a) => {
      if (!ethers.isAddress(a)) throw new Error(`Invalid admin address: ${a}`);
      return ethers.getAddress(a);
    });
}

async function main() {
  if (network.name !== "sonicTestnet") {
    throw new Error(
      `Refusing to run testnet deploy on network=${network.name}. ` +
        `Use --network sonicTestnet.`
    );
  }

  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);

  const mcgp = readAddress("TESTNET_MCGP_ADDRESS", DEFAULT_MCGP);
  const usdc = readAddress("TESTNET_USDC_ADDRESS", DEFAULT_USDC);
  const usdt = optionalAddress("TESTNET_USDT_ADDRESS");
  const initialAdmins = parseAdminsEnv();
  const ownerAfterDeploy = optionalAddress("OWNER_AFTER_DEPLOY");

  console.log("=== Sonic Testnet ProductEscrow Deploy (upgradeable) ===");
  console.log("Network:        ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer:       ", deployer.address);
  console.log("Balance:        ", ethers.formatEther(balance), "S");
  console.log(
    "Initial Owner:  ",
    deployer.address,
    "(deployer; owner is implicitly admin)"
  );
  console.log(
    "Initial Admins: ",
    initialAdmins.length === 0 ? "(none)" : initialAdmins.join(", ")
  );
  console.log("MCGP:           ", mcgp);
  console.log("USDC:           ", usdc);
  console.log("USDT:           ", usdt || "(not whitelisted)");
  if (ownerAfterDeploy) {
    console.log("Transfer to:    ", ownerAfterDeploy, "(after token config)");
  }
  console.log("");

  if (balance === 0n) {
    throw new Error("Deployer has zero balance — fund it with testnet S first.");
  }

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1 — exiting before any chain interaction.");
    return;
  }

  console.log("Deploying ProductEscrow proxy...");
  const ProductEscrow = await ethers.getContractFactory("ProductEscrow");
  const escrow = await upgrades.deployProxy(
    ProductEscrow,
    [deployer.address, initialAdmins],
    { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] }
  );
  await escrow.waitForDeployment();
  const proxyAddr = await escrow.getAddress();
  const implAddr = await upgrades.erc1967.getImplementationAddress(proxyAddr);
  console.log("Proxy:          ", proxyAddr);
  console.log("Implementation: ", implAddr);
  console.log("");

  console.log("Configuring tokens...");
  let tx = await escrow.setMcgpToken(mcgp);
  await tx.wait();
  console.log("  setMcgpToken:       ", mcgp);

  tx = await escrow.setTokenAcceptance(usdc, true);
  await tx.wait();
  console.log("  setTokenAcceptance: ", usdc, "(USDC)");

  tx = await escrow.setTokenAcceptance(mcgp, true);
  await tx.wait();
  console.log("  setTokenAcceptance: ", mcgp, "(MCGP)");

  if (usdt) {
    tx = await escrow.setTokenAcceptance(usdt, true);
    await tx.wait();
    console.log("  setTokenAcceptance: ", usdt, "(USDT)");
  }

  if (
    ownerAfterDeploy &&
    ownerAfterDeploy.toLowerCase() !== deployer.address.toLowerCase()
  ) {
    console.log("");
    console.log(`Transferring ownership to ${ownerAfterDeploy}...`);
    tx = await escrow.transferOwnership(ownerAfterDeploy);
    await tx.wait();
    console.log("  Done.");
  }

  console.log("");
  console.log("=== Deployment Summary ===");
  console.log("PRODUCT_ESCROW_ADDRESS=" + proxyAddr);
  console.log("Implementation:        " + implAddr);
  console.log(
    "Final owner:           " + (ownerAfterDeploy || deployer.address)
  );
  console.log(
    "Admins on init:        " +
      (initialAdmins.length === 0 ? "(none — owner only)" : initialAdmins.join(", "))
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
