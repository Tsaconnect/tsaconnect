// Registers an already-deployed P2PEscrowV2 proxy into the local OZ Upgrades
// manifest (.openzeppelin/<network>.json) so `upgradeProxy` can validate the
// V2->V3 storage layout. Needed when a proxy was deployed from a different
// machine/manifest (e.g. the Sonic-testnet P2P proxy 0x1B1ade… is live on-chain
// but absent from this working copy's manifest).
//
// Read-only w.r.t. the chain — forceImport sends NO transaction; it only writes
// the local manifest. Safe to re-run (idempotent).
//
// Run:
//   P2P_ESCROW_PROXY=0x... npx hardhat run scripts/forceimport-p2p-escrow-v2.js --network sonicTestnet

const { ethers, network, upgrades } = require("hardhat");

async function main() {
  const proxy = process.env.P2P_ESCROW_PROXY;
  if (!proxy || !ethers.isAddress(proxy)) {
    throw new Error("Set P2P_ESCROW_PROXY to the live P2PEscrowV2 proxy address.");
  }
  const code = await ethers.provider.getCode(proxy);
  if (code === "0x") throw new Error(`No contract code at ${proxy} on ${network.name}.`);

  console.log("=== forceImport P2PEscrowV2 proxy into OZ manifest ===");
  console.log("Network:", network.name, "(chain", network.config.chainId + ")");
  console.log("Proxy:  ", ethers.getAddress(proxy));

  const V2 = await ethers.getContractFactory("P2PEscrowV2");
  const imported = await upgrades.forceImport(proxy, V2, {
    kind: "uups",
  });
  await imported.waitForDeployment?.();

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  console.log("Registered. Current implementation:", impl);
  console.log("Manifest updated → upgrade-p2p-escrow-v3.js can now validate the V2→V3 layout.");
}

main().catch((e) => { console.error(e); process.exit(1); });
