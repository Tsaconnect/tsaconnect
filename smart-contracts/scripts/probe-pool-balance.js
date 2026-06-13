// Calls balanceOf(seller, token) on the deployed P2PEscrow proxy and
// prints what the chain returns — so we can tell whether the empty-data
// issue is upstream of the Go service or an actual contract problem.
const { ethers, network } = require("hardhat");

const ESCROW = "0xadB543F2AD95ad38243d36E46530fE1c80c70Fe5"; // testnet proxy
const USDT = "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad";
const SELLER = "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce";

const ABI = [
  "function balanceOf(address seller, address token) view returns (uint256 _available, uint256 _locked)",
  "function acceptedTokens(address) view returns (bool)",
];

async function main() {
  console.log(`Network: ${network.name} (chain ${network.config.chainId})`);
  console.log(`Escrow:  ${ESCROW}`);
  console.log(`Seller:  ${SELLER}`);
  console.log(`Token:   ${USDT}\n`);

  // Raw call so we see exactly what the chain returns (matches what the Go
  // service sees when it Unpack()s).
  const iface = new ethers.Interface(ABI);
  const data = iface.encodeFunctionData("balanceOf", [SELLER, USDT]);
  console.log(`Call data: ${data}`);
  const raw = await ethers.provider.call({ to: ESCROW, data });
  console.log(`Raw return: ${raw}  (length ${(raw.length - 2) / 2} bytes)`);

  if (raw === "0x" || raw.length === 2) {
    console.log("❌ Empty data — the function selector didn't match the contract.");
  } else {
    try {
      const decoded = iface.decodeFunctionResult("balanceOf", raw);
      console.log(`✅ Decoded: available=${decoded[0]}  locked=${decoded[1]}`);
    } catch (e) {
      console.log(`❌ Decode failed: ${e.message}`);
    }
  }

  // Also confirm USDT is whitelisted on this escrow.
  const accepted = await new ethers.Contract(ESCROW, ABI, ethers.provider).acceptedTokens(USDT);
  console.log(`\nacceptedTokens(USDT) = ${accepted}`);
}

main().catch((e) => { console.error(e); process.exit(1); });
