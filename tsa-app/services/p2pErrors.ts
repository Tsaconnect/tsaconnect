// services/p2pErrors.ts
//
// Turn raw P2P API / RPC errors into sentences a user can act on (R10). The
// API's sentinels live in tsa-api-go/internal/services/p2p_trade_service.go
// ("p2p: …"). Anything unmatched goes through submitErrors.mapSubmitError, the
// shared guard against stack traces, HTML and SQL reaching the screen.
import { decodeRevertError } from './txErrors';
import { mapSubmitError, TRANSFER_COPY } from './submitErrors';
import { KYC_LEGAL_NAME_MISSING, KYC_VERIFICATION_REQUIRED } from '@/utils/kycRefusal';

export interface P2PErrorLike {
  message?: string;
  status?: number;
  code?: string;
  /** Structured detail some codes carry, e.g. rate_out_of_band's minRate/maxRate. */
  data?: unknown;
}

const CODE_COPY: Record<string, string> = {
  app_update_required: 'Please update the app to continue this trade.',
  [KYC_VERIFICATION_REQUIRED]: 'Verify your identity to use P2P.',
  gas_not_needed: "This step doesn't need gas right now.",
  gas_topup_unused: 'A gas top-up was already sent recently. Use it first, then try again.',
  sponsorship_in_progress: 'A gas top-up is already in progress. Give it a moment.',
  gas_unavailable: 'The gas top-up is unavailable right now. Try again in a minute.',
  WALLET_PROOF_REQUIRED: 'Prove ownership of your wallet before continuing.',
};

// A timeout says nothing about the outcome: the request may have landed.
const TIMEOUT_COPY =
  'We could not confirm this in time. It may still go through, so check the trade before trying again.';

const RULES: [RegExp, string][] = [
  [/timed out|timeout|etimedout|aborted/i, TIMEOUT_COPY],
  [/network request failed|^network error$|failed to fetch/i, "Can't reach TSA Connect. Check your connection and try again."],
  [/rate source is stale/i, 'The live rate is unavailable right now. Try again in a few seconds.'],
  [/blockchain temporarily unavailable/i, 'The network is busy right now. Try again in a minute.'],
  [/bad voucher signature|ad is not active|no longer available for new p2p/i, 'This ad is no longer available.'],
  [/amount outside ad min\/max/i, "That amount is outside this ad's limits."],
  [/exceeds ad remaining|available balance can no longer cover/i, "This ad can't cover that amount any more. Try a smaller amount or another ad."],
  [/cannot open a trade on their own ad/i, "You can't trade on your own ad."],
  [/already have an active trade on this ad/i, 'You already have an open trade on this ad. Finish it first.'],
  [/invalid state transition/i, 'This trade has already moved on. Pull down to refresh.'],
  [/already has an open dispute/i, 'A dispute is already open for this trade.'],
  [/not authorized for this action/i, "You can't do that on this trade."],
  [/trade not found|ad not found/i, "We couldn't find that. It may have been removed."],
  [/payment method not found/i, 'Pick a payment method and try again.'],
  [/token not supported on this chain/i, "This token isn't supported on that network."],
  [/ad has open trades/i, 'This ad has trades in progress. Wait for them to finish first.'],
];

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
  if (r.status === 401) return 'Your session has expired. Sign in again.';
  if (r.status === 410) return 'Your app version is out of date — please update and retry.';
  if (r.status === 429) return "You've hit the daily limit for gas top-ups. Try again tomorrow.";
  for (const [re, copy] of RULES) if (re.test(raw)) return copy;
  if (decodeRevertError(raw)) {
    return 'The network would reject this transaction, so nothing was sent. Pull down to refresh and try again.';
  }
  const stripped = raw.replace(/^p2p(?: chat)?:\s*/i, '');
  const tidy = stripped.charAt(0).toUpperCase() + stripped.slice(1);
  return mapSubmitError(tidy, { fallback, retry: TRANSFER_COPY.retry });
}

/** The KYC refusal code to route on (utils/kycRefusal), if any. Older API
 *  builds send the P2P gate's 403 without a code — its message counts too. */
export function p2pKycCode(res: P2PErrorLike | null | undefined): string | undefined {
  if (!res) return undefined;
  if (res.code === KYC_VERIFICATION_REQUIRED || res.code === KYC_LEGAL_NAME_MISSING) return res.code;
  if (res.status === 403 && /verification required/i.test(res.message ?? '')) return KYC_VERIFICATION_REQUIRED;
  return undefined;
}

/** Posting ads needs a verified identity. Unknown status (profile not loaded)
 *  does not block — the server's 403 is the backstop. */
export function kycBlocksPosting(verificationStatus: string | null | undefined): boolean {
  return !!verificationStatus && verificationStatus !== 'verified';
}
