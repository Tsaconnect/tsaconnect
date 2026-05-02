// Read ERC20 metadata from any address.
// Usage: TOKEN=0x... npx hardhat run scripts/check-token.js --network <net>

const { ethers, network } = require("hardhat");

const ABI = [
  "function name() view returns (string)",
  "function symbol() view returns (string)",
  "function decimals() view returns (uint8)",
  "function totalSupply() view returns (uint256)",
];

async function main() {
  const token = process.env.TOKEN;
  if (!token || !ethers.isAddress(token)) {
    throw new Error("TOKEN env var must be a valid address");
  }
  const code = await ethers.provider.getCode(token);
  if (code === "0x") {
    throw new Error(`No contract deployed at ${token} on ${network.name}`);
  }
  const c = new ethers.Contract(token, ABI, ethers.provider);
  const [name, symbol, decimals, totalSupply] = await Promise.all([
    c.name().catch((e) => `(name() failed: ${e.shortMessage || e.message})`),
    c.symbol().catch((e) => `(symbol() failed: ${e.shortMessage || e.message})`),
    c.decimals().catch((e) => `(decimals() failed: ${e.shortMessage || e.message})`),
    c.totalSupply().catch((e) => `(totalSupply() failed: ${e.shortMessage || e.message})`),
  ]);
  console.log("Network:    ", network.name, "(chain", network.config.chainId + ")");
  console.log("Address:    ", token);
  console.log("name:       ", name);
  console.log("symbol:     ", symbol);
  console.log("decimals:   ", decimals);
  console.log(
    "totalSupply:",
    typeof totalSupply === "bigint" ? totalSupply.toString() : totalSupply
  );
}

main().catch((e) => {
  console.error(e.message || e);
  process.exit(1);
});
