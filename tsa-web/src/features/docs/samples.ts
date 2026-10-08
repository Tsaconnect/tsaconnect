// The code samples on /docs. Every field, header and value is taken from
// tsa-api-go: CreatePayPaymentInput and PayPaymentView (services), the v1 routes
// (routes.go, pay_handler.go), the webhook body (enqueueWebhook) and its
// signature (SignPayWebhook: t=<ts>,v1=<hex HMAC-SHA256(secret, "<ts>.<body>")>).

export const BASE_URL = 'https://tsa.mcgpchain.com/api/pay/v1';

export const createCurl = `curl ${BASE_URL}/payments \\
  -H "Authorization: Bearer $TSA_PAY_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "amount": 8450000,
    "currency": "NGN",
    "reference": "PNR-K8Q2ZL",
    "description": "LOS-ABV 12 Oct, 1 adult",
    "returnUrl": "https://skyair.ng/booking/K8Q2ZL",
    "expiresIn": 1800
  }'`;

export const createNode = `const res = await fetch('${BASE_URL}/payments', {
  method: 'POST',
  headers: {
    Authorization: \`Bearer \${process.env.TSA_PAY_KEY}\`,
    'Content-Type': 'application/json',
  },
  body: JSON.stringify({
    amount: 8450000, // ₦84,500.00, in kobo
    currency: 'NGN',
    reference: booking.pnr, // yours; retrying with the same body returns the same payment
    description: 'LOS-ABV 12 Oct, 1 adult',
    returnUrl: \`https://skyair.ng/booking/\${booking.pnr}\`,
  }),
});
// A proxy or gateway error can answer with an HTML page: check before parsing.
if (!(res.headers.get('content-type') ?? '').includes('application/json')) {
  throw new Error(\`TSA Pay \${res.status}: not a JSON answer\`);
}
const body = await res.json();
if (!res.ok || !body.success) {
  throw new Error(\`TSA Pay \${res.status} \${body.code ?? ''}: \${body.message}\`);
}
const payment = body.data; // send the customer to payment.checkoutUrl (or payment.appUrl)`;

export const createResponse = `{
  "success": true,
  "message": "Payment created",
  "data": {
    "id": "3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f",
    "reference": "PNR-K8Q2ZL",
    "mode": "live",
    "status": "pending",
    "amount": 8450000,
    "customerTotal": 8535354,
    "merchantNet": 8450000,
    "fee": 85354,
    "currency": "NGN",
    "description": "LOS-ABV 12 Oct, 1 adult",
    "returnUrl": "https://skyair.ng/booking/K8Q2ZL",
    "expiresAt": "2026-09-24T16:31:00Z",
    "appUrl": "tsaconnect://pay?id=3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f",
    "checkoutUrl": "https://tsaconnectworld.com/pay/3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f",
    "createdAt": "2026-09-24T16:01:00Z"
  }
}`;

export const retrieveCurl = `# Read one payment
curl ${BASE_URL}/payments/3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f \\
  -H "Authorization: Bearer $TSA_PAY_KEY"

# Cancel it while it is still pending
curl -X POST ${BASE_URL}/payments/3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f/cancel \\
  -H "Authorization: Bearer $TSA_PAY_KEY"`;

// Top-level keys in the order Go marshals the event map: sorted.
export const webhookBody = `{
  "created": 1790265780,
  "data": {
    "id": "3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f",
    "reference": "PNR-K8Q2ZL",
    "mode": "live",
    "status": "succeeded",
    "amount": 8450000,
    "customerTotal": 8535354,
    "merchantNet": 8450000,
    "fee": 85354,
    "currency": "NGN",
    "description": "LOS-ABV 12 Oct, 1 adult",
    "returnUrl": "https://skyair.ng/booking/K8Q2ZL",
    "expiresAt": "2026-09-24T16:31:00Z",
    "settlementRef": "c41e7b09-2f6a-4d85-b3e1-7a9d0c5f2e18",
    "succeededAt": "2026-09-24T16:03:00Z",
    "appUrl": "tsaconnect://pay?id=3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f",
    "checkoutUrl": "https://tsaconnectworld.com/pay/3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f",
    "createdAt": "2026-09-24T16:01:00Z"
  },
  "id": "b7e0c1d2-5a4f-4c3e-8d2b-9f1a6e7c3d40",
  "livemode": true,
  "type": "payment.succeeded"
}`;

export const verifyNode = `import crypto from 'node:crypto';

// header: the X-TSA-Signature value, "t=<timestamp>,v1=<signature>".
// rawBody: the request body exactly as received, before any JSON parsing.
// secret: your whsec_… signing secret for this mode (live and test differ).
export function verifyTsaSignature(rawBody, header, secret, toleranceSeconds = 300) {
  const parts = {};
  for (const item of String(header ?? '').split(',')) {
    const at = item.indexOf('=');
    if (at > 0) parts[item.slice(0, at).trim()] = item.slice(at + 1).trim();
  }
  const t = parts.t;
  const ts = Number(t);
  if (!t || !Number.isInteger(ts) || Math.abs(Date.now() / 1000 - ts) > toleranceSeconds) return false;
  const expected = crypto.createHmac('sha256', secret).update(\`\${t}.\${rawBody}\`).digest();
  const given = Buffer.from(parts.v1 ?? '', 'hex');
  return given.length === expected.length && crypto.timingSafeEqual(given, expected);
}`;

export const expressNode = `import express from 'express';

const app = express();

// express.raw keeps the body as bytes: the signature is over them, not over re-serialised JSON.
app.post('/tsa/webhook', express.raw({ type: 'application/json' }), async (req, res) => {
  const rawBody = req.body.toString('utf8');
  if (!verifyTsaSignature(rawBody, req.get('X-TSA-Signature'), process.env.TSA_WEBHOOK_SECRET)) {
    return res.sendStatus(400);
  }
  const event = JSON.parse(rawBody);
  // A delivery can repeat: store each event id once (e.g. INSERT … ON CONFLICT (id) DO NOTHING).
  const isNew = await saveEventOnce(event.id, event);
  res.sendStatus(200); // any 2xx within 5 seconds counts as delivered
  if (isNew) await fulfil(event.type, event.data);
});`;

export const errorBody = `{
  "success": false,
  "message": "invalid API key",
  "code": "invalid_key"
}`;

// GET /payments?reference= — the lookup by the merchant's own reference (FindPaymentByReference).
export const findByReferenceCurl = `curl -G ${BASE_URL}/payments \\
  --data-urlencode "reference=PNR-K8Q2ZL" \\
  -H "Authorization: Bearer $TSA_PAY_KEY"`;
