// scripts/rehearse-core-fork.js
//
// Fork rehearsal for the audit-ready core set with the REAL stablecoins: deploys it with
// deploy/core.js, then runs an order (Sonic) and a P2P trade (both chains) end to end and checks
// every balance. Sends nothing to a live chain.
//
//   anvil --fork-url https://rpc.soniclabs.com --port 8545
//   CORE_CHAIN=sonicMainnet npx hardhat run scripts/rehearse-core-fork.js --network localhost
//   (same with https://bsc-dataseed.bnbchain.org and CORE_CHAIN=bscMainnet)

const hre = require("hardhat");
const { ethers } = hre;
const { main: deployCore } = require("../deploy/core");
const { chains } = require("../deploy/chains");

const ERC20 = [
  "function balanceOf(address) view returns (uint256)",
  "function approve(address,uint256) returns (bool)",
  "function decimals() view returns (uint8)",
];

function assert(cond, msg) {
  if (!cond) throw new Error(`rehearsal: ${msg}`);
}

/** Give `to` `amount` of `token` by finding its balance mapping slot on the fork. */
async function fund(token, to, amount) {
  const t = new ethers.Contract(token, ERC20, ethers.provider);
  for (let slot = 0; slot < 30; slot++) {
    const key = ethers.keccak256(ethers.AbiCoder.defaultAbiCoder().encode(["address", "uint256"], [to, slot]));
    const before = await ethers.provider.send("eth_getStorageAt", [token, key, "latest"]);
    await ethers.provider.send("anvil_setStorageAt", [token, key, ethers.toBeHex(amount, 32)]);
    if ((await t.balanceOf(to)) === amount) return;
    await ethers.provider.send("anvil_setStorageAt", [token, key, before]);
  }
  // Namespaced-storage tokens: borrow from a real holder instead (FUND_FROM), impersonated on the fork.
  const holder = process.env.FUND_FROM;
  if (!holder) throw new Error(`could not find the balance slot of ${token}; set FUND_FROM to a holder`);
  await ethers.provider.send("anvil_impersonateAccount", [holder]);
  await ethers.provider.send("anvil_setBalance", [holder, "0x56BC75E2D63100000"]);
  const from = await ethers.getSigner(holder);
  const t2 = new ethers.Contract(token, ["function transfer(address,uint256) returns (bool)"], from);
  await (await t2.transfer(to, amount)).wait();
  if ((await t.balanceOf(to)) < amount) throw new Error(`funding ${to} from ${holder} failed`);
}

async function main() {
  const chainName = process.env.CORE_CHAIN;
  const cfg = chains[chainName];
  assert(cfg && cfg.core, `set CORE_CHAIN to a chain with a core block`);
  const out = await deployCore();
  const [, buyer, seller] = await ethers.getSigners();
  const feeWallet = cfg.core.feeRecipient;
  const symbol = Object.keys(cfg.core.p2p)[0];
  const token = new ethers.Contract(cfg.tokens[symbol], ERC20, ethers.provider);
  const dec = await token.decimals();
  const u = (n) => ethers.parseUnits(String(n), dec);
  console.log(`\nrehearsing with real ${symbol} (${await token.getAddress()}, ${dec} decimals)`);

  for (const who of [buyer, seller]) {
    await fund(await token.getAddress(), who.address, u(10000));
  }
  const feeBefore = await token.balanceOf(feeWallet);

  if (out.order) {
    const escrow = out.order.contract;
    const flat = BigInt(cfg.core.order[symbol].flat);
    const bps = BigInt(cfg.core.order[symbol].bps);
    await (await token.connect(buyer).approve(out.order.address, ethers.MaxUint256)).wait();
    const orderId = ethers.id("rehearsal-order");
    await (await escrow.connect(buyer).createOrder(orderId, seller.address, await token.getAddress(), u(100), u(10), 14, flat)).wait();
    const key = await escrow.orderKey(buyer.address, orderId);
    await (await escrow.connect(seller).markDelivered(key)).wait();
    const sellerBefore = await token.balanceOf(seller.address);
    await (await escrow.connect(buyer).confirmReceipt(key)).wait();
    const fee = (u(100) * bps) / 10000n;
    assert((await token.balanceOf(seller.address)) - sellerBefore === u(110) - fee, "seller payout");
    assert((await escrow.owed(await token.getAddress())) === 0n, "order owed not zero");
    console.log(`  order: seller got ${ethers.formatUnits(u(110) - fee, dec)}, fee ${ethers.formatUnits(fee + flat, dec)} to the fee wallet`);
  }

  // P2P: seller deposits, signs a voucher, buyer opens, marks paid, seller releases.
  const p2p = out.p2p.contract;
  const rate = cfg.core.p2p[symbol];
  await (await token.connect(seller).approve(out.p2p.address, ethers.MaxUint256)).wait();
  await (await p2p.connect(seller).deposit(await token.getAddress(), u(500))).wait();
  const { chainId } = await ethers.provider.getNetwork();
  const v = {
    seller: seller.address, token: await token.getAddress(), maxAmount: u(500), minPerTrade: 0n, maxPerTrade: 0n,
    deadline: BigInt(Math.floor(Date.now() / 1000) + 86400 * 365), nonce: ethers.id("rehearsal-voucher"),
  };
  const sig = await seller.signTypedData(
    { name: "TSAConnectP2PEscrow", version: "3", chainId, verifyingContract: out.p2p.address },
    { AdVoucher: [
      { name: "seller", type: "address" }, { name: "token", type: "address" }, { name: "maxAmount", type: "uint256" },
      { name: "minPerTrade", type: "uint256" }, { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
      { name: "nonce", type: "bytes32" },
    ] },
    v,
  );
  const tradeId = ethers.id("rehearsal-trade");
  await (await p2p.connect(buyer).openTrade(tradeId, v, sig, u(200))).wait();
  const tkey = await p2p.tradeKey(buyer.address, tradeId);
  await (await p2p.connect(buyer).markPaid(tkey)).wait();
  const buyerBefore = await token.balanceOf(buyer.address);
  await (await p2p.connect(seller).release(tkey)).wait();
  const pfee = (u(200) * BigInt(rate.platformBps + rate.gasBps)) / 10000n;
  assert((await token.balanceOf(buyer.address)) - buyerBefore === u(200) - pfee, "p2p buyer payout");
  await (await p2p.connect(seller).withdraw(await token.getAddress(), u(300))).wait();
  assert((await p2p.owed(await token.getAddress())) === 0n, "p2p owed not zero");
  console.log(`  p2p: buyer got ${ethers.formatUnits(u(200) - pfee, dec)}, fee ${ethers.formatUnits(pfee, dec)}; seller withdrew the rest`);

  const feeGained = (await token.balanceOf(feeWallet)) - feeBefore;
  console.log(`  fee wallet ${feeWallet} gained ${ethers.formatUnits(feeGained, dec)} ${symbol}`);
  console.log("REHEARSAL OK");
}

main().then(() => process.exit(0)).catch((e) => { console.error(e); process.exit(1); });
