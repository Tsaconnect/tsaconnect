// services/mcgpSellGuard.ts
//
// Pure guards for the "sell premium MCGP for USDC" screen. Premium MCGP is one
// merged balance in the UI, but the contract tracks two buckets:
// `premiumBought` (paid for in USDC, self-exitable) and `premiumCredited`
// (naira-bought via Instant Transfer/bills, spend-only in-app). sellPremium
// requires premiumBought >= amount and reverts "Insufficient bought premium"
// otherwise — previously the app let a naira buyer submit and only found out
// from a raw gas-estimation error. These run BEFORE any prepare/gas call so
// the block is a plain sentence instead of a chain revert.

/** Shown when the requested amount exceeds what is sellable for USDC. */
export const NAIRA_ONLY_SELL_MESSAGE =
  'MCGP bought with naira can be spent in the app or sold for naira on Instant Transfer, but not sold for USDC.';

/** Shown when the contract itself refuses the sell for lack of USDC float. */
export const USDC_SELL_UNAVAILABLE_MESSAGE =
  'USDC sells are temporarily unavailable. Try again later.';

function toBigIntSafe(v: string | undefined | null): bigint {
  try {
    const t = (v ?? '').trim();
    if (!t) return 0n;
    return BigInt(t);
  } catch {
    return 0n;
  }
}

/**
 * Checks a proposed sell amount (base units, 18dp) against the self-exitable
 * "bought" bucket. Returns a plain-language block reason, or null when the
 * amount is fully covered (including amount <= 0, which the existing "enter
 * an amount" validation already handles).
 */
export function checkSellableForUsdc(amountBase: string, premiumBoughtBase: string): string | null {
  const amount = toBigIntSafe(amountBase);
  if (amount <= 0n) return null;
  const bought = toBigIntSafe(premiumBoughtBase);
  if (amount > bought) return NAIRA_ONLY_SELL_MESSAGE;
  return null;
}

/**
 * Maps a raw prepare/gas-estimation failure message — which may embed a
 * contract revert reason verbatim, e.g. "Prepare failed: gas estimation
 * failed: ... execution reverted: Insufficient bought premium" — to a plain
 * message. Anything unmatched passes through unchanged so an unrelated
 * failure is never hidden.
 */
export function mapMcgpSellError(rawMessage: string | undefined | null): string {
  const msg = rawMessage || '';
  if (msg.includes('Insufficient bought premium')) return NAIRA_ONLY_SELL_MESSAGE;
  if (msg.includes('Insufficient USDC')) return USDC_SELL_UNAVAILABLE_MESSAGE;
  return msg || 'Sell failed';
}
