// Scan recent BSC testnet blocks for ERC20 Transfer events where the
// deployer is the recipient. Unique `address` values are the token
// contracts that have sent tokens to this wallet (faucets, mints, etc.).
//
// BSC testnet limits eth_getLogs to ~5000 blocks per call, so we walk
// backwards in chunks.

const { ethers, network } = require("hardhat");

const TRANSFER_TOPIC =
  "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef";
const CHUNK = 4900;
const MAX_BLOCKS_BACK = 1_000_000; // ~10 days on BSC

const ERC20_ABI = [
  "function symbol() view returns (string)",
  "function decimals() view returns (uint8)",
  "function name() view returns (string)",
  "function balanceOf(address) view returns (uint256)",
];

async function main() {
  const [signer] = await ethers.getSigners();
  const provider = ethers.provider;
  const head = await provider.getBlockNumber();

  console.log(`Network:  ${network.name} (chain ${network.config.chainId})`);
  console.log(`Wallet:   ${signer.address}`);
  console.log(`Scanning blocks ${head - MAX_BLOCKS_BACK} .. ${head}\n`);

  const padded =
    "0x" + signer.address.slice(2).toLowerCase().padStart(64, "0");

  const found = new Map(); // address -> { symbol, decimals, balance }

  for (let to = head; to > head - MAX_BLOCKS_BACK && to > 0; to -= CHUNK + 1) {
    const from = Math.max(0, to - CHUNK);
    let logs;
    try {
      logs = await provider.getLogs({
        fromBlock: from,
        toBlock: to,
        topics: [TRANSFER_TOPIC, null, padded],
      });
    } catch (e) {
      console.log(`  (skip ${from}..${to}: ${e.shortMessage || e.message})`);
      continue;
    }
    for (const log of logs) {
      const addr = ethers.getAddress(log.address);
      if (!found.has(addr)) found.set(addr, null);
    }
    if (logs.length > 0) {
      process.stdout.write(
        `  ${from}..${to}: ${logs.length} transfer(s), ${found.size} unique token(s) so far\r`
      );
    }
  }
  console.log("");
  console.log("");

  if (found.size === 0) {
    console.log("No ERC20 Transfer logs found in that range. Try widening MAX_BLOCKS_BACK.");
    return;
  }

  console.log("Resolving token metadata + balances...\n");
  for (const addr of found.keys()) {
    try {
      const c = new ethers.Contract(addr, ERC20_ABI, provider);
      const [sym, dec, bal] = await Promise.all([
        c.symbol(),
        c.decimals(),
        c.balanceOf(signer.address),
      ]);
      const human = ethers.formatUnits(bal, dec);
      const marker = bal > 0n ? "👉" : "  ";
      console.log(`${marker} ${addr}  ${sym.padEnd(8)} ${dec}dec  bal=${human}`);
    } catch (e) {
      console.log(`   ${addr}  (metadata read failed)`);
    }
  }
}

main().catch((e) => { console.error(e); process.exit(1); });
