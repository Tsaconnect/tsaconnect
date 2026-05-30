// FULL-STACK E2E: drives the real Go API (http://localhost:8090) + the deployed
// P2PEscrowV2 on Sonic testnet + Postgres, for BOTH flows end to end:
//   sell-ad: deposit -> create voucher ad -> sign -> buyer open -> markPaid -> release
//   buy-ad : create buy ad -> taker approve+openTradeWithDeposit -> markPaid -> release
// The API only PREPARES unsigned txs; this script signs+broadcasts with funded
// ephemeral wallets (gas + USDT funded from DEPLOYER_PRIVATE_KEY) and calls confirm.
//
// Prereqs (set up by the caller): API running on :8090 against an isolated DB with
// a verified merchant (id 1111…) + verified user (id 2222…) already inserted.
//   node scripts/smoke-e2e-api.js
require('dotenv').config();
const crypto = require('crypto');
const { ethers } = require('ethers');

const API = 'http://localhost:8090/api';
const SECRET = 'dev-secret-key-change-in-production';
const RPC = process.env.SONIC_TESTNET_RPC || 'https://rpc.testnet.soniclabs.com';
const USDT = '0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad';
const MERCHANT_ID = '11111111-1111-1111-1111-111111111111';
const USER_ID = '22222222-2222-2222-2222-222222222222';

