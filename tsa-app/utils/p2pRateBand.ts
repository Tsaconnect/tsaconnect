// utils/p2pRateBand.ts — the fixed-rate band, checked before a sell ad's
// deposit so a typo (15 for 1500) is caught before any crypto moves.
import { p2pErrorMessage } from '@/services/p2pErrors';

/** Mirrors the API's P2P_FIXED_RATE_BAND_BPS default (2000 = ±20% of the
 *  market reference). The server is authoritative: it may run a different
 *  band or reference, and its 400 rate_out_of_band is the real check — this
 *  only stops an obvious mistake before the deposit. */
export const P2P_FIXED_RATE_BAND_BPS = 2000;

// The server checks stable assets only (IsStableAsset in stable_assets.go).
const BAND_CHECKED_TOKENS = new Set(['USDT', 'USDC']);

function bandEdge(reference: number, sign: 1 | -1): string {
  const v = reference * (1 + (sign * P2P_FIXED_RATE_BAND_BPS) / 10_000);
  // Same shape as the server's ratDecimal: 2 dp, trailing zeros trimmed.
  return v.toFixed(2).replace(/\.?0+$/, '');
}

/** The refusal copy when a fixed `rate` is outside the band around
 *  `reference`, else null. Passes (like the server) when there is nothing to
 *  compare against: a non-stable token, no reference, or a stale one. */
export function fixedRateBandRefusal(p: {
  token: string;
  rate: string;
  reference: number | null | undefined;
  referenceStale?: boolean;
}): string | null {
  if (!BAND_CHECKED_TOKENS.has(p.token.toUpperCase())) return null;
  const ref = p.reference;
  if (ref == null || !isFinite(ref) || ref <= 0 || p.referenceStale) return null;
  const rate = parseFloat(p.rate);
  if (!isFinite(rate)) return null;
  const band = P2P_FIXED_RATE_BAND_BPS / 10_000;
  if (rate >= ref * (1 - band) && rate <= ref * (1 + band)) return null;
  return p2pErrorMessage({
    code: 'rate_out_of_band',
    data: { minRate: bandEdge(ref, -1), maxRate: bandEdge(ref, 1) },
  });
}
