// Reports an address's balance + allowance against a given token + escrow.
// Diagnostics for "ERC20: transfer amount exceeds balance" reverts.
//
// Usage:
//   FAUCET_TO=0xWallet TOKEN=USDT|USDC SPENDER=0xEscrow \
//     npx hardhat run scripts/check-token-balance.js --network sonicTestnet

const { ethers, network } = require("hardhat");

const TOKENS = {
  USDT: { addr: "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad", decimals: 6 },
  USDC: { addr: "0x685CD180e603A15507f575E7A347610b83d36d41", decimals: 6 },
};
const DEFAULT_ESCROW = "0xadB543F2AD95ad38243d36E46530fE1c80c70Fe5";

const ERC20_ABI = [
  "function balanceOf(address) view returns (uint256)",
  "function allowance(address owner, address spender) view returns (uint256)",
];

async function main() {
  const owner = process.env.FAUCET_TO;
  if (!owner || !ethers.isAddress(owner)) {
    throw new Error("Set FAUCET_TO=0x... to the wallet you want to inspect.");
  }
  const spender = process.env.SPENDER || DEFAULT_ESCROW;

  console.log(`Network:  ${network.name} (chain ${network.config.chainId})`);
  console.log(`Owner:    ${owner}`);
  console.log(`Spender:  ${spender} (P2PEscrow)\n`);

  for (const [sym, info] of Object.entries(TOKENS)) {
    const c = new ethers.Contract(info.addr, ERC20_ABI, ethers.provider);
    const [bal, allow] = await Promise.all([
      c.balanceOf(owner),
      c.allowance(owner, spender),
    ]);
    console.log(`${sym}  ${info.addr}`);
    console.log(`  balance:   ${ethers.formatUnits(bal, info.decimals)}`);
    console.log(`  allowance: ${ethers.formatUnits(allow, info.decimals)}\n`);
  }
}

main().catch((e) => { console.error(e); process.exit(1); });
