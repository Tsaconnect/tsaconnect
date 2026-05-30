// Full ON-CHAIN round-trip against the deployed P2PEscrowV2 (Sonic testnet).
// Exercises BOTH happy paths end-to-end on the real contract:
//   • Sell-ad (PooledFromMaker): seller deposit -> sign voucher -> buyer openTrade -> markPaid -> release
//   • Buy-ad (DepositedByTaker):  taker approve -> openTradeWithDeposit -> markPaid -> release
// It self-funds ephemeral wallets from ONE funded DEPLOYER_PRIVATE_KEY (native S for
// gas + minted mock USDT), so the only thing you must provide is a deployer key that
// already holds Sonic-testnet S (from https://testnet.soniclabs.com/account).
//
//   DEPLOYER_PRIVATE_KEY=0x... npx hardhat run scripts/smoke-roundtrip-v2.js --network sonicTestnet
//   CHECK_ONLY=1 DEPLOYER_PRIVATE_KEY=0x... npx hardhat run scripts/smoke-roundtrip-v2.js --network sonicTestnet
//
// CHECK_ONLY prints the plan + confirms the deployer has gas, broadcasting nothing.
const { ethers } = require("hardhat");

const ESCROW = process.env.P2P_ESCROW_ADDRESS || "0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7";
const USDT = process.env.USDT_TOKEN_ADDRESS || "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad";
const GAS_FUND = ethers.parseEther("0.2");   // native S per ephemeral EOA
const AMOUNT = 1_000_000n;                    // 1 USDT (6 decimals) per flow
const CHECK_ONLY = !!process.env.CHECK_ONLY;

const escrowAbi = [
  "function deposit(address token, uint256 amount) external",
  "function openTrade(bytes32 tradeId, (address seller,address token,uint256 maxAmount,uint256 minPerTrade,uint256 maxPerTrade,uint256 deadline,bytes32 nonce) v, bytes sig, uint256 amount) external",
  "function openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant) external",
  "function markPaid(bytes32 tradeId) external",
  "function release(bytes32 tradeId) external",
  "function getTrade(bytes32 tradeId) view returns (address seller,address buyer,address token,uint256 amount,uint256 createdAt,uint8 status,uint8 funding,uint16 feeBps_,uint256 autoReleaseAt,bytes32 voucherHash)",
  "function balanceOf(address seller,address token) view returns (uint256 _available,uint256 _locked)",
  "function feeBps() view returns (uint16)",
  "function systemWallet() view returns (address)",
  "function acceptedTokens(address) view returns (bool)",
];
const erc20Abi = [
  "function mint(address to, uint256 amount) external",
  "function approve(address spender, uint256 amount) external returns (bool)",
  "function balanceOf(address) view returns (uint256)",
  "function decimals() view returns (uint8)",
];
const STATUS = ["None", "Pending", "Paid", "Released", "Cancelled", "Disputed", "Resolved"];
const rid = () => ethers.hexlify(ethers.randomBytes(32));
const ok = (c, m) => { if (!c) throw new Error("ASSERT FAILED: " + m); console.log("    ✓ " + m); };