const b64 = (b) => Buffer.from(b).toString('base64').replace(/=/g, '').replace(/\+/g, '-').replace(/\//g, '_');
const jwt = (id) => {
  const h = b64(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const p = b64(JSON.stringify({ userId: id, exp: 4102444800 }));
  return `${h}.${p}.${b64(crypto.createHmac('sha256', SECRET).update(`${h}.${p}`).digest())}`;
};
const ADMIN_ID = '33333333-3333-3333-3333-333333333333';
const MERCHANT_JWT = jwt(MERCHANT_ID);
const USER_JWT = jwt(USER_ID);
const ADMIN_JWT = jwt(ADMIN_ID);

const erc20 = ['function mint(address,uint256) external'];

async function api(method, path, token, body) {
  const res = await fetch(API + path, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json;
  try { json = JSON.parse(text); } catch { json = { raw: text }; }
  if (res.status >= 300) throw new Error(`${method} ${path} -> HTTP ${res.status}: ${JSON.stringify(json).slice(0, 300)}`);
  return json.data ?? json;
}

let provider;
const hex0x = (s) => (typeof s === 'string' && s.length > 0 && !s.startsWith('0x') ? '0x' + s : s);
async function broadcast(wallet, u, label) {
  const to = hex0x(u.to || u.To);
  const data = hex0x(u.data || u.Data || u.input);
  const value = u.value || u.Value || 0;
  if (!to || !data) throw new Error(`${label}: unsignedTx missing to/data: ${JSON.stringify(u).slice(0, 200)}`);
  const sent = await wallet.sendTransaction({ to, data, value: value ? BigInt(value) : 0n });
  const r = await sent.wait();
  if (!r || r.status !== 1) throw new Error(`${label}: tx reverted ${sent.hash}`);
  return sent.hash;
}

const ok = (m) => console.log('    ✓ ' + m);

async function main() {
  provider = new ethers.JsonRpcProvider(RPC);
  const pk = process.env.DEPLOYER_PRIVATE_KEY;
  const deployer = new ethers.Wallet(pk.startsWith('0x') ? pk : '0x' + pk, provider);
  console.log('deployer:', deployer.address, 'bal', ethers.formatEther(await provider.getBalance(deployer.address)));

  // Ephemeral on-chain identities (DB user is identified by JWT; addresses are passed per-request).
  const merchantW = new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
  const userW = new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
  console.log('merchantW:', merchantW.address, '\nuserW    :', userW.address);

  console.log('funding gas + minting USDT…');
  for (const w of [merchantW, userW]) {
    await (await deployer.sendTransaction({ to: w.address, value: ethers.parseEther('0.3') })).wait();
    await (await new ethers.Contract(USDT, erc20, deployer).mint(w.address, 5_000_000n)).wait();
  }

  // Merchant payment method (NGN) — owned by the merchant, used by both ads.
  const pm = await api('POST', '/p2p/payment-methods', MERCHANT_JWT, {
    provider: 'bank_transfer', country: 'NG', currency: 'NGN',
    bankName: 'Access Bank', bankCode: '044', accountNumber: '0123456789', accountName: 'Smoke Merchant', isDefault: true,
  });
  const PM = pm.id;
  ok('payment method ' + PM);

  // ───────────────── SELL-AD FLOW (merchant sells, user buys) ─────────────────
  console.log('\n══ SELL-AD (PooledFromMaker) ══');
  // 1) deposit 2 USDT into the pool (approve -> deposit -> confirm)
  const approve1 = await api('POST', '/p2p/deposits/approve', MERCHANT_JWT, { token: 'USDT', amount: '2000000', walletAddress: merchantW.address });
  await broadcast(merchantW, approve1.unsignedTx, 'sell.approve');
  const dep = await api('POST', '/p2p/deposits', MERCHANT_JWT, { token: 'USDT', amount: '2000000', walletAddress: merchantW.address });
  const depTx = await broadcast(merchantW, dep.unsignedTx, 'sell.deposit');
  await api('POST', '/p2p/deposits/confirm', MERCHANT_JWT, { txHash: depTx, token: 'USDT', amount: '2000000', walletAddress: merchantW.address });
  ok('deposited 2 USDT to pool');
  // 2) create sell ad (checks on-chain available >= amount) + sign voucher
  const adRes = await api('POST', '/p2p/ads', MERCHANT_JWT, {
    side: 'sell', token: 'USDT', tokenAmount: '2000000', fiatCurrency: 'NGN', rate: '1500', rateType: 'fixed',
    minLimit: '100', maxLimit: '5000', paymentMethodIds: [PM], terms: 'E2E', sellerAddress: merchantW.address,
  });
  const ad = adRes.ad; const td = adRes.typedData;
  ok('sell ad ' + ad.id + ' status=' + ad.status);
  const sig = await merchantW.signTypedData(
    { name: td.domain.name, version: td.domain.version, chainId: td.domain.chainId, verifyingContract: td.domain.verifyingContract },
    { AdVoucher: td.types.AdVoucher },
    td.message,
  );
  const signed = await api('POST', `/p2p/ads/${ad.id}/sign`, MERCHANT_JWT, { signature: sig, sellerAddress: merchantW.address });
  if (signed.status !== 'active') throw new Error('sign: ad not active, got ' + signed.status);
  ok('voucher signed -> ad active (ecrecover passed on live domain)');
  // 3) user opens a trade (₦1500 = 1 USDT)
  const openRes = await api('POST', `/p2p/ads/${ad.id}/trades`, USER_JWT, { fiatAmount: '1500', paymentMethodId: PM, buyerAddress: userW.address });
  const trade1 = openRes.trade;
  const openTx = await broadcast(userW, openRes.unsignedTx, 'sell.openTrade');
  await api('POST', `/p2p/trades/${trade1.id}/open/confirm`, USER_JWT, { txHash: openTx, callerAddress: userW.address });
  ok('trade ' + trade1.id + ' opened + confirmed (pending_payment)');
  // 4) user marks paid, merchant releases
  await api('POST', `/p2p/trades/${trade1.id}/mark-paid`, USER_JWT, { proofImageUrl: 'https://example.test/proof.jpg' });
  ok('marked paid');
  const rel = await api('POST', `/p2p/trades/${trade1.id}/release`, MERCHANT_JWT, { sellerAddress: merchantW.address });
  const relTx = await broadcast(merchantW, rel.unsignedTx, 'sell.release');
  await api('POST', `/p2p/trades/${trade1.id}/release/confirm`, MERCHANT_JWT, { txHash: relTx });
  const t1 = await api('GET', `/p2p/trades/${trade1.id}`, MERCHANT_JWT);
  if ((t1.trade?.status ?? t1.status) !== 'released') throw new Error('sell: final status ' + JSON.stringify(t1).slice(0, 200));
  ok('SELL-AD released ✓ (full stack: API+chain+DB)');

  // ───────────────── BUY-AD FLOW (merchant buys, user sells) ─────────────────
  console.log('\n══ BUY-AD (DepositedByTaker) ══');
  const buyAdRes = await api('POST', '/p2p/ads', MERCHANT_JWT, {
    side: 'buy', token: 'USDT', tokenAmount: '2000000', fiatCurrency: 'NGN', rate: '1500', rateType: 'fixed',
    minLimit: '100', maxLimit: '5000', paymentMethodIds: [PM], terms: 'E2E buy', sellerAddress: merchantW.address,
  });
  const buyAd = buyAdRes.ad;
  ok('buy ad ' + buyAd.id + ' status=' + buyAd.status);
  // user approves exactly 1 USDT, then opens (openTradeWithDeposit)
  const approve2 = await api('POST', '/p2p/deposits/approve', USER_JWT, { token: 'USDT', amount: '1000000', walletAddress: userW.address });
  await broadcast(userW, approve2.unsignedTx, 'buy.approve');
  const openRes2 = await api('POST', `/p2p/ads/${buyAd.id}/trades`, USER_JWT, { tokenAmount: '1000000', paymentMethodId: PM, sellerAddress: userW.address });
  const trade2 = openRes2.trade;
  const openTx2 = await broadcast(userW, openRes2.unsignedTx, 'buy.openTradeWithDeposit');
  await api('POST', `/p2p/trades/${trade2.id}/open/confirm`, USER_JWT, { txHash: openTx2, callerAddress: userW.address });
  ok('trade ' + trade2.id + ' opened via deposit (seller=user, buyer=merchant)');
  // merchant marks paid (fiat payer), user releases
  await api('POST', `/p2p/trades/${trade2.id}/mark-paid`, MERCHANT_JWT, { proofImageUrl: 'https://example.test/proof2.jpg' });
  ok('merchant marked paid');
  const rel2 = await api('POST', `/p2p/trades/${trade2.id}/release`, USER_JWT, { sellerAddress: userW.address });
  const relTx2 = await broadcast(userW, rel2.unsignedTx, 'buy.release');
  await api('POST', `/p2p/trades/${trade2.id}/release/confirm`, USER_JWT, { txHash: relTx2 });
  const t2 = await api('GET', `/p2p/trades/${trade2.id}`, USER_JWT);
  if ((t2.trade?.status ?? t2.status) !== 'released') throw new Error('buy: final status ' + JSON.stringify(t2).slice(0, 200));
  ok('BUY-AD released ✓ (full stack: API+chain+DB)');

  // ───────────────── DISPUTE FLOW (sell-ad, resolve to seller / refund) ─────────────────
  console.log('\n══ DISPUTE → ADMIN RESOLVE (refund to seller) ══');
  // Reuse merchant's leftover pool (1 USDT still available after the sell flow).
  const adD = await api('POST', '/p2p/ads', MERCHANT_JWT, {
    side: 'sell', token: 'USDT', tokenAmount: '1000000', fiatCurrency: 'NGN', rate: '1500', rateType: 'fixed',
    minLimit: '100', maxLimit: '5000', paymentMethodIds: [PM], terms: 'E2E dispute', sellerAddress: merchantW.address,
  });
  const adv = adD.typedData;
  const sigD = await merchantW.signTypedData(
    { name: adv.domain.name, version: adv.domain.version, chainId: adv.domain.chainId, verifyingContract: adv.domain.verifyingContract },
    { AdVoucher: adv.types.AdVoucher }, adv.message,
  );
  await api('POST', `/p2p/ads/${adD.ad.id}/sign`, MERCHANT_JWT, { signature: sigD, sellerAddress: merchantW.address });
  const openD = await api('POST', `/p2p/ads/${adD.ad.id}/trades`, USER_JWT, { fiatAmount: '1500', paymentMethodId: PM, buyerAddress: userW.address });
  const tradeD = openD.trade;
  const openDTx = await broadcast(userW, openD.unsignedTx, 'dispute.openTrade');
  await api('POST', `/p2p/trades/${tradeD.id}/open/confirm`, USER_JWT, { txHash: openDTx, callerAddress: userW.address });
  ok('trade ' + tradeD.id + ' opened');
  // buyer raises a dispute
  const disp = await api('POST', `/p2p/trades/${tradeD.id}/dispute`, USER_JWT, { reason: 'Paid but seller unresponsive', callerAddress: userW.address });
  const dispTx = await broadcast(userW, disp.unsignedTx, 'raiseDispute');
  await api('POST', `/p2p/trades/${tradeD.id}/dispute/confirm`, USER_JWT, { txHash: dispTx });
  ok('dispute raised + confirmed (disputed)');
  // admin (owner EOA) resolves -> refund to seller (re-pools to available)
  const resolve = await api('POST', `/admin/p2p/disputes/${tradeD.id}/resolve`, ADMIN_JWT, { refundBuyer: false, note: 'Insufficient evidence', adminAddress: deployer.address });
  const resolveTx = await broadcast(deployer, resolve.unsignedTx, 'adminResolve');
  await api('POST', `/admin/p2p/disputes/${tradeD.id}/resolve/confirm`, ADMIN_JWT, { txHash: resolveTx });
  const tD = await api('GET', `/p2p/trades/${tradeD.id}`, MERCHANT_JWT);
  if ((tD.trade?.status ?? tD.status) !== 'resolved') throw new Error('dispute: final status ' + JSON.stringify(tD).slice(0, 200));
  ok('DISPUTE resolved ✓ (raiseDispute + adminResolve, full stack)');

  console.log('\n✓✓✓ FULL-STACK E2E PASSED — sell, buy, and dispute flows work through the real API + chain + DB.');
}

main().then(() => process.exit(0)).catch((e) => { console.error('\n✗ E2E FAILED:', e.message); process.exit(1); });
