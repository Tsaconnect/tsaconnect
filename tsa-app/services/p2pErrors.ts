// services/p2pErrors.ts
//
// Turn raw P2P API / RPC errors into sentences a user can act on (R10). The
// API's sentinels live in tsa-api-go/internal/services/p2p_trade_service.go
// ("p2p: …"). Anything unmatched goes through submitErrors.mapSubmitError, the
// shared guard against stack traces, HTML and SQL reaching the screen.
import { decodeRevertError } from './txErrors';
import { mapSubmitError, TRANSFER_COPY } from './submitErrors';
import { KYC_LEGAL_NAME_MISSING, KYC_VERIFICATION_REQUIRED, kycRefusalFor } from '@/utils/kycRefusal';

export interface P2PErrorLike {
  message?: string;
  status?: number;
  code?: string;
  /** Structured detail some codes carry, e.g. rate_out_of_band's minRate/maxRate. */
  data?: unknown;
}

const CODE_COPY_DIRECT_MOVED = 'Direct P2P has moved to BNB Chain. Choose BNB Chain as the network and try again.';

const CODE_COPY: Record<string, string> = {
  app_update_required: 'Please update the app to continue this trade.',
  [KYC_VERIFICATION_REQUIRED]: 'Verify your identity to use P2P.',
  // Verified, but no legal name: only support can fix it (kycRefusal's P2P copy).
  [KYC_LEGAL_NAME_MISSING]: kycRefusalFor(KYC_LEGAL_NAME_MISSING, undefined, 'p2p')!.message,
  gas_not_needed: "This step doesn't need gas right now.",
  gas_topup_unused: 'A gas top-up was already sent recently. Use it first, then try again.',
  sponsorship_in_progress: 'A gas top-up is already in progress. Give it a moment.',
  gas_unavailable: 'The gas top-up is unavailable right now. Try again in a minute.',
  WALLET_PROOF_REQUIRED: 'Prove ownership of your wallet before continuing.',
  // 409 on new activity on Sonic (Sonic keeps Payment Merchant trades only).
  p2p_direct_moved: CODE_COPY_DIRECT_MOVED,
  // 409: a Payment Merchant posting on BNB Chain direct P2P (PMs stay on Sonic).
  p2p_pm_not_on_direct: 'Payment Merchant ads stay on Sonic. Choose Sonic as the network and try again.',
};

// A client-side timeout says nothing about the outcome: the request may have
// landed. Only status-less errors qualify (a server reply is a real answer);
// "confirmation timed out" is services/txWait.ts's waitMined message.
const CLIENT_TIMEOUT = /^request timed out|etimedout|confirmation timed out/i;
const TIMEOUT_COPY =
  'We could not confirm this in time. It may still go through, so check the trade before trying again.';

const RULES: [RegExp, string][] = [
  [/network request failed|^network error$|failed to fetch/i, "Can't reach TSA Connect. Check your connection and try again."],
  [/rate source is stale/i, 'The live rate is unavailable right now. Try again in a few seconds.'],
  [/blockchain temporarily unavailable/i, 'The network is busy right now. Try again in a minute.'],
  [/bad voucher signature|ad is not active|no longer available for new p2p/i, 'This ad is no longer available.'],
  [/amount outside ad min\/max/i, "That amount is outside this ad's limits."],
  [/exceeds ad remaining|available balance can no longer cover/i, "This ad can't cover that amount any more. Try a smaller amount or another ad."],
  [/cannot open a trade on their own ad/i, "You can't trade on your own ad."],
  [/already have an active trade on this ad/i, 'You already have a trade on this ad — find it in My trades.'],
  // services/txWait.ts's waitMined label for the escrow approve. A timeout is
  // not a failure: the approve may still land, and a retry finds the allowance.
  [/approve confirmation timed out/i, "Your approval was sent but hasn't confirmed yet. It may still go through — wait a moment and try again; you may not need to approve again."],
  [/invalid state transition/i, 'This trade has already moved on. Pull down to refresh.'],
  [/already has an open dispute/i, 'A dispute is already open or being raised for this trade — check back in a few minutes.'],
  [/not authorized for this action/i, "You can't do that on this trade."],
  [/trade not found|ad not found/i, "We couldn't find that. It may have been removed."],
  [/payment method not found/i, 'Pick a payment method and try again.'],
  [/direct p2p has moved/i, CODE_COPY_DIRECT_MOVED],
  [/token not supported on this chain/i, "This token isn't supported on that network."],
  [/ad has open trades/i, 'This ad has trades in progress. Wait for them to finish first.'],
  // ErrPoolStockAdExists (p2p_pool_ad.go): the LP's stock ad is live and not due.
  [/already have a live stock ad/i, 'You already have a live pool ad. You can renew it when it is due.'],
];

