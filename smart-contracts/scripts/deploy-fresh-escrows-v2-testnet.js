// Fresh UUPS deploys of ProductEscrowV2 + ServiceContactV2 on Sonic testnet.
// Needed because the live testnet ProductEscrow is a NON-proxy (can't upgrade)
// and no ServiceContact exists on testnet. Both deploy with legacySplitEnabled=
// true (today's behavior) and are wired to the FeeManager; the split is flipped
// to false later in the cutover window.
//
// Run:
//   FEE_MANAGER=0x... npx hardhat run scripts/deploy-fresh-escrows-v2-testnet.js --network sonicTestnet

const { ethers, network, upgrades } = require("hardhat");

const T = {
  USDT: process.env.TESTNET_USDT_ADDRESS || "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
  USDC: process.env.TESTNET_USDC_ADDRESS || "0x685CD180e603A15507f575E7A347610b83d36d41",
  MCGP: process.env.TESTNET_MCGP_ADDRESS || "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
};
const SERVICE_FEE = 100000n; // $0.10 in 6-dec token units (>= MIN_FEE_AMOUNT 10000)

async function main() {
  const fm = process.env.FEE_MANAGER;
  if (!fm || !ethers.isAddress(fm)) throw new Error("Set FEE_MANAGER");
  const [signer] = await ethers.getSigners();
  const owner = signer.address;
  console.log("=== Fresh ProductEscrowV2 + ServiceContactV2 (testnet) ===");
  console.log("Network:", network.name, "chain", network.config.chainId, "| owner/signer:", owner);
  console.log("FeeManager:", fm);

  // --- ProductEscrowV2 ---
  const Product = await ethers.getContractFactory("ProductEscrowV2");
  // NOTE: unsafeAllow constructor — the escrows inherit non-upgradeable ReentrancyGuard
  // (has a constructor). Matches the already-deployed testnet P2P pattern; OZ v5 tolerates
  // _status=0 as not-entered. FLAGGED FOR THE MAINNET RE-AUDIT: switch to
  // ReentrancyGuardUpgradeable. This bypass is for the testnet mechanism test only.
  const product = await upgrades.deployProxy(Product, [owner, []], { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] });
  await product.waitForDeployment();
  const productAddr = await product.getAddress();
  await (await product.setFeeManager(fm)).wait();
  await (await product.setMcgpToken(T.MCGP)).wait();
  for (const [sym, addr] of Object.entries(T)) await (await product.setTokenSymbol(addr, sym)).wait();
  console.log("\nProductEscrowV2:", productAddr);
  console.log("  legacySplitEnabled:", await product.legacySplitEnabled(), "feeManager:", await product.feeManager());

  // --- ServiceContactV2 ---
  const Service = await ethers.getContractFactory("ServiceContactV2");
  const service = await upgrades.deployProxy(Service, [owner], { kind: "uups", initializer: "initialize", unsafeAllow: ["constructor"] });
  await service.waitForDeployment();
  const serviceAddr = await service.getAddress();
  await (await service.setFeeManager(fm)).wait();
  for (const [sym, addr] of Object.entries(T)) await (await service.setTokenSymbol(addr, sym)).wait();
  await (await service.setFeeAmount(T.USDT, SERVICE_FEE)).wait();
  await (await service.setFeeAmount(T.USDC, SERVICE_FEE)).wait();
  console.log("\nServiceContactV2:", serviceAddr);
  console.log("  legacySplitEnabled:", await service.legacySplitEnabled(), "feeManager:", await service.feeManager());

  console.log("\n=== Paste into tsa-api-go/.env (replace the old values) ===");
  console.log("PRODUCT_ESCROW_ADDRESS=" + productAddr);
  console.log("SERVICE_CONTACT_ADDRESS=" + serviceAddr);
}

main().catch((e) => { console.error(e); process.exit(1); });
