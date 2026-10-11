// services/itApi.ts
//
// Client for Instant Transfer (/api/it/*): buy/sell MCGP with Naira via the
// company PM. No client-side signing — the backend operator signs the on-chain
// credit/debit; the app just creates intents and polls status.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders, withWalletProof } from '@/services/walletApi';
import { getActiveWallet } from './wallet';
import type { ITIntentFee } from '@/utils/itIntentView';
import type { PaycrestStatus } from '@/services/paycrestApi';

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

  /** Buy-side delivery target. Resolved server-side from the user's wallet —
   *  never sent by the client. */
  destChain?: string;
  destAddress?: string;

  /** A stablecoin buy the ramp router opened: who fills it. Absent on every
   *  other intent. */
  provider?: 'treasury' | 'flutterwave' | 'tsa_pool';
  /** Set only on a buy opened through the ramp router: when its quote stops
   *  being held. With it, `repriced` means "priced at a live rate instead"
   *  (utils/rampHonour). */
  rateExpiresAt?: string;
  repriced?: boolean;
  /** The quoted breakdown (spec §14): the display rate in minor units and one
   *  line per provider fee (utils/itIntentView). */
  displayRate?: string;
  fees?: ITIntentFee[];
  /** On the intents list, a converted buy's requested asset and chain: what
   *  she receives, where `asset`/`destChain` are its USDC leg. */
  requestedToken?: string;
  requestedChain?: string;
  /** Its conversion record's status (list only): a converted buy's intent
   *  settles when its leg lands, while the bridge still runs. */
  conversionStatus?: string;
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
  /** The `data` a refusal carries, when it carries any — e.g. which order is
   *  already open (see utils/itBuyOrder). Unknown shape: read it through the
   *  helper for its code, never directly. */
  errorData?: unknown;
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
      errorData: json?.data,
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
/** payTo is present only on a buy still waiting for the user's transfer — the
 *  same card POST /it/buy returned. */
export const getITIntent = (id: string) =>
  req<{ intent: ITIntent; payTo?: ITPayTo | null; conversion?: ITConversion }>('GET', `/it/intents/${id}`);

/** The conversion a converted Instant Transfer buy feeds (spec §15.8). */
export interface ITConversion {
  orderId: string;
  status: PaycrestStatus;
  holder?: string;
  requestedToken: string;
  requestedChain: string;
  requestedChainId?: number;
  requestedTokenDecimals?: number;
  estimatedAmount?: string;
  promisedAmount?: string;
  markupBps: number;
  fee?: ITIntentFee;
  walletAddress: string;
  destinationAddress?: string;
}

