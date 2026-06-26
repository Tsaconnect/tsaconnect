import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

// Instant Transfer (IT) — buy/sell MCGP with Naira via the company PM.
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
      <Stack.Screen name="index" options={{ title: 'Instant Transfer' }} />
      <Stack.Screen name="buy" options={{ title: 'Buy with Naira' }} />
      <Stack.Screen name="sell" options={{ title: 'Sell for Naira' }} />
    </Stack>
  );
}
