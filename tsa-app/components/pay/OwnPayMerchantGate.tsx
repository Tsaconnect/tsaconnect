// components/pay/OwnPayMerchantGate.tsx
//
// The one way a TSA Pay merchant screen waits for her merchant: a spinner while
// it loads, an error with Retry, "No TSA Pay account" when the server says she
// has none — and her merchant, handed to the screen, once it is there. Used by
// every payment-link screen (list, form, one-time and reusable detail).
import React from 'react';
import { SafeAreaView } from 'react-native-safe-area-context';
import { ErrorState, Spinner } from '@/components/ui';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import type { OwnMerchant } from '@/services/payMerchantApi';

/** A whole screen waiting on its first read. */
export function ScreenSpinner() {
  return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
}

export function OwnPayMerchantGate({ children }: { children: (merchant: OwnMerchant) => React.ReactNode }) {
  const { load, reload } = useOwnPayMerchant();
  if (load.kind === 'loading') return <ScreenSpinner />;
  if (load.kind === 'error') {
    return <ErrorState title="Could not load your TSA Pay account" subtitle={load.message} onRetry={reload} />;
  }
  if (load.kind === 'none') {
    return <ErrorState title="No TSA Pay account" subtitle="Apply to accept payments with TSA Pay first." />;
  }
  return <>{children(load.merchant)}</>;
}
