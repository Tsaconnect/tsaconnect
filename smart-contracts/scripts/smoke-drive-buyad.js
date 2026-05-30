// Drives the BUY-AD flow through the LIVE API on :8090 (isolated tsa_smoke DB),
// up to the broadcast boundary, then decodes the prepared openTradeWithDeposit
// calldata. No funds/gas needed — every funds-moving tx is only PREPARED here.
//   node scripts/smoke-drive-buyad.js
const crypto = require("crypto");
const { ethers } = require("ethers");

const API = "http://localhost:8090/api";
const SECRET = "dev-secret-key-change-in-production";
const ESCROW = "0x1B1ade6459623810Cf158Cd4F7D3FC8E126858f7";
const USDT = "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad";
const MERCHANT_WALLET = "0x1111111111111111111111111111111111111111";
const TAKER_WALLET = "0x2222222222222222222222222222222222222222";

const b64url = (b) => Buffer.from(b).toString("base64").replace(/=/g, "").replace(/\+/g, "-").replace(/\//g, "_");
function mkjwt(payload) {
  const h = b64url(JSON.stringify({ alg: "HS256", typ: "JWT" }));
  const p = b64url(JSON.stringify(payload));
  const sig = b64url(crypto.createHmac("sha256", SECRET).update(h + "." + p).digest());
  return `${h}.${p}.${sig}`;
}
const MERCHANT_JWT = mkjwt({ userId: "11111111-1111-1111-1111-111111111111", exp: 4102444800 });
const TAKER_JWT = mkjwt({ userId: "22222222-2222-2222-2222-222222222222", exp: 4102444800 });

async function call(method, path, token, body) {
  const res = await fetch(API + path, {
    method,
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
    body: body ? JSON.stringify(body) : undefined,
  });
  let json;
  const text = await res.text();
  try { json = JSON.parse(text); } catch { json = { raw: text }; }
  return { status: res.status, json };
}
const fail = (msg, extra) => { console.error("✗ " + msg, extra ? JSON.stringify(extra, null, 2) : ""); process.exit(1); };

(async () => {
  // 1) create merchant payment method
  let r = await call("POST", "/p2p/payment-methods", MERCHANT_JWT, {
    provider: "bank_transfer", country: "NG", currency: "NGN",
    bankName: "Access Bank", bankCode: "044", accountNumber: "0123456789",
    accountName: "Smoke Merchant", isDefault: true,
  });
  if (r.status >= 300 || !r.json?.data?.id) fail(`payment-method create HTTP ${r.status}`, r.json);
  const PM_ID = r.json.data.id;
  console.log("✓ payment method created:", PM_ID);

  // 2) create BUY ad (merchant)
  r = await call("POST", "/p2p/ads", MERCHANT_JWT, {
    side: "buy", token: "USDT", tokenAmount: "10000000", fiatCurrency: "NGN",
    rate: "1500", rateType: "fixed", minLimit: "1000", maxLimit: "15000",
    paymentMethodIds: [PM_ID], terms: "Smoke test buy ad", sellerAddress: MERCHANT_WALLET,
  });
  if (r.status >= 300) fail(`buy-ad create HTTP ${r.status}`, r.json);
  const ad = r.json?.data?.ad || r.json?.data;
  if (!ad?.id) fail("buy-ad create: no ad id", r.json);
  console.log(`✓ buy ad created: ${ad.id} side=${ad.side} status=${ad.status} typedData=${JSON.stringify(r.json?.data?.typedData ?? null)}`);
  if (ad.side !== "buy") fail(`expected side=buy, got ${ad.side}`);
  if (ad.status !== "active") fail(`expected buy-ad active immediately, got ${ad.status}`);

  // 3) open the buy-ad as the TAKER (openTradeWithDeposit prepare)
  r = await call("POST", `/p2p/ads/${ad.id}/trades`, TAKER_JWT, {
    tokenAmount: "10000000", paymentMethodId: PM_ID, sellerAddress: TAKER_WALLET,
  });
  if (r.status >= 300) fail(`open-trade HTTP ${r.status}`, r.json);
  const trade = r.json?.data?.trade;
  const unsignedTx = r.json?.data?.unsignedTx;
  if (!trade?.id || !unsignedTx) fail("open-trade: missing trade or unsignedTx", r.json);
  console.log("✓ trade opened (prepared):");
  console.log("    trade.id        :", trade.id);
  console.log("    funding         :", trade.funding, "(expect deposited_by_taker)");
  console.log("    sellerId(opener):", trade.sellerId, "(expect taker 2222..)");
  console.log("    buyerId(merchant):", trade.buyerId, "(expect merchant 1111..)");
  console.log("    tokenAmount     :", trade.tokenAmount);
  console.log("    fiatAmount      :", trade.fiatAmount, "(expect 15000 = 10 USDT * 1500)");
  console.log("    onChainTradeId  :", trade.onChainTradeId);
  console.log("    status          :", trade.status);

  // 4) decode the prepared openTradeWithDeposit calldata
  console.log("\n  unsignedTx:", JSON.stringify(unsignedTx, null, 2));
  const iface = new ethers.Interface([
    "function openTradeWithDeposit(bytes32 tradeId, address token, uint256 amount, address merchant)",
  ]);
  const data = unsignedTx.data || unsignedTx.Data || unsignedTx.input;
  const to = unsignedTx.to || unsignedTx.To;
  let decoded;
  try { decoded = iface.parseTransaction({ data }); } catch (e) { fail("could not decode calldata as openTradeWithDeposit", { data, err: e.message }); }
  console.log("\n  decoded calldata:");
  console.log("    fn       :", decoded.name);
  console.log("    tradeId  :", decoded.args[0], decoded.args[0].toLowerCase() === String(trade.onChainTradeId).toLowerCase() ? "✓ == trade.onChainTradeId" : "✗ MISMATCH");
  console.log("    token    :", decoded.args[1], decoded.args[1].toLowerCase() === USDT.toLowerCase() ? "✓ == USDT" : "✗ MISMATCH");
  console.log("    amount   :", decoded.args[2].toString(), decoded.args[2] === 10000000n ? "✓ == 10 USDT" : "✗ MISMATCH");
  console.log("    merchant :", decoded.args[3], decoded.args[3].toLowerCase() === MERCHANT_WALLET.toLowerCase() ? "✓ == merchant wallet" : "✗ MISMATCH");
  console.log("    tx.to    :", to, String(to).toLowerCase() === ESCROW.toLowerCase() ? "✓ == escrow proxy" : "✗ MISMATCH");

  const ok = decoded.name === "openTradeWithDeposit" &&
    decoded.args[1].toLowerCase() === USDT.toLowerCase() &&
    decoded.args[2] === 10000000n &&
    decoded.args[3].toLowerCase() === MERCHANT_WALLET.toLowerCase() &&
    String(to).toLowerCase() === ESCROW.toLowerCase();
  console.log("\n" + (ok ? "✓✓ BUY-AD FLOW PREPARED CORRECTLY (calldata valid, role inversion correct)" : "✗ calldata validation failed"));
  process.exit(ok ? 0 : 1);
})().catch((e) => { console.error("FATAL:", e); process.exit(1); });