async function main() {
  const provider = ethers.provider;
  const [deployer] = await ethers.getSigners();
  const net = await provider.getNetwork();
  console.log("network chainId:", net.chainId.toString(), "(expect 14601)");
  console.log("deployer       :", deployer.address);
  const bal = await provider.getBalance(deployer.address);
  console.log("deployer native S:", ethers.formatEther(bal));
  if (bal < ethers.parseEther("1")) throw new Error("Deployer needs >= ~1 S for gas + funding ephemeral wallets. Fund it at https://testnet.soniclabs.com/account");

  const escrow = new ethers.Contract(ESCROW, escrowAbi, deployer);
  const feeBps = await escrow.feeBps();
  const systemWallet = await escrow.systemWallet();
  ok(await escrow.acceptedTokens(USDT), `USDT ${USDT} is an accepted token`);
  console.log("feeBps:", feeBps.toString(), "systemWallet:", systemWallet);
  const fee = (AMOUNT * BigInt(feeBps)) / 10000n;
  const toRecipient = AMOUNT - fee;
  console.log(`per-flow: amount=${AMOUNT} fee=${fee} -> recipient gets ${toRecipient}`);

  if (CHECK_ONLY) {
    console.log("\nCHECK_ONLY=1 — would now: create 4 ephemeral EOAs, fund 0.2 S each, mint 2 USDT to seller+taker,");
    console.log("then run sell-ad (deposit/openTrade/markPaid/release) and buy-ad (openTradeWithDeposit/markPaid/release).");
    console.log("Nothing broadcast. Re-run without CHECK_ONLY to execute.");
    return;
  }

  // ── ephemeral wallets ──
  const mk = () => new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
  const sellerS = mk(), buyerS = mk(), takerB = mk(), merchantB = mk();
  console.log("\nephemeral wallets:");
  console.log("  sellerS  (sell-ad maker/crypto-provider):", sellerS.address);
  console.log("  buyerS   (sell-ad taker/crypto-recipient):", buyerS.address);
  console.log("  takerB   (buy-ad taker/crypto-provider)  :", takerB.address);
  console.log("  merchantB(buy-ad maker/crypto-recipient) :", merchantB.address);

  console.log("\nfunding gas (0.2 S each)...");
  for (const w of [sellerS, buyerS, takerB, merchantB]) {
    await (await deployer.sendTransaction({ to: w.address, value: GAS_FUND })).wait();
  }
  const usdtD = new ethers.Contract(USDT, erc20Abi, deployer);
  console.log("minting 2 USDT to sellerS + takerB...");
  await (await usdtD.mint(sellerS.address, AMOUNT * 2n)).wait();
  await (await usdtD.mint(takerB.address, AMOUNT * 2n)).wait();

  // ════ FLOW 1: SELL-AD (PooledFromMaker) ════
  console.log("\n══ FLOW 1: SELL-AD (PooledFromMaker) ══");
  const usdtSeller = new ethers.Contract(USDT, erc20Abi, sellerS);
  await (await usdtSeller.approve(ESCROW, AMOUNT)).wait();
  await (await escrow.connect(sellerS).deposit(USDT, AMOUNT)).wait();
  let [avail, lock] = await escrow.balanceOf(sellerS.address, USDT);
  ok(avail === AMOUNT && lock === 0n, `after deposit: available=${avail} locked=${lock}`);

  const domain = { name: "TSAConnectP2PEscrow", version: "2", chainId: Number(net.chainId), verifyingContract: ESCROW };
  const types = { AdVoucher: [
    { name: "seller", type: "address" }, { name: "token", type: "address" },
    { name: "maxAmount", type: "uint256" }, { name: "minPerTrade", type: "uint256" },
    { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" }, { name: "nonce", type: "bytes32" },
  ]};
  const voucher = { seller: sellerS.address, token: USDT, maxAmount: AMOUNT, minPerTrade: 0n, maxPerTrade: 0n,
    deadline: BigInt((await provider.getBlock("latest")).timestamp + 3600), nonce: rid() };
  const sig = await sellerS.signTypedData(domain, types, voucher);

  const tradeId1 = rid();
  await (await escrow.connect(buyerS).openTrade(tradeId1, voucher, sig, AMOUNT)).wait();
  let t = await escrow.getTrade(tradeId1);
  ok(STATUS[t[5]] === "Pending" && t[0].toLowerCase() === sellerS.address.toLowerCase() && t[1].toLowerCase() === buyerS.address.toLowerCase(),
    `openTrade: status=Pending seller=sellerS buyer=buyerS funding=${t[6]}(0=Pooled)`);
  [avail, lock] = await escrow.balanceOf(sellerS.address, USDT);
  ok(avail === 0n && lock === AMOUNT, `available->locked moved: available=${avail} locked=${lock}`);

  await (await escrow.connect(buyerS).markPaid(tradeId1)).wait();
  ok(STATUS[(await escrow.getTrade(tradeId1))[5]] === "Paid", "markPaid -> Paid");

  const buyerBefore = await new ethers.Contract(USDT, erc20Abi, provider).balanceOf(buyerS.address);
  const sysBefore = await new ethers.Contract(USDT, erc20Abi, provider).balanceOf(systemWallet);
  await (await escrow.connect(sellerS).release(tradeId1)).wait();
  ok(STATUS[(await escrow.getTrade(tradeId1))[5]] === "Released", "release -> Released");
  const buyerAfter = await new ethers.Contract(USDT, erc20Abi, provider).balanceOf(buyerS.address);
  const sysAfter = await new ethers.Contract(USDT, erc20Abi, provider).balanceOf(systemWallet);
  ok(buyerAfter - buyerBefore === toRecipient, `buyerS received ${buyerAfter - buyerBefore} (= amount-fee)`);
  ok(sysAfter - sysBefore === fee, `systemWallet received fee ${sysAfter - sysBefore}`);

  // ════ FLOW 2: BUY-AD (DepositedByTaker) ════
  console.log("\n══ FLOW 2: BUY-AD (DepositedByTaker) ══");
  await (await new ethers.Contract(USDT, erc20Abi, takerB).approve(ESCROW, AMOUNT)).wait();
  const tradeId2 = rid();
  await (await escrow.connect(takerB).openTradeWithDeposit(tradeId2, USDT, AMOUNT, merchantB.address)).wait();
  t = await escrow.getTrade(tradeId2);
  ok(STATUS[t[5]] === "Pending" && t[0].toLowerCase() === takerB.address.toLowerCase() && t[1].toLowerCase() === merchantB.address.toLowerCase() && Number(t[6]) === 1,
    `openTradeWithDeposit: status=Pending seller=takerB buyer=merchantB funding=${t[6]}(1=DepositedByTaker)`);
  [, lock] = await escrow.balanceOf(takerB.address, USDT);
  ok(lock === AMOUNT, `taker locked=${lock}`);

  await (await escrow.connect(merchantB).markPaid(tradeId2)).wait();
  ok(STATUS[(await escrow.getTrade(tradeId2))[5]] === "Paid", "markPaid(by merchant) -> Paid");

  const merchBefore = await new ethers.Contract(USDT, erc20Abi, provider).balanceOf(merchantB.address);
  await (await escrow.connect(takerB).release(tradeId2)).wait();
  ok(STATUS[(await escrow.getTrade(tradeId2))[5]] === "Released", "release(by taker) -> Released");
  const merchAfter = await new ethers.Contract(USDT, erc20Abi, provider).balanceOf(merchantB.address);
  ok(merchAfter - merchBefore === toRecipient, `merchantB received ${merchAfter - merchBefore} (= amount-fee)`);

  console.log("\n✓✓✓ BOTH ON-CHAIN ROUND-TRIPS PASSED on the deployed P2PEscrowV2.");
}

main().then(() => process.exit(0)).catch((e) => { console.error("\n✗ FAILED:", e.message || e); process.exit(1); });
