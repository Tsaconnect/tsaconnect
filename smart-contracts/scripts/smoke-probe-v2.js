// Read-only smoke probe of the deployed P2PEscrowV2 on Sonic testnet.
// No funds / no signing required — confirms the deployment is live and the
// EIP-712 domain matches what the Go API will use (the critical invariant).
//   node scripts/smoke-probe-v2.js
const { ethers } = require("ethers");

const RPC = process.env.SONIC_TESTNET_RPC || "https://rpc.testnet.soniclabs.com";
const ESCROW = process.env.P2P_ESCROW_ADDRESS || "0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7";
const EXPECT_OWNER = "0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9";
const TOKENS = {
  USDC: "0x685CD180e603A15507f575E7A347610b83d36d41",
  USDT: "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
  MCGP: "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
};

const escrowAbi = [
  "function owner() view returns (address)",
  "function eip712Domain() view returns (bytes1 fields, string name, string version, uint256 chainId, address verifyingContract, bytes32 salt, uint256[] extensions)",
  "function balanceOf(address token) view returns (uint256)",
  "function accountedBalance(address token) view returns (uint256)",
  "function feeBps() view returns (uint16)",
  "function defaultFeeBps() view returns (uint16)",
  "function trustedForwarder() view returns (address)",
  "function paused() view returns (bool)",
];
const erc20Abi = [
  "function name() view returns (string)",
  "function symbol() view returns (string)",
  "function decimals() view returns (uint8)",
];

const tryCall = async (label, fn) => {
  try { console.log(label, await fn()); }
  catch (e) { console.log(label, "ERR:", e.shortMessage || e.message); }
};

(async () => {
  const provider = new ethers.JsonRpcProvider(RPC);
  const net = await provider.getNetwork();
  console.log("RPC chainId        :", net.chainId.toString(), net.chainId === 14601n ? "(testnet OK)" : "(UNEXPECTED)");

  const code = await provider.getCode(ESCROW);
  console.log("escrow address     :", ESCROW);
  console.log("escrow code bytes  :", (code.length - 2) / 2, code === "0x" ? "(NO CONTRACT!)" : "(deployed OK)");

  const e = new ethers.Contract(ESCROW, escrowAbi, provider);
  await tryCall("owner              :", async () => {
    const o = await e.owner();
    return o + (o.toLowerCase() === EXPECT_OWNER.toLowerCase() ? " (matches expected owner)" : " (UNEXPECTED OWNER)");
  });
  await tryCall("paused             :", () => e.paused());
  await tryCall("trustedForwarder   :", () => e.trustedForwarder());

  console.log("\n--- EIP-712 domain (ERC-5267) — must be name=?, version=2, chainId=14601, verifyingContract=escrow ---");
  try {
    const d = await e.eip712Domain();
    console.log("  name             :", d.name);
    console.log("  version          :", d.version, d.version === "2" ? "(V2 OK)" : "(MISMATCH — expected 2)");
    console.log("  chainId          :", d.chainId.toString(), d.chainId === 14601n ? "(OK)" : "(MISMATCH)");
    console.log("  verifyingContract:", d.verifyingContract, d.verifyingContract.toLowerCase() === ESCROW.toLowerCase() ? "(OK)" : "(MISMATCH)");
  } catch (err) {
    console.log("  eip712Domain ERR :", err.shortMessage || err.message, "(contract may not expose ERC-5267)");
  }

  console.log("\n--- fee config ---");
  await tryCall("feeBps             :", () => e.feeBps().then(String));
  await tryCall("defaultFeeBps      :", () => e.defaultFeeBps().then(String));

  console.log("\n--- tokens + escrow balances ---");
  for (const [sym, addr] of Object.entries(TOKENS)) {
    const t = new ethers.Contract(addr, erc20Abi, provider);
    const c = await provider.getCode(addr);
    let meta = "(NO CONTRACT)";
    if (c !== "0x") {
      try { meta = `name=${await t.name()} symbol=${await t.symbol()} decimals=${await t.decimals()}`; }
      catch (er) { meta = "ERR: " + (er.shortMessage || er.message); }
    }
    console.log(`  ${sym} ${addr}: ${meta}`);
    await tryCall(`    escrow.balanceOf  :`, () => e.balanceOf(addr).then(String));
    await tryCall(`    accountedBalance  :`, () => e.accountedBalance(addr).then(String));
  }
})().catch((e) => { console.error("FATAL:", e); process.exit(1); });
