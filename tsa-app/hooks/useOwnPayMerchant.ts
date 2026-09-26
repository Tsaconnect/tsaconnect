// hooks/useOwnPayMerchant.ts
//
// Her own TSA Pay merchant, read the one way every merchant screen needs it:
// ready, none (the server says she has no merchant), or an error. Only the
// server's no_merchant code means none — any other 404 is the feature being
// off, not an invitation to apply again.
import { useCallback, useState } from 'react';
import { useFocusEffect } from 'expo-router';
import { getOwnMerchant, OWNER_CODES, type OwnMerchant } from '@/services/payMerchantApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';

export type OwnMerchantLoad =
  | { kind: 'loading' }
  | { kind: 'none' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; merchant: OwnMerchant };

export async function loadOwnMerchant(): Promise<OwnMerchantLoad> {
  try {
    return { kind: 'ready', merchant: await getOwnMerchant() };
  } catch (e) {
    if (errorCode(e) === OWNER_CODES.NO_MERCHANT) return { kind: 'none' };
    return { kind: 'error', message: errorMessage(e, 'Could not load your payment settings.') };
  }
}

/** Read on every focus, so a screen returned to shows her merchant as it is now. */
export function useOwnPayMerchant(): { load: OwnMerchantLoad; reload: () => Promise<void> } {
  const [load, setLoad] = useState<OwnMerchantLoad>({ kind: 'loading' });
  const reload = useCallback(async () => { setLoad(await loadOwnMerchant()); }, []);
  useFocusEffect(useCallback(() => { void reload(); }, [reload]));
  return { load, reload };
}
