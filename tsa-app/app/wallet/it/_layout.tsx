import React, { useCallback, useEffect, useState } from 'react';
import { Stack, router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { WALLET_TAB } from '@/constants/routes';
import { Spinner } from '@/components/ui';
import ITAgreementGate from '@/components/it/ITAgreementGate';
import { getITAgreement } from '@/services/itApi';
import { railLabel } from '@/constants/rails';

// Anchor the stack to the hub. The wallet's fiat sheet links straight to
// /wallet/it/buy and /wallet/it/sell, and expo-router builds a nested stack's
// routes array from the URL alone — without this the stack would be
// routes: [buy] at index 0, leaving the header back button with nothing to pop.
// With it, getStateFromPath inserts the hub beneath the target.
export const unstable_settings = { initialRouteName: 'index' };

// Instant Transfer (IT) — fiat & crypto swap; buy/sell MCGP with Naira via the
// company Payment Merchant.
//
// The trading agreement gates the whole group, not just the hub: buy and sell
// are linked directly from the wallet's Deposit/Withdraw sheets, so gating only
// the hub would let a user fill in an entire buy form before the backend —
// which enforces the same agreement on every trade — rejected it at submit.
export default function ITLayout() {
  const { colors } = useTheme();
  // null = still checking; false = must accept before any IT screen renders.
  const [agreed, setAgreed] = useState<boolean | null>(null);

  const check = useCallback(async () => {
    const res = await getITAgreement();
    // If the agreement can't be loaded, let the user through rather than
    // hard-blocking IT on a transient failure — the backend still gates each
    // trade, so this can't admit an un-agreed user to an actual trade.
    setAgreed(res.success && res.data ? res.data.accepted : true);
  }, []);

  useEffect(() => {
    check();
  }, [check]);

  const screenOptions = useDefaultScreenOptions({ backFallback: WALLET_TAB });

  if (agreed === null) {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}
      >
        <Spinner />
      </SafeAreaView>
    );
  }

  if (!agreed) {
    // Cancel leaves IT altogether. The gate stands in for the whole navigator,
    // so popping a single route would re-render the same gate over the hub and
    // read as a dead button — unwind to the stack's anchor, then pop the group.
    const leave = () => {
      router.dismissAll();
      if (router.canGoBack()) router.back();
    };
    return <ITAgreementGate onAccepted={() => setAgreed(true)} onCancel={leave} />;
  }

  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ title: railLabel('it') }} />
      <Stack.Screen name="buy" options={{ title: 'Deposit · Buy' }} />
      <Stack.Screen name="buy-stable" options={{ title: 'Deposit · Buy' }} />
      <Stack.Screen name="sell" options={{ title: 'Withdraw · Sell' }} />
      <Stack.Screen name="sell-stable" options={{ title: 'Withdraw · Sell' }} />
      <Stack.Screen name="intent/[id]" options={{ title: 'Order' }} />
      <Stack.Screen name="activity" options={{ title: 'Activity' }} />
    </Stack>
  );
}
