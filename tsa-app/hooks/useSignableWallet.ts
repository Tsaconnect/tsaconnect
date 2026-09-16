// hooks/useSignableWallet.ts
//
// Whether this device can actually sign for the active wallet, for screens that
// must not show a balance the user has no key to spend. See
// services/wallet.ts#getSignableWallet for why the address alone is not enough.
import { useCallback, useEffect, useState } from 'react';
import { useFocusEffect } from '@react-navigation/native';
import { getSignableWallet } from '@/services/wallet';

export interface SignableWalletState {
  /** The active wallet's address, or null when this device holds no key for it. */
  address: string | null;
  /** True once the check has run — render neither the balance nor the empty state before this. */
  ready: boolean;
  refresh: () => void;
}

export function useSignableWallet(): SignableWalletState {
  const [address, setAddress] = useState<string | null>(null);
  const [ready, setReady] = useState(false);

  const refresh = useCallback(() => {
    let cancelled = false;
    getSignableWallet()
      .then((addr) => {
        if (!cancelled) setAddress(addr);
      })
      .catch(() => {
        // A SecureStore read can throw (device locked, keychain unavailable).
        // Treat that as "cannot sign" — the safe direction, since the cost is a
        // prompt to import rather than a balance the user cannot act on.
        if (!cancelled) setAddress(null);
      })
      .finally(() => {
        if (!cancelled) setReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => refresh(), [refresh]);

  // Re-check on focus: the user can import or switch wallets on another screen
  // and come straight back to this one.
  useFocusEffect(refresh);

  return { address, ready, refresh };
}
