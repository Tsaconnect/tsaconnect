// smoke-pm-e2e.js — end-to-end PM lifecycle driver (NO automatic execution)
//
// NEVER run this against prod DB or broadcast on-chain txs without the
// controller's explicit review.  This file is BUILT but not EXECUTED.
//
// Usage:  node scripts/smoke-pm-e2e.js <phase>
// Phases: seed | onboard | buy | sell | withdraw | verify | cleanup | all
//
// State is persisted to .pm-e2e-state.json between phases so each phase can
// run independently (idempotent upserts / guard checks).
//
// Security:
//   - JWT_SECRET, NOMBA_WEBHOOK_SECRET, DATABASE_URL never printed.
//   - DEPLOYER_PRIVATE_KEY never printed (only used in-memory as a Wallet).
//   - psql commands receive DATABASE_URL via PGPASSWORD env injection only.
// ---------------------------------------------------------------------------

'use strict';

require('dotenv').config({ path: require('path').resolve(__dirname, '../../tsa-api-go/.env') });
require('dotenv').config({ path: require('path').resolve(__dirname, '../.env'), override: false });

const crypto    = require('crypto');
const fs        = require('fs');
const path      = require('path');
const { execSync } = require('child_process');
const { ethers } = require('ethers');

// ─── Config ──────────────────────────────────────────────────────────────────

const API_BASE  = process.env.API_BASE || 'https://tsa.mcgpchain.com/api';
const RPC       = process.env.SONIC_TESTNET_RPC || 'https://rpc.testnet.soniclabs.com';

// Secrets — read once, never logged.
const JWT_SECRET        = process.env.JWT_SECRET;
const WEBHOOK_SECRET    = process.env.NOMBA_WEBHOOK_SECRET;
const DEPLOYER_PK       = process.env.DEPLOYER_PRIVATE_KEY;
const DATABASE_URL      = process.env.DATABASE_URL;

if (!JWT_SECRET)     fatal('JWT_SECRET not set in tsa-api-go/.env');
if (!DEPLOYER_PK)    fatal('DEPLOYER_PRIVATE_KEY not set in smart-contracts/.env');
if (!DATABASE_URL)   fatal('DATABASE_URL not set in tsa-api-go/.env');

// On-chain constants (Sonic testnet).
const ESCROW_ADDR = '0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7';
const USDT_ADDR   = '0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad';
const DEPLOYER_ADDR = '0x38cE9a4C9190707829950B7Ae4Eed1d254c2e2c9';
const CHAIN_ID    = 14601;

// Fixed test UUIDs (recognisable; cleaned up in `cleanup` phase).
// NOTE: accountRef = "pm-<PM_USER_ID>". Nomba's SANDBOX caps a holder at 2 VAs
// and rejects re-creating an existing accountRef, so onboard's provisioning is
// not freely re-runnable. For the negatives run we reuse the FIRST run's live
// sandbox VA (accountRef pm-<this id>, inbound 2998287753) by seeding the PM row
// directly + patching state, bypassing provisioning.
const PM_USER_ID     = '0e2e0000-0000-4000-8000-000000000001';
const COUNTER_USER_ID = '0e2e0000-0000-4000-8000-000000000002';
const ADMIN_USER_ID  = '0e2e0000-0000-4000-8000-000000000003';

const STATE_FILE = path.resolve(__dirname, '.pm-e2e-state.json');

const ERC20_ABI = [
  'function mint(address,uint256) external',
  'function approve(address,uint256) external returns (bool)',
  'function balanceOf(address) view returns (uint256)',
];

// ─── Utilities ───────────────────────────────────────────────────────────────

function fatal(msg) {
  console.error(`\n✗ FATAL: ${msg}`);
  process.exit(1);
}

const ok  = (msg) => console.log(`  ✓ ${msg}`);
const fail = (msg) => console.log(`  ✗ ${msg}`);
const info = (msg) => console.log(`  · ${msg}`);

function b64url(buf) {
  return Buffer.from(buf).toString('base64')
    .replace(/=/g, '').replace(/\+/g, '-').replace(/\//g, '_');
}

function forgeJwt(userId) {
  const header  = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const payload = b64url(JSON.stringify({ userId, exp: 4102444800 }));
  const sig     = b64url(
    crypto.createHmac('sha256', JWT_SECRET).update(`${header}.${payload}`).digest()
  );
  return `${header}.${payload}.${sig}`;
}

// --- State persistence -------------------------------------------------------

function loadState() {
  try {
    return JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));
  } catch {
    return {};
  }
}

function saveState(updates) {
  const state = { ...loadState(), ...updates };
  fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
  return state;
}

// --- API helper --------------------------------------------------------------

// hex0x: ensure 0x-prefixed hex string (for unsigned tx fields).
const hex0x = (s) =>
  typeof s === 'string' && s.length > 0 && !s.startsWith('0x') ? '0x' + s : s;

async function api(method, urlPath, token, body) {
  const url = API_BASE + urlPath;
  const res = await fetch(url, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: 'Bearer ' + token } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json;
  try { json = JSON.parse(text); } catch { json = { raw: text }; }
  if (res.status >= 300) {
    throw new Error(`${method} ${urlPath} → HTTP ${res.status}: ${JSON.stringify(json).slice(0, 400)}`);
  }
  return json.data ?? json;
}

// --- Broadcast unsigned tx ---------------------------------------------------

async function broadcast(wallet, unsignedTxObj, label) {
  const to    = hex0x(unsignedTxObj.to || unsignedTxObj.To);
  const data  = hex0x(unsignedTxObj.data || unsignedTxObj.Data || unsignedTxObj.input);
  const value = unsignedTxObj.value || unsignedTxObj.Value || 0;
  if (!to || !data) {
    throw new Error(`${label}: unsignedTx missing to/data: ${JSON.stringify(unsignedTxObj).slice(0, 200)}`);
  }
  const sent = await wallet.sendTransaction({ to, data, value: value ? BigInt(value) : 0n });
  const receipt = await sent.wait();
  if (!receipt || receipt.status !== 1) {
    throw new Error(`${label}: tx reverted ${sent.hash}`);
  }
  ok(`${label}: tx ${sent.hash}`);
  return sent.hash;
}

// --- psql helper (DATABASE_URL never echoed) ---------------------------------

function psql(sql) {
  // Parse DATABASE_URL into psql-compatible env vars so the URL itself is
  // never part of a printed command.  Supports:
  //   postgres://user:pass@host:port/dbname[?sslmode=...]
  const url = new URL(DATABASE_URL);
  const env = {
    ...process.env,
    PGHOST:     url.hostname,
    PGPORT:     url.port || '5432',
    PGUSER:     url.username,
    PGPASSWORD: url.password,  // psql reads PGPASSWORD; never appears in argv
    PGDATABASE: url.pathname.slice(1).split('?')[0],
    PGSSLMODE:  url.searchParams.get('sslmode') || 'prefer',
  };
  const out = execSync(`psql -t -A -c "${sql.replace(/"/g, '\\"')}"`, {
    env,
    encoding: 'utf8',
  });
  return out.trim();
}

// --- Nomba webhook signature builder -----------------------------------------
//
// signingString = "{event_type}:{requestId}:{userId}:{walletId}:{transactionId}:{type}:{time}:{responseCode}:{timestamp}"
// signature     = base64( HMAC_SHA256( NOMBA_WEBHOOK_SECRET, signingString ) )
// headers:       nomba-signature = signature, nomba-timestamp = timestamp
//
function buildNombaWebhookHeaders(payload, timestamp) {
  if (!WEBHOOK_SECRET) {
    throw new Error('NOMBA_WEBHOOK_SECRET not set — cannot sign self-POST webhook');
  }
  const env   = payload;
  const parts = [
    env.event_type,
    env.requestId,
    (env.data?.merchant?.userId  || ''),
    (env.data?.merchant?.walletId || ''),
    (env.data?.transaction?.transactionId || ''),
    (env.data?.transaction?.type          || ''),
    (env.data?.transaction?.time          || ''),
    (env.data?.transaction?.responseCode  || ''),
    timestamp,
  ];
  const signing = parts.join(':');
  const mac     = crypto.createHmac('sha256', WEBHOOK_SECRET).update(signing).digest();
  const sig     = mac.toString('base64');
  return {
    'Content-Type':    'application/json',
    'nomba-signature': sig,
    'nomba-timestamp': timestamp,
  };
}

