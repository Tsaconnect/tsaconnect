// Probe the deployer's balance on a few well-known BSC testnet USDT
// contracts so we can identify which faucet was used.
const { ethers, network } = require("hardhat");

const ERC20_ABI = [
  "function balanceOf(address) view returns (uint256)",
  "function decimals() view returns (uint8)",
  "function symbol() view returns (string)",
  "function name() view returns (string)",
];

// Curated list of BSC testnet USDT contracts that are commonly handed out
// by faucets. If your faucet uses a different one, paste the address into
// the BSC_TESTNET_USDT_ADDRESS env var and we'll probe that instead.
const KNOWN = [
  "0x337610d27c682E347C9cD60BD4b3b107C9d34dDd",
  "0x7ef95a0FEE0Dd31b22626fA2e10Ee6A223F8a684",
  "0xeD24FC36d5Ee211Ea25A80239Fb8C4Cfd80f12Ee",
  "0xa9bA2980eE19FfeB10C1bB5C1763df54A4828e91",
];

async function main() {
  const [signer] = await ethers.getSigners();
  const candidates = process.env.BSC_TESTNET_USDT_ADDRESS
    ? [process.env.BSC_TESTNET_USDT_ADDRESS, ...KNOWN]
    : KNOWN;

  console.log(`Network:  ${network.name} (chain ${network.config.chainId})`);
  console.log(`Probing balances for ${signer.address}\n`);

  for (const addr of candidates) {
    try {
      const c = new ethers.Contract(addr, ERC20_ABI, ethers.provider);
      const [sym, dec, bal] = await Promise.all([
        c.symbol(),
        c.decimals(),
        c.balanceOf(signer.address),
      ]);
      const human = ethers.formatUnits(bal, dec);
      const marker = bal > 0n ? "👉" : "  ";
      console.log(`${marker} ${addr}  ${sym} (${dec} dec):  ${human}`);
    } catch (e) {
      console.log(`   ${addr}  (not an ERC20 / not deployed)`);
    }
  }
}

main().catch((e) => { console.error(e); process.exit(1); });
