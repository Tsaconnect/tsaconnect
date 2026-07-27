import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';
import { railLabel } from '@/constants/rails';

// Instant Transfer (IT) — fiat & crypto swap; buy/sell MCGP with Naira via the
// company Payment Merchant.
export default function ITLayout() {
  const { colors, type } = useTheme();
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
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ title: railLabel('it') }} />
      <Stack.Screen name="buy" options={{ title: 'Deposit · Buy' }} />
      <Stack.Screen name="sell" options={{ title: 'Withdraw · Sell' }} />
      <Stack.Screen name="intent/[id]" options={{ title: 'Order' }} />
    </Stack>
  );
}
