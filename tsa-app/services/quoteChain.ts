// services/quoteChain.ts
//
// Reads the wallet and applies utils/quoteChain's rule. Kept apart from the
// rule so the rule stays testable without a network.
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { pickQuoteChain } from '@/utils/quoteChain';

/**
 * The chain key the quote leg should use for this user.
 *
 * EVM only: one /wallet/balances call covers every EVM chain from the backend's
 * own token list, so a row's symbol is trustworthy. Solana and Sui balances
 * would each need their own read and can carry impostor tokens named USDT
 * (see matchSpotBalance), so a user holding only there gets the fallback and
 * can switch chains in All Swap.
 *
 * Never throws: a failed read is the same as holding nothing.
 */
export async function fundedQuoteChain(symbol: string, fallback?: string): Promise<string> {
  try {
    const res = await getWalletBalances();
    const rows = res.success && res.data ? normalizeWalletBalances(res.data) : [];
    return pickQuoteChain(symbol, rows, fallback);
  } catch {
    return pickQuoteChain(symbol, [], fallback);
  }
}
