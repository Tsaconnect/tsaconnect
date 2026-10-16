// services/portfolioTotal.ts
//
// The account's headline balance in USD: what Home shows as the total and
// Profile shows as "My Balance". One function so the two can never disagree.

import { readWalletBalances } from './walletApi';
import { getPremiumMcgpAsset } from './mcgpSwapApi';
import { getSignableWallet } from './wallet';

// Symbols the total is summed over. Not a display list — the market list on
// Home is driven entirely by the platform's spot listings.
export const PORTFOLIO_SYMBOLS = ['MCGP', 'USDC', 'USDT'] as const;

/**
 * The total in USD, or null when the wallet's balances could not be read —
 * unknown, never zero: a failed read must not show a holder $0.00.
 */
export async function readPortfolioUsd(): Promise<number | null> {
  // Balances for the wallet this device holds a key for. Without one the
  // backend would answer for the account's REGISTERED address, and the total
  // would show funds the user cannot touch on this device — so no key means
  // no wallet balances, not unknown ones.
  const signer = await getSignableWallet();

  // Independent reads, so neither waits on the other. MCGP in the wallet is
  // the spendable premium entitlement (Contract B), not an ERC-20, so the
  // wallet read reports 0 for it; the premium value is overlaid. A premium
  // read that fails counts as none held, as Home always has.
  const [rows, premium] = await Promise.all([
    signer ? readWalletBalances(signer) : Promise.resolve([]),
    getPremiumMcgpAsset().catch(() => ({ balance: 0, usdValue: 0 })),
  ]);
  if (rows === null) return null;

  const usdBySymbol: Record<string, number> = {};
  for (const entry of rows) {
    const usd = parseFloat(entry.usdValue);
    usdBySymbol[entry.symbol] = (usdBySymbol[entry.symbol] || 0) + (Number.isFinite(usd) ? usd : 0);
  }
  if (premium.balance > 0) usdBySymbol.MCGP = premium.usdValue;

  return PORTFOLIO_SYMBOLS.reduce((s, sym) => s + (usdBySymbol[sym] || 0), 0);
}
