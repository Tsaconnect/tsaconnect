// On-chain timeout-cancel verification for BOTH fundings. Temporarily lowers the
// payment window (owner-only) to the 5-min minimum, opens a pooled trade and a JIT
// trade, waits out the window, then cancelByTimeout each and asserts the refund
// branch: pooled -> returnedToPool=true (re-pooled to seller `available`);
// JIT -> returnedToPool=false (crypto returned to taker wallet). Restores the window.
//   DEPLOYER_PRIVATE_KEY=… npx hardhat run scripts/smoke-timeout-cancel-v2.js --network sonicTestnet
const { ethers } = require('hardhat');

const ESCROW = process.env.P2P_ESCROW_ADDRESS || '0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7';
const USDT = process.env.USDT_TOKEN_ADDRESS || '0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad';
const AMT = 1_000_000n;
const WINDOW = 300; // MIN_PAYMENT_WINDOW (5 min)
const WAIT_MS = (WINDOW + 15) * 1000;

const escrowAbi = [
  'function deposit(address,uint256) external',
  'function openTrade(bytes32, (address seller,address token,uint256 maxAmount,uint256 minPerTrade,uint256 maxPerTrade,uint256 deadline,bytes32 nonce), bytes, uint256) external',
  'function openTradeWithDeposit(bytes32,address,uint256,address) external',
  'function cancelByTimeout(bytes32) external',
  'function getTrade(bytes32) view returns (address,address,address,uint256,uint256,uint8,uint8,uint16,uint256,bytes32)',
  'function balanceOf(address,address) view returns (uint256 _available,uint256 _locked)',
  'function paymentWindow() view returns (uint40)',
  'function setPaymentWindow(uint40) external',
];
const erc20 = ['function mint(address,uint256) external', 'function approve(address,uint256) external returns(bool)', 'function balanceOf(address) view returns(uint256)'];
const STATUS = ['None', 'Pending', 'Paid', 'Released', 'Cancelled', 'Disputed', 'Resolved'];
const rid = () => ethers.hexlify(ethers.randomBytes(32));
const ok = (c, m) => { if (!c) throw new Error('ASSERT FAILED: ' + m); console.log('    ✓ ' + m); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  const provider = ethers.provider;
  const [deployer] = await ethers.getSigners();
  const escrow = new ethers.Contract(ESCROW, escrowAbi, deployer);
  const prevWindow = await escrow.paymentWindow();
  console.log('deployer', deployer.address, '| current paymentWindow', prevWindow.toString());

  console.log(`lowering paymentWindow -> ${WINDOW}s for the test…`);
  await (await escrow.setPaymentWindow(WINDOW)).wait();
  try {
    const mk = () => new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
    const sellerS = mk(), buyerS = mk(), takerB = mk(), merchantB = mk();
    for (const w of [sellerS, buyerS, takerB]) {
      await (await deployer.sendTransaction({ to: w.address, value: ethers.parseEther('0.2') })).wait();
    }
    await (await new ethers.Contract(USDT, erc20, deployer).mint(sellerS.address, AMT * 2n)).wait();
    await (await new ethers.Contract(USDT, erc20, deployer).mint(takerB.address, AMT * 2n)).wait();

    // POOLED trade
    await (await new ethers.Contract(USDT, erc20, sellerS).approve(ESCROW, AMT)).wait();
    await (await escrow.connect(sellerS).deposit(USDT, AMT)).wait();
    const net = await provider.getNetwork();
    const domain = { name: 'TSAConnectP2PEscrow', version: '2', chainId: Number(net.chainId), verifyingContract: ESCROW };
    const types = { AdVoucher: [
      { name: 'seller', type: 'address' }, { name: 'token', type: 'address' }, { name: 'maxAmount', type: 'uint256' },
      { name: 'minPerTrade', type: 'uint256' }, { name: 'maxPerTrade', type: 'uint256' }, { name: 'deadline', type: 'uint256' }, { name: 'nonce', type: 'bytes32' }] };
    const voucher = { seller: sellerS.address, token: USDT, maxAmount: AMT, minPerTrade: 0n, maxPerTrade: 0n,
      deadline: BigInt((await provider.getBlock('latest')).timestamp + 3600), nonce: rid() };
    const sig = await sellerS.signTypedData(domain, types, voucher);
    const idP = rid();
    await (await escrow.connect(buyerS).openTrade(idP, voucher, sig, AMT)).wait();
    ok(STATUS[(await escrow.getTrade(idP))[5]] === 'Pending', 'pooled trade opened (Pending)');

    // JIT trade
    await (await new ethers.Contract(USDT, erc20, takerB).approve(ESCROW, AMT)).wait();
    const takerBefore = await new ethers.Contract(USDT, erc20, provider).balanceOf(takerB.address);
    const idJ = rid();
    await (await escrow.connect(takerB).openTradeWithDeposit(idJ, USDT, AMT, merchantB.address)).wait();
    ok(STATUS[(await escrow.getTrade(idJ))[5]] === 'Pending', 'JIT trade opened (Pending)');

    console.log(`waiting ${WAIT_MS / 1000}s for the payment window to elapse…`);
    await sleep(WAIT_MS);

    // Cancel both (permissionless after timeout)
    await (await escrow.connect(buyerS).cancelByTimeout(idP)).wait();
    ok(STATUS[(await escrow.getTrade(idP))[5]] === 'Cancelled', 'pooled cancelByTimeout -> Cancelled');
    const [availAfter] = await escrow.balanceOf(sellerS.address, USDT);
    ok(availAfter === AMT, `pooled refund re-pooled to seller available=${availAfter} (returnedToPool semantics)`);

    await (await escrow.connect(takerB).cancelByTimeout(idJ)).wait();
    ok(STATUS[(await escrow.getTrade(idJ))[5]] === 'Cancelled', 'JIT cancelByTimeout -> Cancelled');
    const takerAfter = await new ethers.Contract(USDT, erc20, provider).balanceOf(takerB.address);
    ok(takerAfter === takerBefore, `JIT refund returned to taker wallet (${takerAfter} == pre-deposit ${takerBefore})`);

    console.log('\n✓✓✓ TIMEOUT-CANCEL verified for both fundings (pooled re-pool + JIT wallet refund).');
  } finally {
    console.log(`restoring paymentWindow -> ${prevWindow}…`);
    await (await escrow.setPaymentWindow(prevWindow)).wait();
    console.log('paymentWindow restored.');
  }
}

main().then(() => process.exit(0)).catch((e) => { console.error('\n✗ FAILED:', e.message || e); process.exit(1); });
