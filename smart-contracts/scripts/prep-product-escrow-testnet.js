// Testnet test-cutover prep for ProductEscrow:
//   1. forceImport the live V1 proxy into the OZ manifest (manifest-only, no tx)
//   2. upgradeProxy V1 -> ProductEscrowV2 (calls initializeV2 -> legacySplitEnabled=true)
//   3. setFeeManager + setTokenSymbol(USDT/USDC/MCGP)
// Leaves legacySplitEnabled=TRUE (flipped to false later in the cutover window).
//
// Run:
//   PRODUCT_ESCROW_PROXY=0x... FEE_MANAGER=0x... \
//     npx hardhat run scripts/prep-product-escrow-testnet.js --network sonicTestnet

const { ethers, network, upgrades } = require("hardhat");

const TOKENS = {
  USDT: process.env.TESTNET_USDT_ADDRESS || "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
  USDC: process.env.TESTNET_USDC_ADDRESS || "0x685CD180e603A15507f575E7A347610b83d36d41",
  MCGP: process.env.TESTNET_MCGP_ADDRESS || "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
};

async function main() {
  const proxy = process.env.PRODUCT_ESCROW_PROXY;
  const fm = process.env.FEE_MANAGER;
  if (!proxy || !ethers.isAddress(proxy)) throw new Error("Set PRODUCT_ESCROW_PROXY");
  if (!fm || !ethers.isAddress(fm)) throw new Error("Set FEE_MANAGER");
  if ((await ethers.provider.getCode(proxy)) === "0x") throw new Error(`No contract at ${proxy}`);

  const [signer] = await ethers.getSigners();
  console.log("=== Prep ProductEscrow -> V2 (testnet) ===");
  console.log("Network:", network.name, "chain", network.config.chainId);
  console.log("Signer: ", signer.address);
  console.log("Proxy:  ", ethers.getAddress(proxy));

  // 1. forceImport V1 layout (manifest only)
  const V1 = await ethers.getContractFactory("ProductEscrow");
  await upgrades.forceImport(proxy, V1, { kind: "uups" });
  console.log("forceImport OK (V1 layout registered)");

  // 2. upgrade to V2 + initializeV2()
  const V2 = await ethers.getContractFactory("ProductEscrowV2");
  await upgrades.upgradeProxy(proxy, V2, { call: { fn: "initializeV2" } });
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  console.log("Upgraded to ProductEscrowV2, impl:", impl);

  // 3. wire feeManager + tokenSymbols
  const c = await ethers.getContractAt("ProductEscrowV2", proxy);
  await (await c.setFeeManager(fm)).wait();
  console.log("setFeeManager:", fm);
  for (const [sym, addr] of Object.entries(TOKENS)) {
    await (await c.setTokenSymbol(addr, sym)).wait();
    console.log(`  setTokenSymbol ${addr} -> ${sym}`);
  }

  // verify
  const lse = await c.legacySplitEnabled();
  const wired = await c.feeManager();
  console.log("VERIFY legacySplitEnabled:", lse, "(expect true)");
  console.log("VERIFY feeManager:", wired, "(expect", fm + ")");
  if (lse !== true) throw new Error("legacySplitEnabled should be true post-prep");
  if (wired.toLowerCase() !== fm.toLowerCase()) throw new Error("feeManager mismatch");
  console.log("Product escrow is cutover-ready (split still legacy; flip to false in the window).");
}

main().catch((e) => { console.error(e); process.exit(1); });
