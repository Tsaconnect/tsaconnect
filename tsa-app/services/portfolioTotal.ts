// services/portfolioTotal.ts
//
// The account's headline balance in USD: what Home shows as the total and
// Profile shows as "My Balance". One function so the two can never disagree.

import { getWalletBalances, normalizeWalletBalances } from './walletApi';
import { getPremiumMcgpAsset } from './mcgpSwapApi';
import { getSignableWallet } from './wallet';

// Symbols the total is summed over. Not a display list — the market list on
// Home is driven entirely by the platform's spot listings.
export const PORTFOLIO_SYMBOLS = ['MCGP', 'USDC', 'USDT'] as const;

export async function readPortfolioUsd(): Promise<number> {
  // Balances for the wallet this device holds a key for. Without one the
  // backend would answer for the account's REGISTERED address, and the total
  // would show funds the user cannot touch on this device.
  const signer = await getSignableWallet();
  const balResult = signer
    ? await getWalletBalances(undefined, signer)
    : { success: false as const, data: undefined };
  // Flatten all chains into symbol -> usdValue
  const usdBySymbol: Record<string, number> = {};
  if (balResult.success && balResult.data) {
    for (const entry of normalizeWalletBalances(balResult.data)) {
      const usd = Number.isFinite(parseFloat(entry.usdValue)) ? parseFloat(entry.usdValue) : 0;
      usdBySymbol[entry.symbol] = (usdBySymbol[entry.symbol] || 0) + usd;
    }
  }

  // MCGP in the wallet is the spendable premium entitlement (Contract B),
  // not an ERC-20, so getWalletBalances reports 0. Overlay the premium
  // value so the total reflects what the user actually holds.
  try {
    const { balance: premium, usdValue } = await getPremiumMcgpAsset();
    if (premium > 0) usdBySymbol.MCGP = usdValue;
  } catch { /* MCGP swap unavailable — leave at 0 */ }

  return PORTFOLIO_SYMBOLS.reduce((s, sym) => s + (usdBySymbol[sym] || 0), 0);
}
