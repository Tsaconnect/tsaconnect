// utils/quoteChain.ts
//
// Which chain a Spot listing's quote leg (the USDT/USDC side) lands on when All
// Swap opens it.
//
// LiFi needs a concrete token on a concrete chain, but for a cross-chain pair
// like BTC/USDT the chain barely moves the price: measured 2026-09-14, $100–$1000
// of stablecoin → BTC cost about 0.6–1.4% from every chain LiFi routes. What does matter
// is whether the user HOLDS the stablecoin there — open All Swap on BSC for a
// user whose USDT sits on Polygon and the form shows a zero balance. So the leg
// goes where the money already is, and a chain is only guessed for a user who
// holds none.
import type { WalletBalanceWithChain } from '@/services/walletApi';

/** Used when the user holds none of the quote asset and the listing names no chain.
 *  Mirrors models.SpotDefaultQuoteChains in the Go API. */
export const DEFAULT_QUOTE_CHAIN: Record<string, string> = {
  // The chain All Swap already opens paying from (DEFAULT_FROM_CHAIN_ID).
  USDT: 'bsc',
  // The cheapest USDC → BTC route in the 2026-09-14 comparison.
  USDC: 'base',
};
const LAST_RESORT_QUOTE_CHAIN = 'bsc';

export type QuoteBalanceRow = Pick<WalletBalanceWithChain, 'symbol' | 'balance' | 'chainKey'>;

/**
 * The chain where the user holds the most of `symbol`, else `fallback`, else
 * the default for that asset.
 *
 * Rows are compared by their human-readable balance, which is what makes 18-
 * and 6-decimal copies of the same stablecoin comparable at all. A tie keeps
 * the fallback chain when it is one of the tied ones, so an admin's choice
 * still breaks a tie rather than row order.
 */
export function pickQuoteChain(symbol: string, rows: QuoteBalanceRow[], fallback?: string): string {
  const wanted = symbol.trim().toUpperCase();
  const preferred = fallback?.trim().toLowerCase() ?? '';
  let best: { chain: string; amount: number } | null = null;
  for (const row of rows) {
    if (row.symbol.trim().toUpperCase() !== wanted) continue;
    const amount = Number(row.balance);
    if (!Number.isFinite(amount) || amount <= 0) continue;
    const beatsBest = !best || amount > best.amount || (amount === best.amount && row.chainKey === preferred);
    if (beatsBest) best = { chain: row.chainKey, amount };
  }
  if (best) return best.chain;
  return preferred || DEFAULT_QUOTE_CHAIN[wanted] || LAST_RESORT_QUOTE_CHAIN;
}