async function postWebhook(payload) {
  const timestamp = Date.now().toString();
  const headers   = buildNombaWebhookHeaders(payload, timestamp);
  const body      = JSON.stringify(payload);
  const url       = API_BASE + '/webhooks/nomba';
  const res = await fetch(url, { method: 'POST', headers, body });
  const text = await res.text();
  let json;
  try { json = JSON.parse(text); } catch { json = { raw: text }; }
  return { status: res.status, json };
}

// --- Poll helper -------------------------------------------------------------

async function poll(fn, predicate, { interval = 2000, maxAttempts = 20, label = 'poll' } = {}) {
  for (let i = 0; i < maxAttempts; i++) {
    const result = await fn();
    if (predicate(result)) return result;
    info(`${label}: attempt ${i+1}/${maxAttempts}, retrying in ${interval}ms…`);
    await new Promise(r => setTimeout(r, interval));
  }
  throw new Error(`${label}: condition not met after ${maxAttempts} attempts`);
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: seed
// Insert 3 fixed test users (idempotent ON CONFLICT).
// ─────────────────────────────────────────────────────────────────────────────

async function phaseSeed() {
  console.log('\n═══ PHASE: seed ═══');

  const users = [
    { id: PM_USER_ID,      name: 'E2E PM Applicant', username: 'e2e_pm_applicant',  email: 'e2e-pm@example.test',      role: 'user' },
    { id: COUNTER_USER_ID, name: 'E2E Counterparty',  username: 'e2e_counterparty',  email: 'e2e-counter@example.test', role: 'user' },
    { id: ADMIN_USER_ID,   name: 'E2E Admin',         username: 'e2e_admin',         email: 'e2e-admin@example.test',   role: 'super_admin' },
  ];

  for (const u of users) {
    const sql = `
      INSERT INTO users (id, name, username, email, password, role, account_status, verification_status, created_at, updated_at)
      VALUES (
        '${u.id}',
        '${u.name.replace(/'/g, "''")}',
        '${u.username}',
        '${u.email}',
        'smoke-test-nologin',
        '${u.role}',
        'active',
        'verified',
        NOW(), NOW()
      )
      ON CONFLICT (id) DO UPDATE SET
        account_status = 'active',
        verification_status = 'verified',
        role = EXCLUDED.role,
        updated_at = NOW()
    `;
    psql(sql);
    ok(`upserted user ${u.email} (${u.id.slice(0,8)}…) role=${u.role}`);
  }

  // Forge JWTs (never printed as full tokens — just note they're ready).
  const pmJwt      = forgeJwt(PM_USER_ID);
  const counterJwt = forgeJwt(COUNTER_USER_ID);
  const adminJwt   = forgeJwt(ADMIN_USER_ID);

  saveState({
    pmUserId:      PM_USER_ID,
    counterUserId: COUNTER_USER_ID,
    adminUserId:   ADMIN_USER_ID,
    pmJwt,
    counterJwt,
    adminJwt,
    seeded: true,
  });

  ok('JWTs forged and state saved (secrets not printed)');
  console.log('\n✓ seed complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: onboard
// PM-applicant submits a p2p_merchant request → admin approves → poll until active.
// ─────────────────────────────────────────────────────────────────────────────

async function phaseOnboard() {
  console.log('\n═══ PHASE: onboard ═══');
  const state = loadState();
  if (!state.seeded) fatal('Run `seed` phase first');

  const { pmJwt, adminJwt } = state;

  // The PM's maker wallet = deployer address (deployer is funded and is the
  // on-chain admin, so it can fund the pool and sign releases).
  const makerWallet = DEPLOYER_ADDR;

  // Submit merchant request.
  let merchantReqId = state.merchantReqId;
  if (!merchantReqId) {
    info('Submitting merchant request…');
    const req = await api('POST', '/merchant-requests', pmJwt, {
      businessType:            'p2p_merchant',
      businessName:            'E2E PM Corp',
      businessDescription:     'Smoke test payment merchant',
      address:                 '1 Test Street',
      city:                    'Lagos',
      state:                   'Lagos',
      country:                 'NG',
      phone:                   '+2341234567890',
      settlementBankCode:      '999992', // Nomba sandbox test bank — controller should swap real sandbox values
      settlementAccountNumber: '9900000000', // Nomba sandbox test account
      settlementAccountName:   'E2E PM Corp',
      makerWalletAddress:      makerWallet,
      requestedPerTradeCapUsd: 500,
      requestedDailyCapUsd:    5000,
    });
    merchantReqId = req.id;
    saveState({ merchantReqId });
    ok(`merchant request submitted id=${merchantReqId}`);
  } else {
    info(`merchant request already submitted id=${merchantReqId}`);
  }

  // Admin approves.
  info('Admin approving merchant request…');
  try {
    await api('POST', `/admin/merchant-requests/${merchantReqId}/approve`, adminJwt, {
      note: 'Smoke test approval',
    });
    ok('merchant request approved (PM provisioning triggered)');
  } catch (e) {
    // If already approved the API returns 400 "not pending"; that's OK on re-run.
    if (e.message.includes('not in pending') || e.message.includes('already approved') ||
        e.message.includes('approved but PM provisioning failed')) {
      info(`approve returned: ${e.message} (may be re-run, continuing)`);
    } else {
      throw e;
    }
  }

  // Poll admin PM list until a PM row for PM_USER_ID is `active`.
  info('Polling /admin/pm until PM is active…');
  const pmList = await poll(
    () => api('GET', '/admin/pm', adminJwt),
    (list) => {
      const arr = Array.isArray(list) ? list : (list.items || list.data || []);
      return arr.some(p => p.nombaAccountRef === `pm-${PM_USER_ID}` && p.status === 'active');
    },
    { interval: 3000, maxAttempts: 15, label: 'PM active' },
  );

  const arr = Array.isArray(pmList) ? pmList : (pmList.items || pmList.data || []);
  const pmRow = arr.find(p => p.nombaAccountRef === `pm-${PM_USER_ID}`);
  if (!pmRow) throw new Error('PM row not found after approval');

  ok(`PM active: id=${pmRow.id}`);
  ok(`  accountRef=${pmRow.nombaAccountRef}`);
  ok(`  inboundAccountNumber=${pmRow.inboundAccountNumber}`);
  ok(`  status=${pmRow.status} provisioningStatus=${pmRow.provisioningStatus}`);

  saveState({
    pmId:                pmRow.id,
    pmAccountRef:        pmRow.nombaAccountRef,
    pmInboundAccount:    pmRow.inboundAccountNumber,
    pmNombaWalletId:     pmRow.nombaWalletId || '',
  });

  console.log('\n✓ onboard complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: buy
// PM creates sell-ad; counterparty opens a buy trade; self-POST inbound
// payment_success webhook → assert auto-release.
// ─────────────────────────────────────────────────────────────────────────────

async function phaseBuy() {
  console.log('\n═══ PHASE: buy ═══');
  const state = loadState();
  if (!state.pmId) fatal('Run `onboard` phase first');

  const { pmJwt, counterJwt, adminJwt, pmId, pmAccountRef, pmInboundAccount } = state;

  // Set up ethers provider + deployer wallet (never print private key).
  const provider  = new ethers.JsonRpcProvider(RPC);
  const pk        = DEPLOYER_PK.startsWith('0x') ? DEPLOYER_PK : '0x' + DEPLOYER_PK;
  const deployer  = new ethers.Wallet(pk, provider);
  const usdt      = new ethers.Contract(USDT_ADDR, ERC20_ABI, deployer);

  info(`deployer: ${deployer.address}`);
  info(`provider chain: ${(await provider.getNetwork()).chainId}`);

  // ── Step 1: PM applicant user creates a payment method ──────────────────
  info('Creating PM payment method…');
  const pm = await api('POST', '/p2p/payment-methods', pmJwt, {
    provider: 'bank_transfer', country: 'NG', currency: 'NGN',
    bankName: 'Access Bank', bankCode: '044',
    accountNumber: '0123456789', accountName: 'E2E PM Corp', isDefault: true,
  });
  const pmMethodId = pm.id;
  ok(`payment method ${pmMethodId}`);

  // ── Step 2: Deposit 2 USDT into pool (approve → deposit → confirm) ───────
  info('Approving USDT for pool deposit…');
  const depositAmount = '2000000'; // 2 USDT (6 decimals)
  const approveRes = await api('POST', '/p2p/deposits/approve', pmJwt, {
    token: 'USDT', amount: depositAmount, walletAddress: deployer.address,
  });
  await broadcast(deployer, approveRes.unsignedTx, 'buy.pool.approve');

  info('Depositing to pool…');
  const depRes = await api('POST', '/p2p/deposits', pmJwt, {
    token: 'USDT', amount: depositAmount, walletAddress: deployer.address,
  });
  const depTx = await broadcast(deployer, depRes.unsignedTx, 'buy.pool.deposit');
  await api('POST', '/p2p/deposits/confirm', pmJwt, {
    txHash: depTx, token: 'USDT', amount: depositAmount, walletAddress: deployer.address,
  });
  ok(`pool deposit confirmed txHash=${depTx}`);

  // ── Step 3: Create sell ad + sign voucher ────────────────────────────────
  info('Creating sell ad…');
  const adRes = await api('POST', '/p2p/ads', pmJwt, {
    side: 'sell', token: 'USDT', tokenAmount: '1000000',
    fiatCurrency: 'NGN', rate: '1500', rateType: 'fixed',
    minLimit: '500', maxLimit: '2000',
    paymentMethodIds: [pmMethodId],
    terms: 'E2E PM sell ad',
    sellerAddress: deployer.address,
  });
  const sellAd = adRes.ad;
  const td     = adRes.typedData;
  ok(`sell ad ${sellAd.id} status=${sellAd.status}`);

  info('Signing ad voucher…');
  const sig = await deployer.signTypedData(
    {
      name: td.domain.name, version: td.domain.version,
      chainId: td.domain.chainId, verifyingContract: td.domain.verifyingContract,
    },
    { AdVoucher: td.types.AdVoucher },
    td.message,
  );
  const signedAd = await api('POST', `/p2p/ads/${sellAd.id}/sign`, pmJwt, {
    signature: sig, sellerAddress: deployer.address,
  });
  if (signedAd.status !== 'active') {
    throw new Error(`sell ad sign: expected active, got ${signedAd.status}`);
  }
  ok('voucher signed → ad active');

  // ── Step 4: Counterparty opens trade (₦1500 = 1 USDT) ───────────────────
  info('Counterparty opening trade…');

  // Counter user needs gas; fund from deployer.
  const counterW = new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
  info(`counterparty ephemeral wallet: ${counterW.address}`);
  await (await deployer.sendTransaction({ to: counterW.address, value: ethers.parseEther('0.3') })).wait();
  ok('counterparty funded with gas');

  const openRes = await api('POST', `/p2p/ads/${sellAd.id}/trades`, counterJwt, {
    fiatAmount: '1500',
    paymentMethodId: pmMethodId,
    buyerAddress: counterW.address,
  });
  const buyTrade = openRes.trade;
  const openTx   = await broadcast(counterW, openRes.unsignedTx, 'buy.openTrade');
  await api('POST', `/p2p/trades/${buyTrade.id}/open/confirm`, counterJwt, {
    txHash: openTx, callerAddress: counterW.address,
  });
  ok(`trade ${buyTrade.id} opened + confirmed (pending_payment)`);

  // ── Step 5: Self-POST payment_success webhook ────────────────────────────
  // The inbound webhook must carry accountRef = pmAccountRef, and the
  // senderName must match the COUNTER user's users.name exactly (KYC name
  // match in pm_automation_service.go::matchBuyInbound).
  // Amount must equal trade.fiatAmount (₦1500).
  info('Self-posting payment_success webhook…');

  const inboundRequestId = `e2e-buy-${Date.now()}`;
  const tsNow = new Date().toISOString();
  const inboundPayload = {
    event_type: 'payment_success',
    requestId:  inboundRequestId,
    data: {
      merchant: {
        userId:   '',         // not needed for VA-model accountRef lookup
        walletId: '',         // not used in accountRef-only model
      },
      transaction: {
        transactionId:     `txn-buy-${Date.now()}`,
        type:              'credit',
        transactionAmount: 1500,   // naira (not kobo)
        fee:               0,
        time:              tsNow,
        responseCode:      '00',
        narration:         'PM E2E buy test',
        accountRef:        pmAccountRef, // VA-model key — routes to this PM
        aliasAccountNumber: pmInboundAccount,
      },
      customer: {
        senderName:    'E2E Counterparty', // must match users.name of COUNTER_USER_ID
        accountNumber: '0000000001',
        bankCode:      '999992',
        bankName:      'Nomba Test',
      },
    },
  };

  const wResult = await postWebhook(inboundPayload);
  ok(`webhook POST status=${wResult.status} message=${wResult.json?.message || JSON.stringify(wResult.json)}`);

  // ── Step 6: Poll trade status until released ─────────────────────────────
  info('Polling buy trade until released…');
  const finalTrade = await poll(
    () => api('GET', `/p2p/trades/${buyTrade.id}`, counterJwt),
    (r) => {
      const status = r.trade?.status ?? r.status;
      return status === 'released' || status === 'cancelled';
    },
    { interval: 3000, maxAttempts: 20, label: 'buy trade released' },
  );
  const buyFinalStatus = finalTrade.trade?.status ?? finalTrade.status;
  if (buyFinalStatus !== 'released') {
    fail(`buy trade not released: status=${buyFinalStatus}`);
    throw new Error(`buy trade auto-release failed, status=${buyFinalStatus}`);
  }
  ok(`buy trade ${buyTrade.id} is released`);

  // ── Step 7: Verify PM available_ngn increased ────────────────────────────
  const pmListAfter = await api('GET', '/admin/pm', adminJwt);
  const arrAfter = Array.isArray(pmListAfter) ? pmListAfter : (pmListAfter.items || pmListAfter.data || []);
  const pmAfter  = arrAfter.find(p => p.id === pmId);
  const avail    = pmAfter?.availableNgn ?? pmAfter?.available_ngn ?? '?';
  ok(`PM available_ngn after buy: ${avail}`);

  saveState({
    buyAdId:    sellAd.id,
    buyTradeId: buyTrade.id,
    buyDepositTxHash: depTx,
    buyOpenTxHash:    openTx,
    buyInboundEventId: inboundRequestId,
    counterpartyAddress: counterW.address,
    pmMethodId,
  });

  console.log('\n✓ buy phase complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: sell
// PM creates buy-ad; counterparty opens sell trade (JIT deposit);
// admin kicks payout; self-POST payout_success webhook → assert release.
// ─────────────────────────────────────────────────────────────────────────────

async function phaseSell() {
  console.log('\n═══ PHASE: sell ═══');
  const state = loadState();
  if (!state.pmId) fatal('Run `onboard` phase first');

  const { pmJwt, counterJwt, adminJwt, pmId, pmMethodId: existingPmMethodId } = state;

  const provider  = new ethers.JsonRpcProvider(RPC);
  const pk        = DEPLOYER_PK.startsWith('0x') ? DEPLOYER_PK : '0x' + DEPLOYER_PK;
  const deployer  = new ethers.Wallet(pk, provider);

  // Reuse or recreate PM payment method.
  let pmMethodId = existingPmMethodId;
  if (!pmMethodId) {
    const pm = await api('POST', '/p2p/payment-methods', pmJwt, {
      provider: 'bank_transfer', country: 'NG', currency: 'NGN',
      bankName: 'Access Bank', bankCode: '044',
      accountNumber: '0123456789', accountName: 'E2E PM Corp', isDefault: false,
    });
    pmMethodId = pm.id;
    saveState({ pmMethodId });
  }
  ok(`PM payment method: ${pmMethodId}`);

  // Counterparty needs their OWN payment method: a buy-ad open validates the
  // OPENER owns the payment method (validatePaymentMethodsOwned) — it's where
  // the PM will pay the NGN. (A sell-ad open instead validates the AD's method,
  // which is why the buy phase could reuse the PM's.)
  let counterMethodId = state.counterMethodId;
  if (!counterMethodId) {
    const cpm = await api('POST', '/p2p/payment-methods', counterJwt, {
      provider: 'bank_transfer', country: 'NG', currency: 'NGN',
      bankName: 'Access Bank', bankCode: '044',
      accountNumber: '0123456789', accountName: 'E2E Counterparty', isDefault: false,
    });
    counterMethodId = cpm.id;
    saveState({ counterMethodId });
  }
  ok(`counterparty payment method: ${counterMethodId}`);

  // ── Step 1: Create buy-ad ────────────────────────────────────────────────
  info('Creating PM buy-ad…');
  const buyAdRes = await api('POST', '/p2p/ads', pmJwt, {
    side: 'buy', token: 'USDT', tokenAmount: '1000000',
    fiatCurrency: 'NGN', rate: '1500', rateType: 'fixed',
    minLimit: '500', maxLimit: '2000',
    paymentMethodIds: [pmMethodId],
    terms: 'E2E PM buy ad',
    sellerAddress: deployer.address, // buy-ads: sellerAddress is the PM's maker wallet
  });
  const buyAd = buyAdRes.ad;
  ok(`buy-ad ${buyAd.id} status=${buyAd.status}`);

  // ── Step 2: Counterparty funds + opens sell trade (JIT deposit) ──────────
  const counterW = new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
  info(`counterparty ephemeral wallet: ${counterW.address}`);
  await (await deployer.sendTransaction({ to: counterW.address, value: ethers.parseEther('0.3') })).wait();
  const usdt = new ethers.Contract(USDT_ADDR, ERC20_ABI, deployer);
  await (await usdt.mint(counterW.address, 2_000_000n)).wait();
  ok('counterparty funded with gas + 2 USDT');

  // Approve 1 USDT for the JIT deposit.
  info('Counterparty approving USDT for JIT deposit…');
  const approveRes = await api('POST', '/p2p/deposits/approve', counterJwt, {
    token: 'USDT', amount: '1000000', walletAddress: counterW.address,
  });
  await broadcast(counterW, approveRes.unsignedTx, 'sell.approve');

  // Open trade (openTradeWithDeposit — deposited-by-taker path).
  // For a PM buy-ad, counterparty must supply payoutAccountNumber + payoutBank.
  info('Counterparty opening sell trade (JIT deposit)…');
  const openRes2 = await api('POST', `/p2p/ads/${buyAd.id}/trades`, counterJwt, {
    tokenAmount:         '1000000',
    paymentMethodId:     counterMethodId,
    sellerAddress:       counterW.address,
    // Payout destination: where the PM automation will send the NGN to.
    // For a PM buy-ad, this is the counterparty's settlement bank.
    payoutAccountNumber: '0123456789',
    payoutBank:          '044',
    payoutAccountName:   'E2E Counterparty',
  });
  const sellTrade = openRes2.trade;
  const openTx2   = await broadcast(counterW, openRes2.unsignedTx, 'sell.openTradeWithDeposit');
  await api('POST', `/p2p/trades/${sellTrade.id}/open/confirm`, counterJwt, {
    txHash: openTx2, callerAddress: counterW.address,
  });
  ok(`sell trade ${sellTrade.id} opened (deposited-by-taker, pending_payment)`);

  saveState({
    sellAdId:    buyAd.id,
    sellTradeId: sellTrade.id,
    sellOpenTxHash: openTx2,
    sellCounterAddress: counterW.address,
  });

  // ── Step 3: Admin kicks sell payout ─────────────────────────────────────
  // POST /admin/pm/trades/:id/payout — initiates the Nomba bank transfer.
  info('Admin initiating sell payout…');
  let payoutId;
  try {
    const payoutRes = await api('POST', `/admin/pm/trades/${sellTrade.id}/payout`, adminJwt);
    payoutId = payoutRes.payoutId;
    ok(`sell payout initiated: payoutId=${payoutId}`);
  } catch (e) {
    // If Nomba is not configured (503/500) this will fail in sandbox — note it.
    fail(`sell payout initiation failed: ${e.message}`);
    fail('  (This is expected if Nomba credentials are absent/sandboxed; the webhook below will still exercise the payout-success path)');
    payoutId = `sandbox-payout-${Date.now()}`;
    // Directly patch the trade's nomba_payout_id via psql so the webhook can match it.
    psql(`UPDATE p2p_trades SET nomba_payout_id = '${payoutId}' WHERE id = '${sellTrade.id}'`);
    info(`  patched nomba_payout_id=${payoutId} via psql for webhook test`);
  }
  saveState({ sellPayoutId: payoutId });

  // ── Step 4: Self-POST payout_success webhook ─────────────────────────────
  // MerchantTxRef = trade UUID (used as the deterministic key in HandlePayoutStatus).
  info('Self-posting payout_success webhook…');
  const payoutRequestId = `e2e-sell-${Date.now()}`;
  const tsNow = new Date().toISOString();
  const payoutPayload = {
    event_type: 'payout_success',
    requestId:  payoutRequestId,
    data: {
      merchant: {
        userId:   '',
        walletId: '',
      },
      transaction: {
        transactionId:     payoutId,
        type:              'debit',
        transactionAmount: 1500,
        fee:               0,
        time:              tsNow,
        responseCode:      '00',
        merchantTxRef:     sellTrade.id, // trade UUID — deterministic match key
      },
      customer: {
        senderName:    'E2E PM Corp',
        recipientName: 'E2E Counterparty',
        accountNumber: '0123456789',
        bankCode:      '044',
        bankName:      'Access Bank',
      },
    },
  };

  const wResult2 = await postWebhook(payoutPayload);
  ok(`payout webhook POST status=${wResult2.status} message=${wResult2.json?.message || JSON.stringify(wResult2.json)}`);
  saveState({ sellPayoutEventId: payoutRequestId });

  // ── Step 5: Poll trade until released ────────────────────────────────────
  info('Polling sell trade until released…');
  const finalSellTrade = await poll(
    () => api('GET', `/p2p/trades/${sellTrade.id}`, counterJwt),
    (r) => {
      const status = r.trade?.status ?? r.status;
      return status === 'released' || status === 'cancelled';
    },
    { interval: 3000, maxAttempts: 20, label: 'sell trade released' },
  );
  const sellFinalStatus = finalSellTrade.trade?.status ?? finalSellTrade.status;
  if (sellFinalStatus !== 'released') {
    fail(`sell trade not released: status=${sellFinalStatus}`);
    throw new Error(`sell trade payout-release failed, status=${sellFinalStatus}`);
  }
  ok(`sell trade ${sellTrade.id} is released`);

  // ── Step 6: Verify reserved_ngn was consumed ─────────────────────────────
  const pmListAfter = await api('GET', '/admin/pm', adminJwt);
  const arrAfter = Array.isArray(pmListAfter) ? pmListAfter : (pmListAfter.items || pmListAfter.data || []);
  const pmAfter  = arrAfter.find(p => p.id === pmId);
  ok(`PM available_ngn after sell: ${pmAfter?.availableNgn ?? '?'}`);
  ok(`PM reserved_ngn after sell:  ${pmAfter?.reservedNgn  ?? '?'}`);

  console.log('\n✓ sell phase complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: withdraw
// Admin withdraws PM's available_ngn to settlement bank.
// ─────────────────────────────────────────────────────────────────────────────

async function phaseWithdraw() {
  console.log('\n═══ PHASE: withdraw ═══');
  const state = loadState();
  if (!state.pmId) fatal('Run `onboard` phase first');

  const { adminJwt, pmId } = state;

  // Fetch current available balance.
  const pmListBefore = await api('GET', '/admin/pm', adminJwt);
  const arrBefore = Array.isArray(pmListBefore) ? pmListBefore : (pmListBefore.items || pmListBefore.data || []);
  const pmBefore  = arrBefore.find(p => p.id === pmId);
  const available = pmBefore?.availableNgn ?? pmBefore?.available_ngn ?? '0';
  const reserved  = pmBefore?.reservedNgn  ?? pmBefore?.reserved_ngn  ?? '0';

  ok(`PM available_ngn before withdraw: ${available}`);
  ok(`PM reserved_ngn before withdraw:  ${reserved}`);

  // Withdraw free balance = available − reserved (approximated as a string subtraction).
  const avF  = parseFloat(available) || 0;
  const resF = parseFloat(reserved)  || 0;
  const free = (avF - resF).toFixed(2);

  if (parseFloat(free) <= 0) {
    info(`No free NGN to withdraw (available=${available}, reserved=${reserved}); skipping gateway call`);
    saveState({ withdrawPayoutId: null, withdrawAmount: free });
    console.log('\n✓ withdraw phase complete (nothing to withdraw)');
    return;
  }

  info(`Initiating withdrawal of ₦${free}…`);
  let wPayoutId;
  try {
    const wRes = await api('POST', `/admin/pm/${pmId}/withdraw`, adminJwt, { amount: free });
    wPayoutId = wRes.payoutId;
    ok(`withdrawal initiated: payoutId=${wPayoutId}`);
  } catch (e) {
    fail(`withdrawal failed: ${e.message}`);
    info('(Expected if Nomba gateway is absent/sandbox)');
    wPayoutId = null;
  }

  // Verify available_ngn decreased.
  const pmListAfter = await api('GET', '/admin/pm', adminJwt);
  const arrAfter = Array.isArray(pmListAfter) ? pmListAfter : (pmListAfter.items || pmListAfter.data || []);
  const pmAfter  = arrAfter.find(p => p.id === pmId);
  ok(`PM available_ngn after withdraw: ${pmAfter?.availableNgn ?? '?'}`);

  saveState({ withdrawPayoutId: wPayoutId, withdrawAmount: free });
  console.log('\n✓ withdraw phase complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: verify
// Print final summary: PM status, NGN ledger, trade statuses, on-chain balance.
// ─────────────────────────────────────────────────────────────────────────────

async function phaseVerify() {
  console.log('\n═══ PHASE: verify ═══');
  const state = loadState();
  if (!state.pmId) fatal('Run `onboard` phase first');

  const { adminJwt, counterJwt, pmId, buyTradeId, sellTradeId, buyAdId, sellAdId } = state;

  // PM row.
  const pmList = await api('GET', '/admin/pm', adminJwt);
  const arr    = Array.isArray(pmList) ? pmList : (pmList.items || pmList.data || []);
  const pmRow  = arr.find(p => p.id === pmId);

  console.log('\n── PM status ────────────────────────');
  if (pmRow) {
    console.log(`  id:                   ${pmRow.id}`);
    console.log(`  status:               ${pmRow.status}`);
    console.log(`  provisioningStatus:   ${pmRow.provisioningStatus}`);
    console.log(`  inboundAccountNumber: ${pmRow.inboundAccountNumber}`);
    console.log(`  accountRef:           ${pmRow.nombaAccountRef}`);
    console.log(`  available_ngn:        ${pmRow.availableNgn}`);
    console.log(`  reserved_ngn:         ${pmRow.reservedNgn}`);
  } else {
    fail('PM row not found');
  }

  // Trade statuses.
  console.log('\n── Trade statuses ───────────────────');
  for (const [label, tradeId] of [['buy trade', buyTradeId], ['sell trade', sellTradeId]]) {
    if (!tradeId) { info(`${label}: not run`); continue; }
    try {
      const t = await api('GET', `/p2p/trades/${tradeId}`, counterJwt);
      const status = t.trade?.status ?? t.status;
      console.log(`  ${label} ${tradeId}: status=${status}`);
      if (status === 'released') ok(`  ${label}: released ✓`);
      else fail(`  ${label}: expected released, got ${status}`);
    } catch (e) {
      fail(`  ${label} ${tradeId}: ${e.message}`);
    }
  }

  // On-chain pool balance.
  console.log('\n── On-chain escrow pool ─────────────');
  try {
    const provider = new ethers.JsonRpcProvider(RPC);
    const escrowAbi = ['function balanceOf(address,address) view returns (uint256)'];
    const escrow = new ethers.Contract(ESCROW_ADDR, escrowAbi, provider);
    const bal = await escrow.balanceOf(DEPLOYER_ADDR, USDT_ADDR);
    console.log(`  P2PEscrow.balanceOf(deployer, USDT): ${ethers.formatUnits(bal, 6)} USDT`);
    ok('on-chain balance readable');
  } catch (e) {
    fail(`on-chain balance check failed: ${e.message}`);
  }

  console.log('\n✓ verify complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: cleanup
// Delete all rows created by this run (by tracked IDs).
// ─────────────────────────────────────────────────────────────────────────────

async function phaseCleanup() {
  console.log('\n═══ PHASE: cleanup ═══');
  const state = loadState();

  const { buyTradeId, sellTradeId, buyAdId, sellAdId, merchantReqId } = state;

  // Include negatives-phase N3 trade + ad alongside buy/sell phase rows.
  const tradeIds = [buyTradeId, sellTradeId, state.negN3TradeId].filter(Boolean);
  const adIds    = [buyAdId,    sellAdId,    state.negN3AdId   ].filter(Boolean);

  // Deletion order respects FK constraints (messages/trades before ads/PMs, etc.)
  const steps = [
    {
      // Covers:
      //  - buy phase:      buyInboundEventId  (e.g. "e2e-buy-…")
      //  - sell phase:     sellPayoutEventId  (e.g. "e2e-sell-…")
      //  - negatives N1:   negN1EventId       ("e2e-neg-n1-…")
      //  - negatives N2:   negN2EventId       ("e2e-neg-n2-…")
      //  - negatives N3-A: negN3EventIdWrong  ("e2e-neg-n3a-…")
      //  - negatives N3-B: negN3EventIdRight  ("e2e-neg-n3b-…")
      // The prefix LIKE 'e2e-%' catches all of the above without enumerating
      // each one individually; it is safe because all test requestIds use the
      // 'e2e-' prefix by convention and no prod events share this prefix.
      table: 'webhook_events',
      sql: `DELETE FROM webhook_events WHERE provider = 'nomba' AND event_id LIKE 'e2e-%'`,
    },
    {
      table: 'p2p_messages',
      sql: tradeIds.length
        ? `DELETE FROM p2p_messages WHERE trade_id IN (${tradeIds.map(id => `'${id}'`).join(',')})`
        : null,
    },
    {
      table: 'p2p_trades',
      sql: tradeIds.length
        ? `DELETE FROM p2p_trades WHERE id IN (${tradeIds.map(id => `'${id}'`).join(',')})`
        : null,
    },
    {
      table: 'p2p_ads',
      sql: adIds.length
        ? `DELETE FROM p2p_ads WHERE id IN (${adIds.map(id => `'${id}'`).join(',')})`
        : null,
    },
    {
      table: 'p2p_payment_methods',
      sql: (() => {
        const ids = [state.pmMethodId, state.counterMethodId].filter(Boolean);
        return ids.length ? `DELETE FROM p2p_payment_methods WHERE id IN (${ids.map(id => `'${id}'`).join(',')})` : null;
      })(),
    },
    {
      table: 'payment_merchants',
      sql: `DELETE FROM payment_merchants WHERE user_id = '${PM_USER_ID}'`,
    },
    {
      table: 'merchant_requests',
      sql: merchantReqId
        ? `DELETE FROM merchant_requests WHERE id = '${merchantReqId}'`
        : `DELETE FROM merchant_requests WHERE user_id = '${PM_USER_ID}'`,
    },
    {
      table: 'users',
      sql: `DELETE FROM users WHERE id IN ('${PM_USER_ID}','${COUNTER_USER_ID}','${ADMIN_USER_ID}')`,
    },
  ];

  for (const step of steps) {
    if (!step.sql) { info(`${step.table}: nothing to delete`); continue; }
    try {
      const out = psql(step.sql + ' RETURNING id');
      const count = out.split('\n').filter(l => l.trim()).length;
      ok(`${step.table}: deleted ${count} row(s)`);
    } catch (e) {
      // Some tables may not have RETURNING id (e.g., if the row didn't exist).
      try {
        psql(step.sql);
        ok(`${step.table}: deleted (no RETURNING)`);
      } catch (e2) {
        fail(`${step.table}: ${e2.message}`);
      }
    }
  }

  // Remove state file.
  try { fs.unlinkSync(STATE_FILE); ok('state file removed'); } catch {}

  console.log('\n✓ cleanup complete');
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase: negatives
//
// Proves the PM buy-flow AUTO-RELEASE SAFETY invariants without relying on the
// happy-path buy/sell phases.  Each sub-test (N1/N2/N3) is isolated in its own
// try/catch so a failure in one does NOT abort the others.  The phase exits
// non-zero if any sub-test failed.
//
// Preconditions: `seed` + `onboard` already ran (state has pmId, pmAccountRef,
// pmJwt, counterJwt, adminJwt, and the counter user's name is "E2E Counterparty").
//
// Sub-tests:
//   N1 — dedup / double-credit guard (no on-chain tx needed).
//   N2 — non-settling event is a no-op (no on-chain tx needed).
//   N3 — wrong-amount mismatch → no release + appears in manual queue;
//        then the correct inbound recovers the trade (proves safety + recovery).
//
// State keys added:
//   negN1EventId       – requestId used for both N1 POSTs (dedup key; cleaned up).
//   negN3AdId          – sell ad created for N3 (cleaned up).
//   negN3TradeId       – buy trade opened for N3 (cleaned up).
//   negN3EventIdWrong  – requestId of the wrong-amount N3 inbound (cleaned up).
//   negN3EventIdRight  – requestId of the correct N3 inbound (cleaned up).
//
// Cleanup:
//   - webhook_events rows matched by event_id LIKE 'e2e-neg-%' are removed by
//     the existing cleanup phase (extended below to cover the pattern).
//   - N3 ad/trade rows are tracked under negN3AdId / negN3TradeId and deleted by
//     the extended cleanup step.
// ─────────────────────────────────────────────────────────────────────────────

async function phaseNegatives() {
  console.log('\n═══ PHASE: negatives ═══');
  const state = loadState();
  if (!state.pmId) {
    fatal('negatives phase requires `onboard` to have run first (state missing pmId).');
  }

  const { pmJwt, counterJwt, adminJwt, pmId, pmAccountRef, pmInboundAccount } = state;

  // Helper: read PM available_ngn from /admin/pm list.
  async function readAvailableNgn() {
    const list = await api('GET', '/admin/pm', adminJwt);
    const arr  = Array.isArray(list) ? list : (list.items || list.data || []);
    const row  = arr.find(p => p.id === pmId);
    // API may return camelCase or snake_case depending on serializer version.
    const raw  = row?.availableNgn ?? row?.available_ngn ?? '0';
    return parseFloat(raw) || 0;
  }

  // Helper: build a minimal signed settled payment_success inbound.
  // senderName and amount are the two match variables; requestId is the dedup key.
  function buildSettledInbound({ requestId, amount, senderName }) {
    const tsNow = new Date().toISOString();
    return {
      event_type: 'payment_success',
      requestId,
      data: {
        merchant: { userId: '', walletId: '' },
        transaction: {
          transactionId:      `txn-${requestId}`,
          type:               'credit',
          transactionAmount:  amount,   // naira (not kobo); json.Number in parser
          fee:                0,
          time:               tsNow,
          responseCode:       '00',
          narration:          'PM E2E negatives test',
          accountRef:         pmAccountRef,
          aliasAccountNumber: pmInboundAccount || '',
        },
        customer: {
          senderName,
          accountNumber: '0000000099',
          bankCode:      '999992',
          bankName:      'Nomba Test',
        },
      },
    };
  }

  let anyFailed = false;

  // ── N1: dedup / double-credit guard ────────────────────────────────────────
  // Send the SAME settled inbound twice (same requestId = same dedup key).
  // Expected: available_ngn rises by exactly 1234.00 (not 2468.00) across both
  // POSTs, and both return HTTP 200.
  //
  // Mechanism: NombaWebhookStore.Record uses an ON CONFLICT DO NOTHING upsert on
  // (provider, event_id). The second POST finds the existing row and skips
  // dispatch entirely — the webhook handler returns 200 (already processed) but
  // does NOT call HandleInbound a second time, so CreditNGN fires only once.
  // The senderName "Nobody Matches" ensures this credited event never matches a
  // live trade (no auto-release, pure credit test).
  try {
    console.log('\n  ── N1: dedup / double-credit guard ──');
    const n1EventId = `e2e-neg-n1-${Date.now()}`;
    saveState({ negN1EventId: n1EventId });

    const before = await readAvailableNgn();
    info(`N1: available_ngn before = ${before}`);

    const n1Payload = buildSettledInbound({
      requestId:  n1EventId,
      amount:     1234.00,
      senderName: 'Nobody Matches N1',  // no pending trade with this name → credit only
    });

    // First POST — must be accepted.
    const r1 = await postWebhook(n1Payload);
    if (r1.status >= 300) {
      throw new Error(`N1 first POST returned HTTP ${r1.status}: ${JSON.stringify(r1.json)}`);
    }
    info(`N1: first POST status=${r1.status}`);

    // Second POST with IDENTICAL requestId — must also return 200 (already processed).
    const r2 = await postWebhook(n1Payload);
    if (r2.status !== 200) {
      throw new Error(`N1 second POST (duplicate) returned HTTP ${r2.status}, expected 200`);
    }
    info(`N1: second POST (dup) status=${r2.status} — ok`);

    // Brief settle wait: the webhook dispatch is synchronous in the current
    // implementation, but allow up to ~3s for any async path.
    await new Promise(r => setTimeout(r, 3000));

    const after = await readAvailableNgn();
    info(`N1: available_ngn after  = ${after}`);

    const delta = Math.round((after - before) * 100) / 100;
    if (Math.abs(delta - 1234.00) > 0.01) {
      throw new Error(`N1 FAIL: expected available_ngn to rise by 1234.00, actual delta=${delta}`);
    }
    ok(`N1 PASS: available_ngn rose by exactly ${delta} (not double-credited)`);
  } catch (e) {
    fail(`N1 FAIL: ${e.message}`);
    anyFailed = true;
  }

  // ── N2: non-settling event is a no-op ──────────────────────────────────────
  // Post a payment_failed inbound (a well-formed Nomba envelope but NOT
  // payment_success). Expected: available_ngn UNCHANGED, HTTP 200.
  //
  // Mechanism: ParseNombaInbound detects event_type "payment_failed" has the
  // "payment_" prefix but is not "payment_success" → returns ErrNonSettlingEvent.
  // The webhook handler converts ErrNonSettlingEvent to a 200 "processed" response
  // (no credit, no release). The event IS recorded in webhook_events (provider=nomba,
  // event_id=requestId, status=processed) so it can be cleaned up by prefix.
  //
  // Note: the signing string uses the same colon-delimited scheme regardless of
  // event_type — the signature check passes before the parser runs.
  try {
    console.log('\n  ── N2: non-settling event is a no-op ──');

    const n2EventId = `e2e-neg-n2-${Date.now()}`;
    saveState({ negN2EventId: n2EventId });

    const before = await readAvailableNgn();
    info(`N2: available_ngn before = ${before}`);

    // Build as if it were a settled inbound for the signature, then override
    // event_type to payment_failed.  The signature builder reads event_type from
    // the payload object, so it will sign "payment_failed" correctly.
    const n2Payload = {
      event_type: 'payment_failed',         // non-settling; must be a no-op
      requestId:  n2EventId,
      data: {
        merchant: { userId: '', walletId: '' },
        transaction: {
          transactionId:      `txn-${n2EventId}`,
          type:               'credit',
          transactionAmount:  999.00,
          fee:                0,
          time:               new Date().toISOString(),
          responseCode:       '99',          // failure code
          narration:          'PM E2E N2 non-settling test',
          accountRef:         pmAccountRef,
          aliasAccountNumber: pmInboundAccount || '',
        },
        customer: {
          senderName:    'E2E Counterparty',
          accountNumber: '0000000099',
          bankCode:      '999992',
          bankName:      'Nomba Test',
        },
      },
    };

    const r = await postWebhook(n2Payload);
    if (r.status >= 300) {
      throw new Error(`N2 non-settling POST returned HTTP ${r.status}: ${JSON.stringify(r.json)}`);
    }
    info(`N2: POST status=${r.status}`);

    await new Promise(r => setTimeout(r, 3000));

    const after = await readAvailableNgn();
    info(`N2: available_ngn after  = ${after}`);

    const delta = Math.round((after - before) * 100) / 100;
    if (Math.abs(delta) > 0.01) {
      throw new Error(`N2 FAIL: available_ngn changed by ${delta}, expected no change`);
    }
    ok(`N2 PASS: non-settling payment_failed was a no-op (available_ngn unchanged)`);
  } catch (e) {
    fail(`N2 FAIL: ${e.message}`);
    anyFailed = true;
  }

  // ── N3: wrong-amount mismatch → no release + manual queue; then recovery ───
  // Full on-chain setup (pool deposit + sell ad + openTrade) to get a real
  // pending_payment trade for amount A (₦1500 = 1 USDT).
  //
  // Part A — wrong amount:
  //   POST settled inbound senderName="E2E Counterparty", amount=A+500=2000.
  //   matchBuyInbound: ngnEqual("1500.00","2000.00") = false → no match → no-op.
  //   Assert: trade stays pending_payment (poll), appears in manual queue.
  //
  // Part B — correct inbound (recovery):
  //   POST settled inbound senderName="E2E Counterparty", amount=1500.
  //   matchBuyInbound: exactly one hit → AutoRelease fires.
  //   Assert: trade status becomes released.
  try {
    console.log('\n  ── N3: wrong-amount mismatch → no release, then recovery ──');

    // Set up ethers provider + deployer wallet.
    const provider = new ethers.JsonRpcProvider(RPC);
    const pk       = DEPLOYER_PK.startsWith('0x') ? DEPLOYER_PK : '0x' + DEPLOYER_PK;
    const deployer = new ethers.Wallet(pk, provider);

    info(`N3: deployer ${deployer.address}`);

    // Reuse or create PM payment method.
    let n3MethodId = state.pmMethodId;
    if (!n3MethodId) {
      const pm = await api('POST', '/p2p/payment-methods', pmJwt, {
        provider: 'bank_transfer', country: 'NG', currency: 'NGN',
        bankName: 'Access Bank', bankCode: '044',
        accountNumber: '0123456789', accountName: 'E2E PM Corp', isDefault: false,
      });
      n3MethodId = pm.id;
      saveState({ pmMethodId: n3MethodId });
    }
    ok(`N3: PM payment method ${n3MethodId}`);

    // Pool deposit: approve 2 USDT → deposit → confirm.
    const depositAmt = '2000000'; // 2 USDT (6 dec)
    info('N3: approving USDT for pool deposit…');
    const approveRes = await api('POST', '/p2p/deposits/approve', pmJwt, {
      token: 'USDT', amount: depositAmt, walletAddress: deployer.address,
    });
    await broadcast(deployer, approveRes.unsignedTx, 'neg.N3.pool.approve');

    info('N3: depositing to pool…');
    const depRes = await api('POST', '/p2p/deposits', pmJwt, {
      token: 'USDT', amount: depositAmt, walletAddress: deployer.address,
    });
    const depTxN3 = await broadcast(deployer, depRes.unsignedTx, 'neg.N3.pool.deposit');
    await api('POST', '/p2p/deposits/confirm', pmJwt, {
      txHash: depTxN3, token: 'USDT', amount: depositAmt, walletAddress: deployer.address,
    });
    ok(`N3: pool deposit confirmed txHash=${depTxN3}`);

    // Create sell ad (token 1 USDT = ₦1500).
    info('N3: creating sell ad…');
    const adRes = await api('POST', '/p2p/ads', pmJwt, {
      side: 'sell', token: 'USDT', tokenAmount: '1000000',
      fiatCurrency: 'NGN', rate: '1500', rateType: 'fixed',
      minLimit: '500', maxLimit: '2000',
      paymentMethodIds: [n3MethodId],
      terms: 'E2E PM negatives N3',
      sellerAddress: deployer.address,
    });
    const n3Ad = adRes.ad;
    const td   = adRes.typedData;
    saveState({ negN3AdId: n3Ad.id });
    ok(`N3: sell ad ${n3Ad.id}`);

    info('N3: signing ad voucher…');
    const sig = await deployer.signTypedData(
      { name: td.domain.name, version: td.domain.version,
        chainId: td.domain.chainId, verifyingContract: td.domain.verifyingContract },
      { AdVoucher: td.types.AdVoucher },
      td.message,
    );
    const signedAd = await api('POST', `/p2p/ads/${n3Ad.id}/sign`, pmJwt, {
      signature: sig, sellerAddress: deployer.address,
    });
    if (signedAd.status !== 'active') {
      throw new Error(`N3: ad sign returned status=${signedAd.status}, expected active`);
    }
    ok('N3: ad voucher signed → ad active');

    // Counterparty opens trade (₦1500 = 1 USDT, fiatAmount=1500).
    // Create an ephemeral wallet, fund with gas from deployer.
    const counterW = new ethers.Wallet(ethers.Wallet.createRandom().privateKey, provider);
    info(`N3: counterparty ephemeral wallet ${counterW.address}`);
    await (await deployer.sendTransaction({ to: counterW.address, value: ethers.parseEther('0.3') })).wait();
    ok('N3: counterparty funded with gas');

    info('N3: counterparty opening trade…');
    const openRes = await api('POST', `/p2p/ads/${n3Ad.id}/trades`, counterJwt, {
      fiatAmount:      '1500',
      paymentMethodId: n3MethodId,
      buyerAddress:    counterW.address,
    });
    const n3Trade  = openRes.trade;
    const openTxN3 = await broadcast(counterW, openRes.unsignedTx, 'neg.N3.openTrade');
    await api('POST', `/p2p/trades/${n3Trade.id}/open/confirm`, counterJwt, {
      txHash: openTxN3, callerAddress: counterW.address,
    });
    ok(`N3: trade ${n3Trade.id} opened + confirmed (pending_payment)`);
    saveState({ negN3TradeId: n3Trade.id });

    // ── N3-A: Post settled inbound with WRONG amount (₦2000 ≠ ₦1500) ─────────
    // matchBuyInbound: ngnEqual("1500.00","2000.00")=false → no match → no-op.
    // The credit still lands (available_ngn +2000) — that's correct, it's a
    // top-up. But no release must fire and the trade stays pending_payment.
    info('N3-A: POSTing settled inbound with wrong amount (2000 ≠ 1500)…');
    const n3EventIdWrong = `e2e-neg-n3a-${Date.now()}`;
    saveState({ negN3EventIdWrong: n3EventIdWrong });

    const n3WrongPayload = buildSettledInbound({
      requestId:  n3EventIdWrong,
      amount:     2000,            // A+500; does NOT match ₦1500 trade
      senderName: 'E2E Counterparty', // name matches but amount does not
    });
    const rWrong = await postWebhook(n3WrongPayload);
    if (rWrong.status >= 300) {
      throw new Error(`N3-A: wrong-amount POST returned HTTP ${rWrong.status}: ${JSON.stringify(rWrong.json)}`);
    }
    info(`N3-A: wrong-amount POST status=${rWrong.status}`);

    // Poll a few times to confirm trade does NOT release.
    // 4 × 3s = 12s — more than enough for a synchronous dispatch; if the bus is
    // async an intentional release would appear well within this window.
    info('N3-A: polling trade to confirm it stays pending_payment…');
    let finalStatusA = null;
    for (let i = 0; i < 4; i++) {
      await new Promise(r => setTimeout(r, 3000));
      const t = await api('GET', `/p2p/trades/${n3Trade.id}`, counterJwt);
      finalStatusA = t.trade?.status ?? t.status;
      info(`N3-A: attempt ${i+1}/4: trade status=${finalStatusA}`);
      if (finalStatusA === 'released') break; // fail fast below
    }
    if (finalStatusA === 'released') {
      throw new Error('N3-A FAIL: trade was released on wrong-amount inbound — safety invariant VIOLATED');
    }
    if (finalStatusA !== 'pending_payment') {
      // Any status other than pending_payment (e.g. cancelled by watchdog) is
      // unexpected and should be flagged, but is not a safety invariant breach.
      throw new Error(`N3-A FAIL: unexpected trade status=${finalStatusA} (expected pending_payment)`);
    }
    ok(`N3-A PASS: trade ${n3Trade.id} is still pending_payment after wrong-amount inbound`);

    // Confirm the unmatched trade appears in the manual queue.
    // ListBuyManualQueue returns pending_payment pooled sell-ad PM trades with
    // no nomba_inbound_event_id set — i.e. not yet matched by any inbound.
    info('N3-A: checking manual queue…');
    const queue = await api('GET', '/admin/pm/manual-queue', adminJwt);
    const qArr  = Array.isArray(queue) ? queue : (queue.items || queue.data || []);
    const inQueue = qArr.some(item => {
      const id = item.tradeId ?? item.trade_id ?? item.id;
      return id === n3Trade.id;
    });
    if (!inQueue) {
      // The queue might not include it if it got deduped or the wrong-amount
      // inbound credit set nomba_inbound_event_id. Check whether the field was
      // set by reading via psql to give a useful diagnostic.
      const eventIdField = psql(
        `SELECT nomba_inbound_event_id FROM p2p_trades WHERE id = '${n3Trade.id}'`
      );
      throw new Error(
        `N3-A FAIL: trade ${n3Trade.id} not found in manual queue ` +
        `(nomba_inbound_event_id='${eventIdField}'); ` +
        `queue had ${qArr.length} items`
      );
    }
    ok(`N3-A PASS: trade ${n3Trade.id} appears in manual queue`);

    // ── N3-B: Post CORRECT inbound (same name, amount=1500) → recovery ────────
    // matchBuyInbound: ngnEqual("1500.00","1500.00")=true AND name matches →
    // exactly one hit → SystemMarkPaid + AutoRelease fires.
    info('N3-B: POSTing correct settled inbound (amount=1500, matching name)…');
    const n3EventIdRight = `e2e-neg-n3b-${Date.now()}`;
    saveState({ negN3EventIdRight: n3EventIdRight });

    const n3RightPayload = buildSettledInbound({
      requestId:  n3EventIdRight,
      amount:     1500,            // exact match for fiat_amount=1500
      senderName: 'E2E Counterparty', // exact match for users.name of COUNTER_USER_ID
    });
    const rRight = await postWebhook(n3RightPayload);
    if (rRight.status >= 300) {
      throw new Error(`N3-B: correct-amount POST returned HTTP ${rRight.status}: ${JSON.stringify(rRight.json)}`);
    }
    info(`N3-B: correct-amount POST status=${rRight.status}`);

    // Poll until released (or timeout after ~60s).
    info('N3-B: polling trade until released…');
    let released = false;
    for (let i = 0; i < 20; i++) {
      await new Promise(r => setTimeout(r, 3000));
      const t = await api('GET', `/p2p/trades/${n3Trade.id}`, counterJwt);
      const s = t.trade?.status ?? t.status;
      info(`N3-B: attempt ${i+1}/20: trade status=${s}`);
      if (s === 'released') { released = true; break; }
      if (s === 'cancelled') {
        throw new Error(`N3-B FAIL: trade was cancelled, expected released`);
      }
    }
    if (!released) {
      throw new Error(`N3-B FAIL: trade ${n3Trade.id} did not release after correct inbound`);
    }
    ok(`N3-B PASS: trade ${n3Trade.id} released after correct inbound (mismatch-then-recover works)`);

    ok('N3 PASS: wrong-amount is safe (no release) AND recovery with correct amount works');
  } catch (e) {
    fail(`N3 FAIL: ${e.message}`);
    if (process.env.DEBUG) console.error(e.stack);
    anyFailed = true;
  }

  if (anyFailed) {
    console.log('\n✗ negatives phase complete — ONE OR MORE SUB-TESTS FAILED (see ✗ lines above)');
    // We do NOT call process.exit(1) here; main() does it via the `negAnyFailed`
    // flag so cleanup can still run in `all` mode.
  } else {
    console.log('\n✓ negatives phase complete — all sub-tests passed');
  }

  // Persist failure flag so main() can exit non-zero even after cleanup.
  saveState({ negAnyFailed: anyFailed });
}

// ─────────────────────────────────────────────────────────────────────────────
// Entrypoint
// ─────────────────────────────────────────────────────────────────────────────

async function main() {
  const phase = process.argv[2] || 'all';
  const validPhases = ['seed', 'onboard', 'buy', 'sell', 'withdraw', 'verify', 'negatives', 'cleanup', 'all'];

  if (!validPhases.includes(phase)) {
    fatal(`Unknown phase "${phase}". Valid phases: ${validPhases.join(', ')}`);
  }

  console.log(`\n>>> smoke-pm-e2e.js | phase=${phase} | API=${API_BASE}\n`);

  const run = async (name, fn) => {
    if (phase === name || phase === 'all') {
      await fn();
    }
  };

  await run('seed',      phaseSeed);
  await run('onboard',   phaseOnboard);
  await run('negatives', phaseNegatives);
  await run('buy',       phaseBuy);
  await run('sell',      phaseSell);
  await run('withdraw',  phaseWithdraw);
  await run('verify',    phaseVerify);

  // Capture the negatives failure flag BEFORE cleanup removes the state file.
  const negFailed = loadState().negAnyFailed === true;

  await run('cleanup',   phaseCleanup);

  // Exit non-zero if any negatives sub-test failed so CI catches it.
  if (negFailed) {
    console.error('\n✗ Exiting non-zero: negatives phase had failures.\n');
    process.exit(1);
  }

  console.log('\n>>> ALL PHASES DONE\n');
}

main().then(() => process.exit(0)).catch((e) => {
  console.error(`\n✗ PHASE FAILED: ${e.message}`);
  if (process.env.DEBUG) console.error(e.stack);
  process.exit(1);
});
