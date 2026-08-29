// services/itApi.ts
//
// Client for Instant Transfer (/api/it/*): buy/sell MCGP with Naira via the
// company PM. No client-side signing — the backend operator signs the on-chain
// credit/debit; the app just creates intents and polls status.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export interface ITRate {
  buyRateKobo: string; // kobo per 1 MCGP
  sellRateKobo: string;
}

export interface ITBalance {
  premiumBought: string; // MCGP wei (USDC-bought, self-exitable)
  premiumCredited: string; // MCGP wei (IT/NGN, spendable)
  pendingSettle: string; // MCGP wei (in-flight NGN sell)
}

export interface ITIntent {
  id: string;
  direction: 'buy' | 'sell';
  status: string;
  mcgpAmount: string;
  ngnKobo: string;
  rateKobo: string;
  /** Fees withheld from this leg, and what actually moves (kobo). Absent on
   *  intents created before fee deduction shipped. */
  feeNgnKobo?: string;
  netNgnKobo?: string;
  paymentRef?: string;
  txHash?: string;
  failureReason?: string;
  createdAt: string;

  /** Stablecoin ramp. Absent on MCGP intents, which is why every field is
   *  optional — the same row type serves both flows. */
  asset?: string;
  assetAmount?: string;
  currency?: string;
  sourceChain?: string;
  depositTxHash?: string;
  receivedAmount?: string;
  netMinor?: string;
  /** Flutterwave's address this sell was quoted against. Persisted server-side
   *  so an abandoned sell can be resumed without minting a second intent. */
  depositAddress?: string;
  conversionAttempts?: number;
}

// code carries the backend's machine error code (e.g. "it_agreement_required")
// so callers can branch on it instead of matching message strings.
export type ApiResult<T> = {
  success: boolean;
  data?: T;
  message?: string;
  code?: string;
  /** HTTP status, so callers can distinguish a user-fixable 400 from a
   *  retryable 503. Absent when the request never reached the server. */
  status?: number;
};

/** RFC 4122 v4 where available; same approach as services/p2p.ts.
 *  Exported because a caller may need to hold ONE key across retries of the
 *  same logical create (see the stablecoin sell screen). */
