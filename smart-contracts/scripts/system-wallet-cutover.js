// Point fee payouts at the system fee wallet and stop the on-chain cashback split.
//
// Per escrow (ProductEscrowV2, ServiceContactV2 on Sonic):
//   1. upgrade the proxy to the build that has systemWallet (skipped when it
//      already answers systemWallet())
//   2. setSystemWallet(FEE_WALLET)
//   3. setLegacySplitEnabled(false) — the on-chain 25/25/50 split pays buyer and
//      upline USDC while the off-chain engine also credits them cashback, so
//      users are paid twice; with it off the whole fee goes to the system wallet
//      and the engine is the sole splitter (spec D5).
// P2P escrows already carry setSystemWallet: pass P2P=0x… (on the right network)
// to repoint one, e.g. the BSC escrow still paying 0xA412….
//
// Dry-run by default. Every step is idempotent, so a partial run can be repeated.
//
// Usage:
//   npx hardhat run scripts/system-wallet-cutover.js --network sonicMainnet
//   EXECUTE=1 npx hardhat run scripts/system-wallet-cutover.js --network sonicMainnet
//   EXECUTE=1 ONLY=p2p P2P=0xD16c1597EA3be8D2017698CC5d6Bf53b595EEb03 \
//     npx hardhat run scripts/system-wallet-cutover.js --network <bsc network>
//
// Env:
//   FEE_WALLET  default 0xaF326D5D242C9A55590540f14658adDDd3586A8d
//   PRODUCT / SERVICE  default the Sonic mainnet proxies
//   P2P         a P2P escrow proxy to repoint (systemWallet only)
//   ONLY=p2p    skip product and service
//   EXECUTE=1   broadcast (signer must be the contract owner)
const { ethers } = require("hardhat");

const FEE_WALLET = process.env.FEE_WALLET || "0xaF326D5D242C9A55590540f14658adDDd3586A8d";
const PRODUCT = process.env.PRODUCT || "0x42C3c60F40c48497D023166C6088e27872D63fcb";
const SERVICE = process.env.SERVICE || "0xAb3574501d41940cD860Ecbfc535BFa6D9e13Ff3";
const execute = process.env.EXECUTE === "1";

const ABI = [
  "function owner() view returns (address)",
  "function systemWallet() view returns (address)",
  "function setSystemWallet(address)",
  "function legacySplitEnabled() view returns (bool)",
  "function setLegacySplitEnabled(bool)",
  "function upgradeToAndCall(address,bytes)",
];

const same = (a, b) => a.toLowerCase() === b.toLowerCase();

async function hasSystemWallet(c) {
  try {
    await c.systemWallet();
    return true;
  } catch {
    return false; // the implementation predates setSystemWallet
  }
}

async function repoint(name, c, signer) {
  if (!same(await c.owner(), signer.address)) {
    throw new Error(`${name}: signer ${signer.address} is not the owner (${await c.owner()})`);
  }
  const current = await c.systemWallet();
  if (same(current, FEE_WALLET)) {
    console.log(`  ${name}: systemWallet already ${FEE_WALLET}`);
    return;
  }
  console.log(`  ${name}: systemWallet ${current} -> ${FEE_WALLET}`);
  if (execute) await (await c.setSystemWallet(FEE_WALLET)).wait();
}

async function upgradeAndFlip(name, factoryName, addr, signer) {
  const c = new ethers.Contract(addr, ABI, signer);
  if (!same(await c.owner(), signer.address)) {
    throw new Error(`${name}: signer ${signer.address} is not the owner (${await c.owner()})`);
  }
  if (!(await hasSystemWallet(c))) {
    console.log(`  ${name}: upgrade to ${factoryName} (adds systemWallet)`);
    if (execute) {
      const impl = await (await ethers.getContractFactory(factoryName)).deploy();
      await impl.waitForDeployment();
      await (await c.upgradeToAndCall(await impl.getAddress(), "0x")).wait();
      console.log(`    new implementation ${await impl.getAddress()}`);
    }
  } else {
    console.log(`  ${name}: already has systemWallet, no upgrade`);
  }
  // Before the upgrade runs (dry-run), systemWallet() cannot be read back.
  if (execute || (await hasSystemWallet(c))) await repoint(name, c, signer);
  else console.log(`  ${name}: systemWallet -> ${FEE_WALLET} (after the upgrade)`);

  if (await c.legacySplitEnabled()) {
    console.log(`  ${name}: legacySplitEnabled true -> false`);
    if (execute) await (await c.setLegacySplitEnabled(false)).wait();
  } else {
    console.log(`  ${name}: legacySplitEnabled already false`);
  }
}

async function main() {
  if (!ethers.isAddress(FEE_WALLET)) throw new Error(`bad FEE_WALLET ${FEE_WALLET}`);
  const [signer] = await ethers.getSigners();
  const net = await ethers.provider.getNetwork();
  console.log(`${execute ? "EXECUTE" : "DRY RUN"} on chain ${net.chainId} as ${signer.address}`);
  console.log(`fee wallet: ${FEE_WALLET}`);

  if (process.env.ONLY !== "p2p") {
    await upgradeAndFlip("ProductEscrowV2", "ProductEscrowV2", PRODUCT, signer);
    await upgradeAndFlip("ServiceContactV2", "ServiceContactV2", SERVICE, signer);
  }
  if (process.env.P2P) {
    await repoint("P2P escrow", new ethers.Contract(process.env.P2P, ABI, signer), signer);
  }
  if (!execute) console.log("\nDry run only. Re-run with EXECUTE=1 to broadcast.");
}

main().catch((e) => { console.error(e); process.exit(1); });
