// utils/otcSell.ts
//
// The OTC sell form's limit. Owner rule (2026-09-28): wallet MCGP is not
// usable in the app, except that a wallet may sell back on the OTC desk what
// it bought on the OTC desk. The API rebuilds that figure from the desk's own
// Bought/Sold logs (GET /api/otc/position) and refuses anything larger when the
// sell is prepared; these helpers make the form agree with it, so the user
// meets the limit while typing rather than after tapping Review.
//
// Everything is in wei (bigint) — the old check compared Number()s of decimal
// strings, which cannot hold 18-decimal amounts exactly.
//
// Pure and dependency-light so the rules can be tested without the screen.
import { formatUnits, parseUnits } from 'ethers';

export const MCGP_DECIMALS = 18;

export const OTC_SELLABLE_LABEL = 'Available to sell (bought on OTC)';

export const OTC_NOTHING_TO_SELL =
  'This wallet has no MCGP bought on the OTC desk, so there is nothing to sell here. ' +
  'Only MCGP purchased on the OTC desk can be sold back.';

export const OTC_POSITION_UNAVAILABLE =
  "We couldn't check your OTC purchases right now. Please try again in a moment.";

function toBig(wei: string | null | undefined): bigint | null {
  if (wei == null || !/^\d+$/.test(wei)) return null;
  return BigInt(wei);
}

/** The amount string Max fills in: the whole sellable, full precision. */
export function otcSellableDecimal(sellableWei: string | null | undefined): string {
  const v = toBig(sellableWei);
  if (v == null || v === BigInt(0)) return '0';
  return formatUnits(v, MCGP_DECIMALS).replace(/\.0$/, '');
}

/** A short display of the sellable amount, e.g. "5.2". Truncates, never rounds up. */
export function otcSellableDisplay(sellableWei: string | null | undefined, maxFraction = 6): string {
  const full = otcSellableDecimal(sellableWei);
  const [int, frac = ''] = full.split('.');
  const cut = frac.slice(0, maxFraction).replace(/0+$/, '');
  const short = cut ? `${int}.${cut}` : int;
  // Dust below the cut would read as "0" while there is something to sell.
  return short === '0' ? full : short;
}

/** Whether there is anything to sell. null = not known (loading or failed). */
export function hasOtcSellable(sellableWei: string | null | undefined): boolean | null {
  const v = toBig(sellableWei);
  return v == null ? null : v > BigInt(0);
}

/**
 * Why `amount` (a decimal MCGP string as typed) cannot be sold, or null when
 * it can. `sellableWei` is null while the position is unknown — the form
 * refuses then, the same as the API would.
 */
export function otcSellBlocker(amount: string, sellableWei: string | null | undefined): string | null {
  const sellable = toBig(sellableWei);
  if (sellable == null) return OTC_POSITION_UNAVAILABLE;
  if (sellable === BigInt(0)) return OTC_NOTHING_TO_SELL;

  let wei: bigint;
  try {
    wei = parseUnits(amount.trim(), MCGP_DECIMALS);
  } catch {
    return 'Enter a valid amount.';
  }
  if (wei <= BigInt(0)) return 'Enter a valid amount.';
  if (wei > sellable) {
    return `You can sell up to ${otcSellableDisplay(sellableWei)} MCGP. ` +
      'Only MCGP purchased on the OTC desk can be sold back.';
  }
  return null;
}
