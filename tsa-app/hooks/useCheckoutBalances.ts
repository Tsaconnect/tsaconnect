// Balances checkout needs: each pay token and S for gas. USDC/USDT/S are read
// through /wallet/balances for the signing wallet, the same source the wallet
// screens use. MCGP is the exception: checkout pays MCGP from Spendable MCGP —
// the premium balance in the entitlement contract (/mcgp/balance
// premiumBalance), the same number useMcgpBalances shows — never from ERC-20
// MCGP sitting in the wallet. A null balance means "unknown" — callers must
// not block payment on it, only warn when a balance is known to be short.
import { useCallback, useEffect, useState } from 'react';
import { ethers } from 'ethers';
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { getMcgpBalance } from '@/services/mcgpSwapApi';
import { getNetwork } from '@/hooks/useNetwork';
import { getSignableWallet } from '@/services/wallet';

export type CheckoutBalanceSymbol = 'USDC' | 'USDT' | 'MCGP' | 'S';
export type CheckoutBalances = Record<CheckoutBalanceSymbol, number | null>;

const UNKNOWN: CheckoutBalances = { USDC: null, USDT: null, MCGP: null, S: null };
const WALLET_SYMBOLS: CheckoutBalanceSymbol[] = ['USDC', 'USDT', 'S'];

/** Spendable MCGP in MCGP units, or null when it cannot be read. */
async function readSpendableMcgp(): Promise<number | null> {
  try {
    const res = await getMcgpBalance();
    if (!res.success || !res.data) return null;
    return Number(ethers.formatUnits(res.data.premiumBalance || '0', 18)) || 0;
  } catch {
    return null;
  }
}

export function useCheckoutBalances() {
  const [balances, setBalances] = useState<CheckoutBalances>(UNKNOWN);
  const [address, setAddress] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    const wallet = await getSignableWallet();
    setAddress(wallet);
    if (!wallet) {
      setBalances(UNKNOWN);
      return;
    }
    const sonicChainId = getNetwork() === 'testnet' ? 14601 : 146;
    const [res, spendable] = await Promise.all([getWalletBalances(sonicChainId, wallet), readSpendableMcgp()]);
    const next = { ...UNKNOWN, MCGP: spendable };
    if (res.success && res.data) {
      const flat = normalizeWalletBalances(res.data, 'sonic').filter((b) => b.chainKey === 'sonic');
      for (const symbol of WALLET_SYMBOLS) {
        const row = flat.find((b) => b.symbol?.toUpperCase() === symbol);
        // Only a returned row is a known balance. A token missing from the
        // response (one RPC failing server-side) stays unknown, so it can never
        // block a payment the wallet could cover.
        if (row) next[symbol] = parseFloat(row.balance || '0') || 0;
      }
    }
    setBalances(next);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { balances, address, refresh };
}
