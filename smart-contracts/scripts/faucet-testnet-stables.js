// Mints test USDT + USDC to a target wallet on Sonic testnet using the
// deployer key. The MockERC20 contracts have public mint() so anyone can
// call this, but using the existing deployer keeps everything in one
// place for dev convenience.
//
// Usage:
//   FAUCET_TO=0xYourTestWallet npx hardhat run \
//     scripts/faucet-testnet-stables.js --network sonicTestnet
//
// Optional:
//   FAUCET_AMOUNT=500   (default 1000 — applies to both USDT and USDC)

const { ethers, network } = require("hardhat");

const USDT = "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad";
const USDC = "0x685CD180e603A15507f575E7A347610b83d36d41";
const DECIMALS = 6;

const MINT_ABI = [
  "function mint(address to, uint256 amount) external",
  "function symbol() view returns (string)",
];

async function main() {
  if (network.name !== "sonicTestnet") {
    throw new Error(`Run with --network sonicTestnet (got ${network.name})`);
  }
  const to = process.env.FAUCET_TO;
  if (!to || !ethers.isAddress(to)) {
    throw new Error(
      "Set FAUCET_TO=0x... to the recipient wallet (e.g. your test wallet inside the app)."
    );
  }
  const recipient = ethers.getAddress(to);

  const human = process.env.FAUCET_AMOUNT || "1000";
  const amount = ethers.parseUnits(human, DECIMALS);

  const [signer] = await ethers.getSigners();
  console.log(`Network:   ${network.name} (chain ${network.config.chainId})`);
  console.log(`Signer:    ${signer.address}`);
  console.log(`Recipient: ${recipient}`);
  console.log(`Amount:    ${human} of each (USDT + USDC, ${DECIMALS} decimals)\n`);

  for (const [label, addr] of [["USDT", USDT], ["USDC", USDC]]) {
    const c = new ethers.Contract(addr, MINT_ABI, signer);
    const tx = await c.mint(recipient, amount);
    process.stdout.write(`${label}  ${tx.hash} ... `);
    await tx.wait();
    console.log("✓");
  }
  console.log("\nDone. Refresh the wallet on the app — the balances should appear.");
}

main().catch((e) => { console.error(e); process.exit(1); });