export interface ITPayTo {
  accountNumber: string;
  bank: string;
  accountName: string;
  /** Central mode only: the whole-naira figure the matcher compares against. */
  amountNgn?: string;
  /** Central mode only: own-name / recipient-name notice. See utils/payInto. */
  payerInstruction?: string;
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
// A second open order is refused with code it_buy_order_open (409) naming the
// open one — see utils/itBuyOrder.openBuyOrderOf.
export const itBuy = async (ngnKobo: string, walletAddress?: string) => {
  const resolvedWallet = walletAddress || (await getActiveWallet()) || undefined;
  // A device wallet other than the linked one is only accepted once proven.
  return withWalletProof(() => req<ITBuyResult>('POST', '/it/buy', { ngnKobo, walletAddress: resolvedWallet }));
};

/** Abandon an unpaid buy order (MCGP or stablecoin). Refused with
 *  it_buy_payment_arrived when the payment already appears to be in. */
export const cancelITBuy = (intentId: string) =>
  req<{ cancelled: boolean; intent?: ITIntent }>('POST', `/it/buy/${intentId}/cancel`);

/** "I've paid": asks the API to look for this buy's transfer now rather than on
 *  its next scheduled poll. `checked` is false when a lookup had just run or
 *  was still running — the returned intent is current either way. */
export const checkITBuyPayment = (intentId: string) =>
  req<{ intent: ITIntent; payTo?: ITPayTo; checked: boolean; conversion?: ITConversion }>(
    'POST',
    `/it/buy/${intentId}/check-payment`
  );

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

// Machine code: the live price cannot be computed right now (stale/missing NGN
// rate or unpriceable fees). Transient and nothing moved — show a retry that
// keeps the user's inputs, never a dead-end failure.
export const IT_PRICING_UNAVAILABLE = 'it_pricing_unavailable';

// GET /api/it/sell/quote — the MCGP sell priced by the sell's own server path.
export interface ITSellQuote {
  mode: 'mcgp' | 'receive';
  currency: string;
  mcgpAmount: string; // wei
  rateKobo: string;
  grossKobo: string;
  feeKobo: string;
  netKobo: string; // what lands in the bank — exact, not an estimate
  feeQuote: ITFeeQuote;
}

// Exactly one of mcgpAmount (wei: "what does selling this pay?") or
// receiveKobo ("the smallest MCGP that pays this much after fees").
export const getITSellQuote = (
  amount: { mcgpAmount: string } | { receiveKobo: string },
  currency = 'NGN',
) => {
  const q = new URLSearchParams({ ...amount, currency });
  return req<ITSellQuote>('GET', `/it/sell/quote?${q.toString()}`);
};

// Optional payout destination for /it/sell. Omit entirely (or leave every
// field empty) for the legacy NGN manual-payout-queue path — unchanged
// behavior. When destinationCurrency/railType are set they're validated
// against the withdraw-destinations catalog server-side before the intent is
// created.
export interface ITSellOptions {
  walletAddress?: string;
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
export const itSell = async (mcgpAmount: string, opts: ITSellOptions = {}) => {
  const walletAddress = opts.walletAddress || (await getActiveWallet()) || undefined;
  // The operator debits the account's wallet, so it must be one the account proved.
  return withWalletProof(() => req<{ intent: ITIntent }>('POST', '/it/sell', { mcgpAmount, ...opts, walletAddress }));
};

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

// ──────────────────────────── stablecoin buy (brokered) ───────────────────
//
// Users buy USDT/USDC with naira. They pay into their own standing virtual
// account; Flutterwave converts and delivers to an address WE resolve from the
// database — never one the caller sends. See
// docs/superpowers/specs/2026-08-31-stablecoin-buy-app-design.md.

/** Machine codes the buy endpoints return. Branch on these, not messages. */
export const IT_STABLE_BUY_ALREADY_OPEN = 'stable_buy_already_open';
export const IT_STABLE_BUY_NO_VA = 'stable_buy_no_va';
export const IT_STABLE_BUY_NO_WALLET = 'stable_buy_no_wallet';
export const IT_STABLE_NOT_CONFIGURED = 'stable_not_configured';

export interface ITStableBuyQuote {
  asset: string;
  currency: string;
  /** What the user pays, in fiat MINOR units. */
  fiatMinor: string;
  /** Asset base units (6dp) before the company spread. */
  grossUnits: string;
  /** Asset base units (6dp) the user receives — what to display. */
  assetUnits: string;
  /** Fiat minor units per one whole asset unit, spread applied. */
  ratePerUnit: string;
  expiresAt: string;
  /** Always true today: the binding price is struck when the naira lands.
   *  The screen keys off this rather than assuming. */
  indicative: boolean;
}

/** Where the user sends their naira: their own standing virtual account.
 *
 *  NOT ITPayTo. That type names the field `bank`; this endpoint returns
 *  `bankName` (StableBuyInstructions, it_stable_buy.go). Reusing ITPayTo
 *  compiles and renders an empty bank at runtime. */
export interface ITStableBuyPayInto {
  accountNumber: string;
  bankName: string;
  accountName: string;
  /** VA mode only — nothing minted survives a transfer into the central account. */
  reference?: string;
  /** Central mode only: the naira to send, as a decimal string. */
  amountNgn?: string;
  /** Central mode only: own-name / recipient-name notice. See utils/payInto. */
  payerInstruction?: string;
}

export interface ITStableBuyResult {
  intent: ITIntent;
  payInto: ITStableBuyPayInto;
}

/** Indicative price. amountMinor is fiat MINOR units as a string. */
export const quoteStableBuy = (asset: string, amountMinor: string, currency: string) =>
  req<ITStableBuyQuote>('POST', '/it/stable/buy/quote', {
    asset,
    amount: amountMinor,
    currency,
  });

/** Opens a purchase. Pass a stable key so a retry of the SAME create is
 *  deduped server-side rather than minting a second purchase. */
export const createStableBuy = (
  asset: string,
  amountMinor: string,
  currency: string,
  destChain: string,
  idempotencyKey?: string
) =>
  req<ITStableBuyResult>(
    'POST',
    '/it/stable/buy',
    { asset, amount: amountMinor, currency, destChain },
    { idempotencyKey }
  );

/** Chains this user can actually receive `asset` on. A chain they hold no
 *  address for is never returned, so it can never be offered. */
export const getStableBuyChains = (asset: string) =>
  req<{ chains: string[] }>('GET', `/it/stable/buy/chains?asset=${encodeURIComponent(asset)}`);

/** Abandon an unpaid purchase. Without this the user is blocked from buying
 *  until the intent expires 24h later. */
export const cancelStableBuy = (intentId: string) =>
  req<{ cancelled: boolean }>('POST', `/it/stable/buy/${intentId}/cancel`);

// ──────────────────────────── currency ramp table ─────────────────────────
//
// Source of truth for which currency routes to which provider. Replaces a
// hardcoded NGN/GHS switch in app/wallet/it/index.tsx — adding a currency is
// now a backend registry change, not an app release.

export type RampProvider = 'paycrest' | 'flutterwave' | '';

/** One currency the ramp knows about. rampProvider is '' when the stablecoin
 *  ramp is unavailable in it — the app shows that rather than hiding it. */
export interface RampCurrency {
  code: string;
  exponent: number;
  rampProvider: RampProvider;
  /** Whether this currency can trade in each direction RIGHT NOW. A currency
   *  can have a provider and still trade one way only — UGX and TZS have
   *  offramp providers and no onramp — so the two are separate questions.
   *  Derived server-side from the live Paycrest order book, not configured. */
  rampBuy?: boolean;
  rampSell?: boolean;
  depositEnabled: boolean;
  payoutEnabled: boolean;
  minPayoutMinor: number;
}

/** Which directions a currency trades in. */
export interface RampLegs {
  buy: boolean;
  sell: boolean;
}

export async function getRampCurrencies(): Promise<RampCurrency[]> {
  const res = await req<RampCurrency[]>('GET', '/it/currencies');
  return res.success && res.data ? res.data : [];
}

/** Who serves the ramp for a currency, or '' for none.
 *
 *  Defaults to '' for an unknown code and for an empty table, so a failed or
 *  in-flight fetch shows "unavailable" rather than offering a trade the
 *  backend would refuse. */
export function rampProviderFor(list: RampCurrency[], code: string): RampProvider {
  return list.find((c) => c.code === code)?.rampProvider ?? '';
}

/** Which directions a currency can trade in right now.
 *
 *  No provider means no legs, whatever else the row says. Beyond that the
 *  fields are optional on purpose: an older API build does not send them, and
 *  an app that read a missing field as "closed" would blank out the working
 *  NGN ramp the moment it talked to one. Absent therefore means "both", which
 *  is what this app assumed before directions existed — the order endpoints
 *  still refuse a leg no provider can fill. */
export function rampLegsFor(list: RampCurrency[], code: string): RampLegs {
  const row = list.find((c) => c.code === code);
  if (!row || !row.rampProvider) return { buy: false, sell: false };
  return { buy: row.rampBuy ?? true, sell: row.rampSell ?? true };
}
