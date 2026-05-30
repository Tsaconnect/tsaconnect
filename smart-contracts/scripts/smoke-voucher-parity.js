// Decisive voucher-path proof against the LIVE P2PEscrowV2 — no funds, no API.
// Proves the EIP-712 digest computed from the EXACT domain+types the Go API uses
// (p2p_trade_service.go) equals the deployed contract's hashVoucher(), and that a
// signature over that typed-data recovers to the signer. This is exactly what the
// API's CreateAd (builds typedData) + SignAd (ecrecover) rely on, end to end.
//   node scripts/smoke-voucher-parity.js
const { ethers } = require("ethers");

const RPC = "https://rpc.testnet.soniclabs.com";
const ESCROW = "0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7";
const USDC = "0x685CD180e603A15507f575E7A347610b83d36d41";

// EXACTLY mirrors internal/services/p2p_trade_service.go buildVoucherTypedData
const domain = { name: "TSAConnectP2PEscrow", version: "2", chainId: 14601, verifyingContract: ESCROW };
const types = {
  AdVoucher: [
    { name: "seller", type: "address" },
    { name: "token", type: "address" },
    { name: "maxAmount", type: "uint256" },
    { name: "minPerTrade", type: "uint256" },
    { name: "maxPerTrade", type: "uint256" },
    { name: "deadline", type: "uint256" },
    { name: "nonce", type: "bytes32" },
  ],
};

(async () => {
  const provider = new ethers.JsonRpcProvider(RPC);
  const wallet = ethers.Wallet.createRandom(); // throwaway seller
  const voucher = {
    seller: wallet.address,
    token: USDC,
    maxAmount: 1_000_000n,        // 1 USDC (6 decimals)
    minPerTrade: 0n,
    maxPerTrade: 0n,
    deadline: BigInt(Math.floor(Date.now() / 1000) + 86400),
    nonce: ethers.hexlify(ethers.randomBytes(32)),
  };

  // 1) Digest the Go API would produce (ethers EIP-712 over the same domain/types)
  const localDigest = ethers.TypedDataEncoder.hash(domain, types, voucher);

  // 2) Digest the LIVE contract produces via hashVoucher()
  const escrow = new ethers.Contract(
    ESCROW,
    ["function hashVoucher((address seller,address token,uint256 maxAmount,uint256 minPerTrade,uint256 maxPerTrade,uint256 deadline,bytes32 nonce) v) view returns (bytes32)"],
    provider
  );
  const onchainDigest = await escrow.hashVoucher(voucher);

  console.log("seller (throwaway):", wallet.address);
  console.log("local  EIP-712 digest:", localDigest);
  console.log("onchain hashVoucher  :", onchainDigest);
  const digestMatch = localDigest.toLowerCase() === onchainDigest.toLowerCase();
  console.log("DIGEST PARITY        :", digestMatch ? "MATCH ✓ (Go typed-data == live contract)" : "MISMATCH ✗");

  // 3) Sign + recover (mirrors SignAd's ecrecover==seller acceptance)
  const sig = await wallet.signTypedData(domain, types, voucher);
  const recovered = ethers.verifyTypedData(domain, types, voucher, sig);
  console.log("signature recovers to:", recovered);
  console.log("RECOVER == seller     :", recovered.toLowerCase() === wallet.address.toLowerCase() ? "OK ✓" : "FAIL ✗");

  // 4) Negative control: a wrong domain version must NOT match the contract
  const badDigest = ethers.TypedDataEncoder.hash({ ...domain, version: "1" }, types, voucher);
  console.log("v1-domain digest match:", badDigest.toLowerCase() === onchainDigest.toLowerCase() ? "MATCH (UNEXPECTED!)" : "no match ✓ (confirms version=2 is load-bearing)");

  process.exit(digestMatch && recovered.toLowerCase() === wallet.address.toLowerCase() ? 0 : 1);
})().catch((e) => { console.error("FATAL:", e.shortMessage || e.message); process.exit(1); });
