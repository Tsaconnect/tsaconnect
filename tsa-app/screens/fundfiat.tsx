// screens/fundfiat.tsx
//
// RETIRED (2026-07-27): unreferenced. Fiat deposits are live via Instant
// Transfer (app/wallet/it/buy.tsx) and the P2P / Payment Merchant markets,
// reachable from the wallet's Deposit sheet.
//
// Fiat funding placeholder — a "Coming Soon" notice with a Go Back action.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). Behavior is unchanged: the only action still calls router.back().
import { router } from 'expo-router';
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { EmptyState } from '@/components/ui';

export default function FundFiatScreen() {
  const { colors } = useTheme();
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
        <EmptyState
          icon="clock"
          title="Coming Soon"
          subtitle="Fiat deposits are on the way. You'll be able to fund your wallet via bank transfer and other local payment methods."
          actionLabel="Go Back"
          onAction={() => router.back()}
        />
      </View>
    </SafeAreaView>
  );
}