// The escrow's party checks, as a revert from the server's gas estimate (the
// estimate runs as the signing wallet): V1/V2 strings "Only buyer"/"Only
// seller", V3 custom errors OnlyBuyer()/OnlySeller()/NotAParty() by name or
// selector. On a trade it means the active wallet is not the one that opened it.
const WRONG_WALLET = /\bonly ?(buyer|seller)\b|\bNotAParty\b|0x86efbb55|0x85d1f726|0xc749b689/i;
const WRONG_WALLET_COPY =
  'This trade belongs to a different wallet. Switch to the wallet you used for this trade and try again.';

function rateBandCopy(data: unknown): string {
  const { minRate, maxRate } = (data ?? {}) as { minRate?: unknown; maxRate?: unknown };
  if (minRate != null && maxRate != null) return `Rate must be between ${minRate} and ${maxRate}.`;
  return 'That rate is outside the allowed range. Adjust it and try again.';
}

export function p2pErrorMessage(
  res: P2PErrorLike | string | null | undefined,
  fallback = 'Something went wrong. Please try again.',
): string {
  const r: P2PErrorLike = typeof res === 'string' ? { message: res } : res ?? {};
  const raw = r.message ?? '';
  if (r.code === 'rate_out_of_band') return rateBandCopy(r.data);
  if (r.code && CODE_COPY[r.code]) return CODE_COPY[r.code];
  // An older code-less KYC 403 still gets the KYC copy, not its own text.
  const kyc = p2pKycCode(r);
  if (kyc) return CODE_COPY[kyc];
  if (r.status === 401) return 'Your session has expired. Sign in again.';
  if (r.status === 410) return 'Your app version is out of date — please update and retry.';
  if (r.status === 429) {
    return /used today's gas top-ups/i.test(raw)
      ? "You've hit the daily limit for gas top-ups. Try again tomorrow."
      : 'Too many requests. Wait a moment and try again.';
  }
  if (/revert/i.test(raw) && WRONG_WALLET.test(raw)) return WRONG_WALLET_COPY;
  for (const [re, copy] of RULES) if (re.test(raw)) return copy;
  if (r.status == null && CLIENT_TIMEOUT.test(raw)) return TIMEOUT_COPY;
  if (decodeRevertError(raw)) {
    return 'The network would reject this transaction, so nothing was sent. Pull down to refresh and try again.';
  }
  const stripped = raw.replace(/^p2p(?: chat)?:\s*/i, '');
  const tidy = stripped.charAt(0).toUpperCase() + stripped.slice(1);
  return mapSubmitError(tidy, { fallback, retry: TRANSFER_COPY.retry });
}

/** The message of anything a catch receives (an Error, a string, a refusal). */
export function errorMessage(err: unknown): string {
  return (err as { message?: string } | undefined)?.message ?? String(err);
}

/** An error whose message is app copy written for the user (not a raw API or
 *  RPC string), so p2pCaughtMessage shows it as-is: re-mapping could swap a
 *  long, specific sentence for the generic fallback. */
export class P2PCopyError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'P2PCopyError';
  }
}

/** A refusal to throw: `message` is copy (shown as-is), and the error keeps
 *  the machine fields a screen routes on — the KYC code, status and data
 *  (rate_out_of_band's min/max). A bare Error would drop them. */
export function p2pRefusalError(res: P2PErrorLike, message: string): P2PCopyError {
  return Object.assign(new P2PCopyError(message), {
    code: p2pKycCode(res) ?? res.code,
    status: res.status,
    data: res.data,
    raw: res.message,
  });
}

/** The unmapped server/RPC text behind a mapped error (its `raw`), else its
 *  message. Only for decoders that need the original string (e.g. an ERC20
 *  revert's hex) — never to show. */
export function rawErrorText(err: unknown): string {
  const raw = (err as { raw?: unknown } | null | undefined)?.raw;
  return typeof raw === 'string' ? raw : errorMessage(err);
}

/** What a catch shows: app copy as-is, anything else through p2pErrorMessage
 *  (a raw server, RPC or library message never reaches the screen). */
export function p2pCaughtMessage(err: unknown, fallback?: string): string {
  if (err instanceof P2PCopyError) return err.message;
  // An Error keeps the code/status a refusal attached (services/adPublish).
  return p2pErrorMessage(typeof err === 'object' && err !== null ? (err as P2PErrorLike) : errorMessage(err), fallback);
}

/** The KYC refusal code to route on (utils/kycRefusal), if any. Older API
 *  builds send the P2P gate's 403 without a code — its message counts too. */
export function p2pKycCode(res: P2PErrorLike | null | undefined): string | undefined {
  if (!res) return undefined;
  if (res.code === KYC_VERIFICATION_REQUIRED || res.code === KYC_LEGAL_NAME_MISSING) return res.code;
  if (res.status === 403 && /verification required/i.test(res.message ?? '')) return KYC_VERIFICATION_REQUIRED;
  return undefined;
}
