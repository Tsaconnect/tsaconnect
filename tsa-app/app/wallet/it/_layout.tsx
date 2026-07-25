import React, { useCallback, useEffect, useState } from 'react';
import { Stack, router } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';
import { Spinner } from '@/components/ui';
import ITAgreementGate from '@/components/it/ITAgreementGate';
import { getITAgreement } from '@/services/itApi';

// Instant Transfer (IT) — buy/sell MCGP with Naira via the company PM.
//
// The trading agreement gates the whole group, not just the hub: buy and sell
// are linked directly from the wallet's fiat sheet, so gating only the hub
// would let a user fill in an entire buy form before the backend — which
// enforces the same agreement on every trade — rejected it at submit.
export default function ITLayout() {
  const { colors, type } = useTheme();
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

  const screenOptions: NativeStackNavigationOptions = {
    headerTitleAlign: 'center',
    headerTintColor: colors.accent,
    headerTitleStyle: {
      fontSize: type.title3.fontSize,
      fontFamily: fontFamily['600'],
      color: colors.textPrimary,
    },
    headerStyle: { backgroundColor: colors.surface },
    headerShadowVisible: false,
    contentStyle: { backgroundColor: colors.background },
  };

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
    return <ITAgreementGate onAccepted={() => setAgreed(true)} onCancel={() => router.back()} />;
  }

  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ title: 'Instant Transfer' }} />
      <Stack.Screen name="buy" options={{ title: 'Buy' }} />
      <Stack.Screen name="sell" options={{ title: 'Sell' }} />
      <Stack.Screen name="intent/[id]" options={{ title: 'Order' }} />
    </Stack>
  );
}
