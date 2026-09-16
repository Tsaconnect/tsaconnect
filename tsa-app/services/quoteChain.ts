// services/quoteChain.ts
//
// Reads the wallet and applies utils/quoteChain's rule. Kept apart from the
// rule so the rule stays testable without a network.
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { pickQuoteChain, type QuoteBalanceRow } from '@/utils/quoteChain';

// The full-wallet read touches every EVM chain, and the pair stays on
// "Selecting this pair…" until it answers. Past this, the fallback chain is a
// better answer than a longer wait: the user can still switch chains in the form.
export const BALANCE_READ_BUDGET_MS = 2500;

async function readRows(): Promise<QuoteBalanceRow[]> {
  try {
    const res = await getWalletBalances();
    return res.success && res.data ? normalizeWalletBalances(res.data) : [];
  } catch {
    return [];
  }
}

/**
 * The chain key the quote leg should use for this user.
 *
 * EVM only: one /wallet/balances call covers every EVM chain from the backend's
 * own token list, so a row's symbol is trustworthy. Solana and Sui balances
 * would each need their own read and can carry impostor tokens named USDT
 * (see matchSpotBalance), so a user holding only there gets the fallback and
 * can switch chains in All Swap.
 *
 * Never throws: a failed or slow read is the same as holding nothing.
 */
export async function fundedQuoteChain(
  symbol: string, fallback?: string, budgetMs = BALANCE_READ_BUDGET_MS,
): Promise<string> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const outOfTime = new Promise<QuoteBalanceRow[]>((resolve) => {
    timer = setTimeout(() => resolve([]), budgetMs);
  });
  try {
    return pickQuoteChain(symbol, await Promise.race([readRows(), outOfTime]), fallback);
  } finally {
    clearTimeout(timer);
  }
}
