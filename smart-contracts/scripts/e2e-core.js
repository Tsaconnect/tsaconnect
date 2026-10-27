// scripts/e2e-core.js
//
// End-to-end run of the audit-ready core set (FeeConfig, OrderEscrow, P2PTradeEscrow) with real
// stablecoins: every order and P2P path that does not need days to pass, checking every balance.
//
// Fork (deploys a fresh set first; sends nothing to a live chain):
//   anvil --fork-url https://rpc.soniclabs.com --port 8545
//   CORE_CHAIN=sonicMainnet FUND_FROM=<a USDT holder> npx hardhat run scripts/e2e-core.js --network localhost
//
// Live (against the dev set deployed with CORE_DEV=1; small real amounts):
//   E2E_BUYER_KEY=0x… E2E_SELLER_KEY=0x… npx hardhat run scripts/e2e-core.js --network sonicMainnet
//   - The deployer (DEPLOYER_PRIVATE_KEY) must be the set's admin: it pauses, resolves, relays the
//     gasless calls and, for the run, receives the fees (the recipient is restored at the end).
//   - Buyer and seller are two throwaway wallets, each with a little gas. E2E_UNIT (default 1)
//     scales every amount: the buyer needs 7 units plus the flat order fee 4 times, the seller 6
//     units of E2E_TOKEN (default USDT). At E2E_UNIT=0.1 on Sonic that is 1.1 and 0.6 USDT.
//     Refunds and withdrawals return most of it, and every fee goes to the deployer for the run.
//
// Not covered here, because they need hours or days to pass: payment and auto-release timeouts
// (30 minutes), the delivery timeout (1+ days), the 7-day confirm window and the 14/60-day dispute
// backstops. The Hardhat suites in test/core cover them with time travel.

const hre = require("hardhat");
const { ethers } = hre;
const { main: deployCore } = require("../deploy/core");
const { chains } = require("../deploy/chains");
const { readLedger } = require("../deploy/lib");

