// Sonic balances checkout needs: each pay token (USDC/USDT/MCGP) and S for gas.
// Read through /wallet/balances for the signing wallet, the same source the
// wallet screens use. A null balance means "unknown" — callers must not block
// payment on it, only warn when a balance is known to be short.
import { useCallback, useEffect, useState } from 'react';
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { getNetwork } from '@/hooks/useNetwork';
import { getSignableWallet } from '@/services/wallet';

const SYMBOLS = ['USDC', 'USDT', 'MCGP', 'S'] as const;
export type CheckoutBalanceSymbol = (typeof SYMBOLS)[number];
export type CheckoutBalances = Record<CheckoutBalanceSymbol, number | null>;

const UNKNOWN: CheckoutBalances = { USDC: null, USDT: null, MCGP: null, S: null };

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
    const res = await getWalletBalances(sonicChainId, wallet);
    if (!res.success || !res.data) {
      setBalances(UNKNOWN);
      return;
    }
    const flat = normalizeWalletBalances(res.data, 'sonic').filter((b) => b.chainKey === 'sonic');
    const next = { ...UNKNOWN };
    for (const symbol of SYMBOLS) {
      const row = flat.find((b) => b.symbol?.toUpperCase() === symbol);
      // Only a returned row is a known balance. A token missing from the
      // response (one RPC failing server-side) stays unknown, so it can never
      // block a payment the wallet could cover.
      if (row) next[symbol] = parseFloat(row.balance || '0') || 0;
    }
    setBalances(next);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { balances, address, refresh };
}
