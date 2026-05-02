// Sonic mainnet deploy for the upgradeable ProductEscrow.
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY     - the EOA that will sign the deploy. Becomes
//                              the contract owner unless OWNER_AFTER_DEPLOY
//                              is set, in which case ownership is handed off
//                              after token config.
//   MAINNET_MCGP_ADDRESS     - canonical MCGP contract on Sonic mainnet
//   MAINNET_USDC_ADDRESS     - canonical USDC.e contract on Sonic mainnet
//
// Optional env:
//   MAINNET_USDT_ADDRESS     - canonical USDT contract; whitelisted if set
//   INITIAL_ADMINS           - CSV of addresses seeded into the admins
//                              mapping at initialize time. Owner is always
//                              implicitly admin, so this is only needed if
//                              you want a separate hot-wallet admin from
//                              day one.
//   OWNER_AFTER_DEPLOY       - if set, ownership is transferred to this
//                              address AFTER token config. Use this when
//                              the long-term owner is a multisig you want
//                              to take over post-deploy.
//   SONICSCAN_API_KEY        - if set, the implementation contract is
//                              verified on SonicScan automatically. Proxy
//                              verification typically requires a manual
//                              "Is this a proxy?" click in the explorer.
//   CHECK_ONLY=1             - print the planned config and exit before
//                              touching the chain. Use this to verify env
//                              before paying for gas.
//
// Run:
//   npm run deploy:mainnet:escrow

const { ethers, network, upgrades, run } = require("hardhat");

function requiredAddress(name) {
  const v = process.env[name];
  if (!v) throw new Error(`Missing required env ${name}`);
  if (!ethers.isAddress(v)) throw new Error(`Invalid address for ${name}: ${v}`);
  return ethers.getAddress(v);
}

function optionalAddress(name) {
  const v = process.env[name];
  if (!v) return null;
  if (!ethers.isAddress(v)) throw new Error(`Invalid address for ${name}: ${v}`);
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
  if (network.name !== "sonicMainnet") {
    throw new Error(
      `Refusing to run mainnet deploy on network=${network.name}. ` +
        `Use --network sonicMainnet.`
    );
  }

  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);

  const mcgp = requiredAddress("MAINNET_MCGP_ADDRESS");
  const usdc = requiredAddress("MAINNET_USDC_ADDRESS");
  const usdt = optionalAddress("MAINNET_USDT_ADDRESS");
  const initialAdmins = parseAdminsEnv();
  const ownerAfterDeploy = optionalAddress("OWNER_AFTER_DEPLOY");

  console.log("=== Sonic Mainnet ProductEscrow Deploy ===");
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
    throw new Error("Deployer has zero balance — fund it with S before deploying.");
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
    console.log("  Done. Verify with: escrow.owner() === " + ownerAfterDeploy);
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

  if (process.env.SONICSCAN_API_KEY) {
    console.log("");
    console.log("Verifying implementation on SonicScan...");
    try {
      await run("verify:verify", {
        address: implAddr,
        constructorArguments: [],
      });
      console.log("  Implementation verified.");
    } catch (e) {
      console.warn("  Verification failed:", e.message);
      console.warn(
        "  Retry manually: npx hardhat verify --network sonicMainnet " + implAddr
      );
    }
    console.log(
      "Note: proxy verification on SonicScan usually requires clicking " +
        '"Is this a proxy?" on the contract page.'
    );
  } else {
    console.log("");
    console.log(
      "(SONICSCAN_API_KEY not set — skipping verification. Run manually:"
    );
    console.log(
      "  npx hardhat verify --network sonicMainnet " + implAddr + ")"
    );
  }
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
