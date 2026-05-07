// Sonic mainnet deploy for the ServiceContact contract (UUPS upgradeable).
//
// The contract is upgradeable behind a UUPS proxy, mirroring ProductEscrow's
// pattern: future fee/split logic changes ship via `upgrades.upgradeProxy`
// without redeploying or migrating state. Owner is the only address that
// can authorize an upgrade.
//
// Required env (in smart-contracts/.env):
//   DEPLOYER_PRIVATE_KEY     - the EOA that signs the deploy. Becomes the
//                              contract owner unless OWNER_AFTER_DEPLOY is
//                              set, in which case ownership is transferred
//                              after token config.
//   MAINNET_USDC_ADDRESS     - canonical USDC.e contract on Sonic mainnet
//                              (must be a 6-decimal stablecoin if using the
//                              default $0.10 fee).
//
// Optional env:
//   MAINNET_USDT_ADDRESS     - canonical USDT contract; whitelisted at
//                              the same fee if set.
//   FEE_USDC                 - per-token fee in token-units. Default 100000
//                              ($0.10 for 6-decimal stablecoins).
//   FEE_USDT                 - per-token fee for USDT. Default 100000.
//   OWNER_AFTER_DEPLOY       - if set, ownership is transferred to this
//                              address AFTER token config. Use this when
//                              the long-term owner is a multisig that
//                              should also control upgrades.
//   SONICSCAN_API_KEY        - if set, the implementation contract is
//                              verified on SonicScan automatically. Proxy
//                              verification typically requires clicking
//                              "Is this a proxy?" in the explorer.
//   CHECK_ONLY=1             - print the planned config and exit before
//                              touching the chain.
//
// Run:
//   npm run deploy:mainnet:service-contact
//
// After deploy, set SERVICE_CONTACT_ADDRESS on the API server to the
// printed proxy address. The startup bytecode check in
// service_contact_service.go will refuse to start with an empty address.
//
// MCGP: not configured here. The contract supports per-token fees so
// adding MCGP later is two transactions (setTokenAcceptance + setFeeAmount)
// once we have a price-derived fee in MCGP units.

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

function feeFromEnv(name, fallback) {
  const v = process.env[name];
  if (!v) return fallback;
  return BigInt(v);
}

async function main() {
  if (network.name !== "sonicMainnet") {
    throw new Error(
      `Refusing to run mainnet deploy on network=${network.name}. ` +
        `Use --network sonicMainnet.`,
    );
  }

  const [deployer] = await ethers.getSigners();
  const balance = await ethers.provider.getBalance(deployer.address);

  const usdc = requiredAddress("MAINNET_USDC_ADDRESS");
  const usdt = optionalAddress("MAINNET_USDT_ADDRESS");
  const ownerAfterDeploy = optionalAddress("OWNER_AFTER_DEPLOY");

  const usdcFee = feeFromEnv("FEE_USDC", 100000n); // $0.10 for 6-decimal token
  const usdtFee = feeFromEnv("FEE_USDT", 100000n);

  console.log("=== Sonic Mainnet ServiceContact Deploy (UUPS) ===");
  console.log("Network:        ", network.name, "(chain", network.config.chainId + ")");
  console.log("Deployer:       ", deployer.address);
  console.log("Balance:        ", ethers.formatEther(balance), "S");
  console.log("Initial Owner:  ", deployer.address, "(deployer)");
  console.log("USDC:           ", usdc, "fee=" + usdcFee.toString());
  console.log(
    "USDT:           ",
    usdt ? `${usdt} fee=${usdtFee.toString()}` : "(not whitelisted)",
  );
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

  console.log("Deploying ServiceContact proxy...");
  const ServiceContact = await ethers.getContractFactory("ServiceContact");
  const service = await upgrades.deployProxy(
    ServiceContact,
    [deployer.address],
    { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] },
  );
  await service.waitForDeployment();
  const proxyAddr = await service.getAddress();
  const implAddr = await upgrades.erc1967.getImplementationAddress(proxyAddr);
  console.log("Proxy:          ", proxyAddr);
  console.log("Implementation: ", implAddr);
  console.log("");

  console.log("Configuring tokens...");
  let tx = await service.setTokenAcceptance(usdc, true);
  await tx.wait();
  console.log("  setTokenAcceptance: ", usdc, "(USDC)");
  tx = await service.setFeeAmount(usdc, usdcFee);
  await tx.wait();
  console.log("  setFeeAmount:       ", usdc, "=", usdcFee.toString());

  if (usdt) {
    tx = await service.setTokenAcceptance(usdt, true);
    await tx.wait();
    console.log("  setTokenAcceptance: ", usdt, "(USDT)");
    tx = await service.setFeeAmount(usdt, usdtFee);
    await tx.wait();
    console.log("  setFeeAmount:       ", usdt, "=", usdtFee.toString());
  }

  if (
    ownerAfterDeploy &&
    ownerAfterDeploy.toLowerCase() !== deployer.address.toLowerCase()
  ) {
    console.log("");
    console.log(`Transferring ownership to ${ownerAfterDeploy}...`);
    tx = await service.transferOwnership(ownerAfterDeploy);
    await tx.wait();
    console.log("  Done. Verify with: service.owner() === " + ownerAfterDeploy);
  }

  console.log("");
  console.log("=== Deployment Summary ===");
  console.log("SERVICE_CONTACT_ADDRESS=" + proxyAddr);
  console.log("Implementation:        " + implAddr);
  console.log(
    "Final owner:           " + (ownerAfterDeploy || deployer.address),
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
        "  Retry manually: npx hardhat verify --network sonicMainnet " +
          implAddr,
      );
    }
    console.log(
      "Note: proxy verification on SonicScan usually requires clicking " +
        '"Is this a proxy?" on the contract page.',
    );
  } else {
    console.log("");
    console.log(
      "(SONICSCAN_API_KEY not set — skipping verification. Run manually:",
    );
    console.log(
      "  npx hardhat verify --network sonicMainnet " + implAddr + ")",
    );
  }
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
