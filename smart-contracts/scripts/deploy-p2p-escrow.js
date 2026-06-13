// Deploy the upgradeable P2PEscrow contract.
//
// Works on both Sonic testnet (--network sonicTestnet) and Sonic mainnet
// (--network sonicMainnet). Mirrors deploy-testnet-upgradeable.js / deploy-
// mainnet-escrow.js patterns.
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY     - the EOA that will sign the deploy
//
// Network-keyed env (one set per target):
//   sonicTestnet:
//     TESTNET_MCGP_ADDRESS   - default 0x517600323e5E2938207fA2e2e915B9D80e5B2b21
//     TESTNET_USDC_ADDRESS   - default 0x29219dd400f2Bf60E5a23d13Be72B486D4038894
//     TESTNET_USDT_ADDRESS   - whitelisted only if set
//   sonicMainnet:
//     MAINNET_MCGP_ADDRESS   - required
//     MAINNET_USDC_ADDRESS   - required
//     MAINNET_USDT_ADDRESS   - whitelisted only if set
//
// Shared optional env:
//   INITIAL_ADMINS           - CSV of admin addresses (operator EOA goes here)
//   SYSTEM_WALLET            - if set, fees go here; else owner()
//   OWNER_AFTER_DEPLOY       - if set, ownership transferred after token config
//   CHECK_ONLY=1             - dry-run; print plan and exit before any tx
//
// Run:
//   npx hardhat run scripts/deploy-p2p-escrow.js --network sonicTestnet
//   npx hardhat run scripts/deploy-p2p-escrow.js --network sonicMainnet

const { ethers, network, upgrades } = require("hardhat");

const DEFAULT_TESTNET_MCGP = "0x517600323e5E2938207fA2e2e915B9D80e5B2b21";
const DEFAULT_TESTNET_USDC = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894";

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

function resolveTokens() {
  if (network.name === "sonicTestnet") {
    return {
      mcgp: readAddress("TESTNET_MCGP_ADDRESS", DEFAULT_TESTNET_MCGP),
      usdc: readAddress("TESTNET_USDC_ADDRESS", DEFAULT_TESTNET_USDC),
      usdt: optionalAddress("TESTNET_USDT_ADDRESS"),
    };
  }
  if (network.name === "sonicMainnet") {
    return {
      mcgp: readAddress("MAINNET_MCGP_ADDRESS"),
      usdc: readAddress("MAINNET_USDC_ADDRESS"),
      usdt: optionalAddress("MAINNET_USDT_ADDRESS"),
    };
  }
  throw new Error(
    `Unsupported network=${network.name}. Use --network sonicTestnet or sonicMainnet.`
  );
}

async function main() {
  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);

  const { mcgp, usdc, usdt } = resolveTokens();
  const initialAdmins = parseAdminsEnv();
  const systemWallet = optionalAddress("SYSTEM_WALLET");
  const ownerAfterDeploy = optionalAddress("OWNER_AFTER_DEPLOY");

  console.log("=== P2PEscrow Deploy (upgradeable) ===");
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
  console.log("System Wallet:  ", systemWallet || "(unset — fees route to owner)");
  console.log("MCGP:           ", mcgp);
  console.log("USDC:           ", usdc);
  console.log("USDT:           ", usdt || "(not whitelisted)");
  if (ownerAfterDeploy) {
    console.log("Transfer to:    ", ownerAfterDeploy, "(after token config)");
  }
  console.log("");

  if (balance === 0n) {
    throw new Error("Deployer has zero balance — fund it with native S first.");
  }

  if (process.env.CHECK_ONLY === "1") {
    console.log("CHECK_ONLY=1 — exiting before any chain interaction.");
    return;
  }

  console.log("Deploying P2PEscrow proxy...");
  const P2PEscrow = await ethers.getContractFactory("P2PEscrow");
  const escrow = await upgrades.deployProxy(
    P2PEscrow,
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
  // setMcgpToken now requires the MCGP address to already be whitelisted, so
  // whitelist all tokens first, then set MCGP as the fee-discount token.
  let tx = await escrow.setTokenAcceptance(usdc, true);
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

  tx = await escrow.setMcgpToken(mcgp);
  await tx.wait();
  console.log("  setMcgpToken:       ", mcgp);

  if (systemWallet) {
    tx = await escrow.setSystemWallet(systemWallet);
    await tx.wait();
    console.log("  setSystemWallet:    ", systemWallet);
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
  console.log("P2P_ESCROW_ADDRESS=" + proxyAddr);
  console.log("Implementation:    " + implAddr);
  console.log(
    "Final owner:       " + (ownerAfterDeploy || deployer.address)
  );
  console.log(
    "Admins on init:    " +
      (initialAdmins.length === 0 ? "(none — owner only)" : initialAdmins.join(", "))
  );
  console.log(
    "System wallet:     " +
      (systemWallet || `(unset — fees go to ${ownerAfterDeploy || deployer.address})`)
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