const P2P_FQN = "contracts/core/P2PTradeEscrow.sol:P2PTradeEscrow";
const ERC20 = [
  "function balanceOf(address) view returns (uint256)",
  "function approve(address,uint256) returns (bool)",
  "function allowance(address,address) view returns (uint256)",
  "function decimals() view returns (uint8)",
];
const VOUCHER_TYPES = {
  AdVoucher: [
    { name: "seller", type: "address" }, { name: "token", type: "address" }, { name: "maxAmount", type: "uint256" },
    { name: "minPerTrade", type: "uint256" }, { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
    { name: "nonce", type: "bytes32" }, { name: "taker", type: "address" },
  ],
};
const FORWARD_TYPES = {
  ForwardRequest: [
    { name: "from", type: "address" }, { name: "to", type: "address" }, { name: "value", type: "uint256" },
    { name: "gas", type: "uint256" }, { name: "nonce", type: "uint256" }, { name: "deadline", type: "uint48" },
    { name: "data", type: "bytes" },
  ],
};

function assert(cond, msg) {
  if (!cond) throw new Error(`e2e: ${msg}`);
}

const send = async (txPromise) => (await txPromise).wait();

/** Expect a call to revert with `contract`'s named custom error, without sending it. */
async function expectRevert(contract, callPromise, errorName) {
  try {
    await callPromise;
  } catch (e) {
    const data = e.data ?? e.info?.error?.data ?? e.error?.data;
    let name = e.revert?.name;
    try {
      name ??= contract.interface.parseError(data)?.name;
    } catch {}
    assert(name === errorName, `expected ${errorName}, got ${name || e.shortMessage || e.message}`);
    return;
  }
  throw new Error(`e2e: expected ${errorName}, but the call succeeded`);
}

/** Fork only: give `to` `amount` of `token` by finding its balance slot, else from FUND_FROM. */
async function fundOnFork(token, to, amount) {
  const t = new ethers.Contract(token, ERC20, ethers.provider);
  for (let slot = 0; slot < 30; slot++) {
    const key = ethers.keccak256(ethers.AbiCoder.defaultAbiCoder().encode(["address", "uint256"], [to, slot]));
    const before = await ethers.provider.send("eth_getStorageAt", [token, key, "latest"]);
    await ethers.provider.send("anvil_setStorageAt", [token, key, ethers.toBeHex(amount, 32)]);
    if ((await t.balanceOf(to)) === amount) return;
    await ethers.provider.send("anvil_setStorageAt", [token, key, before]);
  }
  const holder = process.env.FUND_FROM;
  if (!holder) throw new Error(`could not find the balance slot of ${token}; set FUND_FROM to a holder`);
  const from = await impersonate(holder);
  await send(new ethers.Contract(token, ["function transfer(address,uint256) returns (bool)"], from).transfer(to, amount));
}

async function impersonate(addr) {
  await ethers.provider.send("anvil_impersonateAccount", [addr]);
  await ethers.provider.send("anvil_setBalance", [addr, "0x56BC75E2D63100000"]);
  return ethers.getSigner(addr);
}

/** The contracts and the four actors, for a fork (fresh deploy) or a live network (dev set). */
async function setup() {
  const fork = hre.network.name === "localhost";
  const chainName = fork ? process.env.CORE_CHAIN : hre.network.name;
  const cfg = chains[chainName];
  assert(cfg && cfg.core, `no core config for ${chainName}${fork ? " (set CORE_CHAIN)" : ""}`);

  let addrs;
  let buyer;
  let seller;
  const [deployer] = await ethers.getSigners();
  if (fork) {
    const out = await deployCore();
    addrs = { feeConfig: out.feeConfig.address, order: out.order?.address, p2p: out.p2p.address };
    [, buyer, seller] = await ethers.getSigners();
  } else {
    const ledger = readLedger(hre.network.name);
    const proxy = (name) => ledger[name]?.proxy;
    addrs = { feeConfig: proxy("CoreDevFeeConfig"), order: proxy("CoreDevOrderEscrow"), p2p: proxy("CoreDevP2PTradeEscrow") };
    assert(addrs.feeConfig && addrs.p2p, `no CoreDev* set in deployments/${hre.network.name}.json; deploy one with CORE_DEV=1`);
    for (const v of ["E2E_BUYER_KEY", "E2E_SELLER_KEY"]) assert(process.env[v], `set ${v}`);
    buyer = new ethers.Wallet(process.env.E2E_BUYER_KEY, ethers.provider);
    seller = new ethers.Wallet(process.env.E2E_SELLER_KEY, ethers.provider);
  }

  const feeConfig = await ethers.getContractAt("FeeConfig", addrs.feeConfig);
  const order = addrs.order ? await ethers.getContractAt("OrderEscrow", addrs.order) : null;
  const p2p = await ethers.getContractAt(P2P_FQN, addrs.p2p);
  const adminAddr = await feeConfig.defaultAdmin();
  const admin = fork ? await impersonate(adminAddr) : deployer;
  assert(admin.address === adminAddr, `the deployer ${deployer.address} is not the set's admin ${adminAddr}`);

  const symbol = process.env.E2E_TOKEN || Object.keys(cfg.core.p2p)[0];
  const token = new ethers.Contract(cfg.tokens[symbol], ERC20, ethers.provider);
  const dec = await token.decimals();
  const u = (n) => ethers.parseUnits(String(n), dec);
  // Steps are written in units of E2E_UNIT tokens (n has at most one decimal); bigint math keeps
  // small units such as 0.01 exact.
  const unit = u(process.env.E2E_UNIT || "1");
  const amt = (n) => (u(n) * unit) / u(1);

  if (fork) {
    for (const who of [buyer, seller]) await fundOnFork(await token.getAddress(), who.address, amt(100));
  } else {
    // The buyer also pays the flat order fee (it does not scale) on each of the 4 orders.
    const flat = addrs.order ? (await feeConfig.rateFor(ethers.encodeBytes32String("order"), cfg.tokens[symbol])).flatFee : 0n;
    const names = [["buyer", buyer, amt(7) + 4n * flat], ["seller", seller, amt(6)]];
    for (const [name, w, need] of names) {
      const have = await token.balanceOf(w.address);
      assert(have >= need, `${name} ${w.address} holds ${ethers.formatUnits(have, dec)} ${symbol}, needs ${ethers.formatUnits(need, dec)}`);
      assert((await ethers.provider.getBalance(w.address)) > 0n, `${name} ${w.address} has no gas`);
    }
  }
  return { fork, cfg, feeConfig, order, p2p, admin, buyer, seller, token, symbol, dec, amt };
}

async function main() {
  const c = await setup();
  const { feeConfig, order, p2p, admin, buyer, seller, token, symbol, dec, amt } = c;
  const T = await token.getAddress();
  const fmt = (v) => `${ethers.formatUnits(v, dec)} ${symbol}`;
  const bal = (a) => token.balanceOf(a.address ?? a);
  const { chainId } = await ethers.provider.getNetwork();
  const run = Date.now().toString(36); // fresh ids every run
  const id = (s) => ethers.id(`e2e:${run}:${s}`);
  console.log(`\n${c.fork ? "fork" : "LIVE"} e2e on ${hre.network.name} with ${symbol} ${T}`);
  console.log(`  admin ${admin.address}  buyer ${buyer.address}  seller ${seller.address}`);

  // FeeConfig: only CONFIG_ROLE writes; the admin points fees at itself for the run so every fee is
  // measured exactly, then restores the recipient.
  const originalRecipient = await feeConfig.feeRecipient();
  await expectRevert(feeConfig, feeConfig.connect(buyer).setFeeRecipient.staticCall(buyer.address), "AccessControlUnauthorizedAccount");
  await send(feeConfig.connect(admin).setFeeRecipient(admin.address));
  assert((await feeConfig.feeRecipient()) === admin.address, "fee recipient not switched");
  console.log("  ✔ FeeConfig: only the admin sets the fee recipient");

  const feeTaken = async (fn) => {
    const before = await bal(admin);
    await fn();
    return (await bal(admin)) - before;
  };

  try {
    if (order) await orderFlows();
    await p2pFlows();
  } finally {
    await send(feeConfig.connect(admin).setFeeRecipient(originalRecipient));
    console.log(`  fee recipient restored to ${originalRecipient}`);
  }

  if (order) assert((await order.owed(T)) === 0n, "OrderEscrow still owes something");
  assert((await p2p.owed(T)) === 0n, "P2PTradeEscrow still owes something");
  console.log("E2E OK: both escrows owe 0");

  async function orderFlows() {
    const O = await order.getAddress();
    const ORDER = ethers.encodeBytes32String("order");
    const rate = await feeConfig.rateFor(ORDER, T);
    const limits = await feeConfig.limitsFor(T);
    const fee = (product) => (product * BigInt(rate.bps)) / 10000n;
    const flat = rate.flatFee;
    if ((await token.allowance(buyer.address, O)) < amt(100)) await send(token.connect(buyer).approve(O, ethers.MaxUint256));
    const create = (name, product, shipping, signer = buyer) =>
      order.connect(signer).createOrder(id(name), seller.address, T, product, shipping, 14, flat);
    const keyOf = (name) => order.orderKey(buyer.address, id(name));

    // 1. create → deliver → (pause: settling still works) → confirm.
    let taken = await feeTaken(() => send(create("o1", amt(2), amt(0.5))));
    assert(taken === flat, `flat fee ${fmt(taken)}, expected ${fmt(flat)}`);
    const k1 = await keyOf("o1");
    await send(order.connect(seller).markDelivered(k1));
    await send(order.connect(admin).pause());
    await expectRevert(order, order.connect(buyer).createOrder.staticCall(id("paused"), seller.address, T, amt(1), 0n, 14, flat), "EnforcedPause");
    const sellerBefore = await bal(seller);
    taken = await feeTaken(() => send(order.connect(buyer).confirmReceipt(k1)));
    assert(taken === fee(amt(2)), `merchant fee ${fmt(taken)}, expected ${fmt(fee(amt(2)))}`);
    assert((await bal(seller)) - sellerBefore === amt(2.5) - fee(amt(2)), "seller payout on confirm");
    await send(order.connect(admin).unpause());
    console.log(`  ✔ order: paid, delivered, confirmed while paused; seller got ${fmt(amt(2.5) - fee(amt(2)))}, fees ${fmt(flat + fee(amt(2)))}`);

    // 2. seller cancels before delivery → buyer refunded in full (the flat fee is kept).
    await send(create("o2", amt(1), 0n));
    let buyerBefore = await bal(buyer);
    await send(order.connect(seller).cancel(await keyOf("o2")));
    assert((await bal(buyer)) - buyerBefore === amt(1), "refund on seller cancel");
    console.log(`  ✔ order: seller cancel refunds the buyer ${fmt(amt(1))}`);

    // 3. delivered → buyer disputes → resolver splits half and half.
    await send(create("o3", amt(2), 0n));
    const k3 = await keyOf("o3");
    await send(order.connect(seller).markDelivered(k3));
    await send(order.connect(buyer).dispute(k3));
    buyerBefore = await bal(buyer);
    const sellerBefore3 = await bal(seller);
    taken = await feeTaken(() => send(order.connect(admin).resolve(k3, amt(1))));
    assert((await bal(buyer)) - buyerBefore === amt(1), "resolve: buyer share");
    assert(taken === fee(amt(1)), `resolve fee ${fmt(taken)}, expected ${fmt(fee(amt(1)))}`);
    assert((await bal(seller)) - sellerBefore3 === amt(1) - fee(amt(1)), "resolve: seller share");
    console.log(`  ✔ order: dispute resolved 50/50, fee only on the seller's half`);

    // 4. limits and the flat-fee guard refuse before anything moves.
    await expectRevert(order, order.connect(buyer).createOrder.staticCall(id("big"), seller.address, T, limits.maxPerOrder + 1n, 0n, 14, flat), "OrderAboveLimit");
    if (flat > 0n) {
      await expectRevert(order, order.connect(buyer).createOrder.staticCall(id("flat"), seller.address, T, amt(1), 0n, 14, flat - 1n), "FlatFeeAboveMax");
    }
    console.log("  ✔ order: per-order limit and max flat fee enforced");

    // 5. gasless: the buyer signs, the admin relays; the order is the buyer's, then cancelled.
    if (await gasless(order, buyer, "createOrder", [id("o4"), seller.address, T, amt(1), 0n, 14, flat])) {
      const o4 = await order.getOrder(await keyOf("o4"));
      assert(o4.buyer === buyer.address, "gasless order not credited to the buyer");
      await send(order.connect(seller).cancel(await keyOf("o4")));
      console.log("  ✔ order: created gaslessly through the forwarder, buyer recorded correctly");
    }
  }

  async function p2pFlows() {
    const P = await p2p.getAddress();
    if ((await token.allowance(seller.address, P)) < amt(100)) await send(token.connect(seller).approve(P, ethers.MaxUint256));
    const domain = { name: "TSAConnectP2PEscrow", version: "3", chainId, verifyingContract: P };
    const v = {
      seller: seller.address, token: T, maxAmount: amt(4), minPerTrade: 0n, maxPerTrade: 0n,
      deadline: BigInt(Math.floor(Date.now() / 1000) + 3600), nonce: id("voucher"), taker: ethers.ZeroAddress,
    };
    const sig = await seller.signTypedData(domain, VOUCHER_TYPES, v);
    const open = async (name, amount) => {
      await send(p2p.connect(buyer).openTrade(id(name), v, sig, amount));
      return p2p.tradeKey(buyer.address, id(name));
    };
    const feeOf = async (key) => {
      const t = await p2p.getTrade(key);
      return (t.amount * (BigInt(t.platformFeeBps) + BigInt(t.gasFeeBps))) / 10000n;
    };

    // 1. deposit → signed ad → open → mark paid → seller releases.
    await send(p2p.connect(seller).deposit(T, amt(4)));
    const k1 = await open("t1", amt(1.5));
    await send(p2p.connect(buyer).markPaid(k1));
    let buyerBefore = await bal(buyer);
    const f1 = await feeOf(k1);
    let taken = await feeTaken(() => send(p2p.connect(seller).release(k1)));
    assert(taken === f1 && (await bal(buyer)) - buyerBefore === amt(1.5) - f1, "p2p release payout");
    console.log(`  ✔ p2p: pooled trade released; buyer got ${fmt(amt(1.5) - f1)}, fee ${fmt(f1)}`);

    // 2. the seller's named releaser releases.
    await send(p2p.connect(seller).setReleaser(admin.address));
    const k2 = await open("t2", amt(0.5));
    await send(p2p.connect(buyer).markPaid(k2));
    await expectRevert(p2p, p2p.connect(buyer).release.staticCall(k2), "NotReleaser");
    await send(p2p.connect(admin).release(k2));
    await send(p2p.connect(seller).setReleaser(ethers.ZeroAddress));
    console.log("  ✔ p2p: the seller's releaser releases; nobody else can");

    // 3. dispute → resolver returns the crypto to the seller's pool.
    const k3 = await open("t3", amt(1));
    await send(p2p.connect(buyer).markPaid(k3));
    await send(p2p.connect(seller).raiseDispute(k3));
    const [availBefore] = await p2p.balanceOf(seller.address, T);
    await send(p2p.connect(admin).adminResolve(k3, false));
    const [availAfter] = await p2p.balanceOf(seller.address, T);
    assert(availAfter - availBefore === amt(1), "resolve to seller: pool not restored");
    console.log("  ✔ p2p: dispute resolved back to the seller's pool");

    // 4. gasless openTrade through the forwarder, released by the seller.
    if (await gasless(p2p, buyer, "openTrade", [id("t4"), v, sig, amt(0.5)])) {
      const k4 = await p2p.tradeKey(buyer.address, id("t4"));
      assert((await p2p.getTrade(k4)).buyer === buyer.address, "gasless trade not credited to the buyer");
      await send(p2p.connect(buyer).markPaid(k4));
      await send(p2p.connect(seller).release(k4));
      console.log("  ✔ p2p: trade opened gaslessly through the forwarder");
    }

    // 5. a cancelled ad can no longer be traded.
    await send(p2p.connect(seller).cancelVoucher(v));
    await expectRevert(p2p, p2p.connect(buyer).openTrade.staticCall(id("t5"), v, sig, amt(0.1)), "VoucherCancelledErr");
    console.log("  ✔ p2p: cancelled ad refused");

    // 6. buy-ad: the seller sells into the buyer's buy-ad, naming the admin as this trade's releaser.
    const k6Id = id("t6");
    await send(p2p.connect(seller).openTradeWithDeposit(k6Id, T, amt(1), buyer.address, admin.address));
    const k6 = await p2p.tradeKey(seller.address, k6Id);
    await send(p2p.connect(buyer).markPaid(k6));
    assert((await p2p.getTrade(k6)).autoReleaseAt === 0n, "buy-ad trade armed an auto-release");
    buyerBefore = await bal(buyer);
    const f6 = await feeOf(k6);
    taken = await feeTaken(() => send(p2p.connect(admin).release(k6)));
    assert(taken === f6 && (await bal(buyer)) - buyerBefore === amt(1) - f6, "buy-ad payout");
    console.log("  ✔ p2p: buy-ad trade released by the releaser the user named");

    // 7. the seller withdraws the rest of the pool.
    const [rest, locked] = await p2p.balanceOf(seller.address, T);
    assert(locked === 0n, "seller still has locked funds");
    if (rest > 0n) await send(p2p.connect(seller).withdraw(T, rest));
    console.log(`  ✔ p2p: seller withdrew the remaining ${fmt(rest)}`);
  }

  /** Relay `method(args)` on `target` as `from` through the escrow's forwarder; false if none. */
  async function gasless(target, from, method, args) {
    const fwdAddr = await target.trustedForwarder();
    if (fwdAddr === ethers.ZeroAddress) {
      console.log(`  - ${method}: no forwarder on this chain, gasless path skipped`);
      return false;
    }
    const fwd = await ethers.getContractAt("TSAForwarder", fwdAddr);
    const [, name, version] = await fwd.eip712Domain();
    const request = {
      from: from.address, to: await target.getAddress(), value: 0n, gas: 500_000n,
      nonce: await fwd.nonces(from.address), deadline: Math.floor(Date.now() / 1000) + 300,
      data: target.interface.encodeFunctionData(method, args),
    };
    const signature = await from.signTypedData({ name, version, chainId, verifyingContract: fwdAddr }, FORWARD_TYPES, request);
    await send(fwd.connect(admin).execute({ ...request, signature }));
    return true;
  }
}

main().then(() => process.exit(0)).catch((e) => { console.error(e); process.exit(1); });