export function makeIdempotencyKey(): string {
  const g: any = globalThis as any;
  if (g.crypto?.randomUUID) return g.crypto.randomUUID();
  return `it-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
}

async function req<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: { idempotencyKey?: string } = {}
): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    // Non-GET calls create or move money; a network-level retry must not
    // produce a second intent. Mirrors services/p2p.ts.
    if (method !== 'GET') {
      headers['Idempotency-Key'] = opts.idempotencyKey ?? makeIdempotencyKey();
    }
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method,
      headers,
      body: body == null ? undefined : JSON.stringify(body),
    });
    let json: any = null;
    try {
      json = await res.json();
    } catch {
      /* empty */
    }
    if (json?.success) return { success: true, data: json.data as T, status: res.status };
    return {
      success: false,
      message: json?.message || `Request failed (${res.status})`,
      code: json?.code,
      status: res.status,
    };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// Backend machine code returned by the trade endpoints when the user has not
// accepted the current trading agreement.
export const IT_AGREEMENT_REQUIRED = 'it_agreement_required';

// Trading agreement — must be accepted (current version) before the first IT
// transaction. getITAgreement returns the text + whether the caller has agreed.
export interface ITAgreement {
  version: string;
  title: string;
  intro: string;
  clauses: string[];
  accepted: boolean;
}
export const getITAgreement = () => req<ITAgreement>('GET', '/it/agreement');
export const acceptITAgreement = () =>
  req<{ version: string; accepted: boolean }>('POST', '/it/agreement/accept');

export const getITRate = () => req<ITRate>('GET', '/it/rate');
export const getITBalance = () => req<ITBalance>('GET', '/it/balance');
export const listITIntents = () => req<{ intents: ITIntent[] }>('GET', '/it/intents');
export const getITIntent = (id: string) => req<{ intent: ITIntent }>('GET', `/it/intents/${id}`);

export interface ITPayTo {
  accountNumber: string;
  bank: string;
  accountName: string;
  /** @deprecated Spec 1 standing VA — no user-typed reference */
  reference?: string;
}

export interface ITBuyQuote {
  /** What the user transfers, in kobo. */
  ngnKobo: string;
  /** MCGP credited — already NET of feeNgnKobo. */
  mcgpAmount: string;
  rateKobo: string;
  /** Naira withheld as fees; the MCGP it would have bought is never minted. */
  feeNgnKobo?: string;
  netNgnKobo?: string;
}

export interface ITBuyResult {
  needsActivation: boolean;
  quote: ITBuyQuote;
  payTo: ITPayTo | null;
  intent?: ITIntent | null;
}

// ngnKobo = naira * 100 (string). Opens a pending buy order + standing VA
// details. needsActivation=true when the user has no VA yet.
export const itBuy = (ngnKobo: string) =>
  req<ITBuyResult>('POST', '/it/buy', { ngnKobo });

export interface ITVA {
  id: string;
  currency: string;
  accountNumber: string;
  bankName: string;
  accountName: string;
  status: string;
}

export const getITVA = () =>
  req<{ va: ITVA; payTo: ITPayTo }>('GET', '/it/va');

// One-time BVN/NIN gate — idNumber is never stored client-side beyond the request.
export const activateITVA = (idType: 'BVN' | 'NIN', idNumber: string) =>
  req<{ va: ITVA; payTo: ITPayTo }>('POST', '/it/va/activate', { idType, idNumber });

// One entry from GET /it/withdraw/destinations — product catalog of
// currency/rail (and, for mobile_money, network) combinations IT withdrawal
// supports. Currently NGN/bank only until multi-currency sell is re-enabled.
export interface WithdrawDestination {
  currency: string;
  rail: 'bank' | 'mobile_money' | string;
  network?: string; // mobile_money only, e.g. "wave"
  label: string;
}

// GET /api/it/withdraw/destinations — product catalog (NGN/bank only until
// multi-currency sell is re-enabled). Single source of truth also used to
// validate the sell submission server-side.
export const getWithdrawDestinations = () =>
  req<{ destinations: WithdrawDestination[] }>('GET', '/it/withdraw/destinations');

export interface WithdrawQuote {
  destCurrency: string;
  destAmount: number;
  rate: number;
  stale: boolean;
}

// GET /api/it/withdraw/quote?ngn=<amt>&currency=<ISO> — live FX preview for a
// non-NGN destination. With the NGN-only product catalog this is unused by
// the app; kept for when foreign corridors reopen.
export const quoteWithdrawal = (ngn: number, currency: string) =>
  req<WithdrawQuote>('GET', `/it/withdraw/quote?ngn=${encodeURIComponent(ngn)}&currency=${encodeURIComponent(currency)}`);

// Fee line from GET /api/it/quote — minor units + display string.
export interface ITFeeLine {
  minor: string;
  display: string;
}

// Itemized deposit/withdrawal fee quote (MCGP ↔ NGN). Quote-only; never moves money.
export interface ITFeeQuote {
  flow: 'deposit' | 'withdraw' | string;
  asset: string;
  currency: string;
  amount: ITFeeLine;
  platformFee: { usdCents: number; usd: string; inCurrency: ITFeeLine };
  providerFee: { fee: ITFeeLine; source: string; fallbackBps?: number };
  tax: {
    basis: string;
    base: ITFeeLine;
    vatBps: number;
    vat: ITFeeLine;
    platformTaxBps: number;
    platformTax: ITFeeLine;
    total: ITFeeLine;
  };
  swapFee: { fee: ITFeeLine; bps: number; legs?: string[] };
  totalFees: ITFeeLine;
  netAmount: ITFeeLine;
  viable: boolean;
  assetOutEstimate?: string | null;
}

// GET /api/it/quote?flow=&asset=&amount=&currency= — itemized fee breakdown
// shown on sell Review (and available for buy). Amount is a positive decimal
// in `currency` (NGN for IT), max 2dp.
export const getITFeeQuote = (params: {
  flow: 'deposit' | 'withdraw';
  asset: 'MCGP' | 'USDC' | 'USDT';
  amount: string;
  currency?: string;
  chain?: string;
}) => {
  const q = new URLSearchParams({
    flow: params.flow,
    asset: params.asset,
    amount: params.amount,
    currency: params.currency || 'NGN',
  });
  if (params.chain) q.set('chain', params.chain);
  return req<{ quote: ITFeeQuote }>('GET', `/it/quote?${q.toString()}`);
};

// Optional payout destination for /it/sell. Omit entirely (or leave every
// field empty) for the legacy NGN manual-payout-queue path — unchanged
// behavior. When destinationCurrency/railType are set they're validated
// against the withdraw-destinations catalog server-side before the intent is
// created.
export interface ITSellOptions {
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  destinationCurrency?: string;
  railType?: 'bank' | 'mobile_money';
  mobileNetwork?: string; // mobile_money only, from the selected catalog entry
  mobileCountry?: string; // mobile_money only, ISO-2 e.g. "SN"
  mobileMsisdn?: string; // mobile_money only
}

// mcgpAmount is base-unit wei (string) — semantics unchanged. `opts` carries
// the optional payout destination; omitted, this is exactly the legacy
// NGN-bank call.
export const itSell = (mcgpAmount: string, opts: ITSellOptions = {}) =>
  req<{ intent: ITIntent }>('POST', '/it/sell', { mcgpAmount, ...opts });

// ──────────────────────────── stablecoin sell (brokered) ──────────────────
//
// Users sell USDT/USDC for fiat. Their stablecoin goes straight from their own
// wallet to Flutterwave's Polygon address — the platform is a broker, holds no
// float, never custodies. This is the transport layer for that flow; the
// money math lives elsewhere.

export type StableAsset = 'USDC' | 'USDT';

/** Machine code: this deposit hash is already claimed by another intent. */
export const IT_STABLE_DEPOSIT_CLAIMED = 'stable_deposit_already_claimed';

// All money fields are minor-unit integer STRINGS — they stay strings until
// they are formatted for display.
export interface StableQuote {
  asset: string;
  currency: string;
  /** The asset amount the quote was priced for, as a STRING — the handler
   *  emits fmt.Sprintf("%.6f", …) (it_handler.go:716), not a JSON number.
   *  Typing it `number` would make `q.amount.toFixed(...)` a runtime crash. */
  amount: string;
  gross: string;
  afterSpread: string;
  fee: string;
  net: string;
  rate: string;
  expiresAt: string;
}

/** Where the user must send their stablecoin. This is FLUTTERWAVE's address —
 *  never one the platform controls, and never hardcoded in the app. */
export interface StableDeposit {
  chain: string;
  address: string;
  asset: string;
  /** The exact ERC-20 contract on Polygon that ConfirmStableDeposit will
   *  verify the arrival against (s.stableTokens[asset], it_stable_sell.go:361).
   *  NOT optional and NOT resolvable app-side: Polygon carries both native
   *  USDC and bridged USDC.e, and delivering the other one leaves the deposit
   *  unverifiable with the funds already at Flutterwave. The backend fails
   *  closed (ErrStableNoTokenAddress) rather than return this empty, so if a
   *  create succeeded this field is populated. */
  tokenAddress: string;
  amount: string;
}

export interface StableSellBank {
  code: string;
  number: string;
  name: string;
}

// `currency` is binding:"required" on both requests (it_handler.go:675).
// `{ currency: 'NGN', ...body }` does NOT default it: an explicit
// `currency: undefined` in `body` overrides the default back to undefined,
// JSON.stringify drops the key, and the server 400s. `?? 'NGN'` defaults it
// for real.
export const quoteStableSell = (body: {
  asset: StableAsset;
  amount: string;
  currency?: string;
}) =>
  req<StableQuote>('POST', '/it/stable/quote', {
    ...body,
    currency: body.currency ?? 'NGN',
  });

export const createStableSell = (body: {
  asset: StableAsset;
  amount: string;
  currency?: string;
  sourceChain: string;
  bank: StableSellBank;
  /** Pass a stable key so a retry of the SAME create is deduped server-side. */
  idempotencyKey?: string;
}) => {
  const { idempotencyKey, ...payload } = body;
  return req<{ intent: ITIntent; deposit: StableDeposit }>(
    'POST',
    '/it/stable/sell',
    { ...payload, currency: payload.currency ?? 'NGN' },
    { idempotencyKey }
  );
};

export const reportStableDeposit = (intentId: string, txHash: string) =>
  req<{ recorded: boolean }>('POST', `/it/stable/sell/${intentId}/deposit`, { txHash });
