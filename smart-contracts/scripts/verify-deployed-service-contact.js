// Quick read of the deployed ServiceContact proxy. Useful as a smoke test
// after deploy and any time the BE refuses to start due to missing bytecode.
//
// Run:
//   PROXY=0x... npx hardhat run scripts/verify-deployed-service-contact.js --network sonicMainnet

const { ethers } = require("hardhat");

async function main() {
  const proxyAddr = process.env.PROXY;
  if (!proxyAddr) throw new Error("Set PROXY=<address>");

  const sc = await ethers.getContractAt("ServiceContact", proxyAddr);
  console.log("Proxy:           ", proxyAddr);
  console.log("owner:           ", await sc.owner());
  console.log("paused:          ", await sc.paused());

  const tokens = (process.env.TOKENS || "").split(",").filter(Boolean);
  for (const t of tokens) {
    const accepted = await sc.acceptedTokens(t);
    const fee = await sc.feeAmounts(t);
    console.log(`  ${t}: accepted=${accepted} fee=${fee.toString()}`);
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
