import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

// Nested stack for the MCGP (Swap Contract B) feature: a balance hub plus the
// buy / sell / pay flows. Mirrors app/wallet/_layout.tsx styling.
export default function McgpLayout() {
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
      <Stack.Screen name="index" options={{ title: 'MCGP' }} />
      <Stack.Screen name="buy" options={{ title: 'Buy MCGP' }} />
      <Stack.Screen name="sell" options={{ title: 'Sell MCGP' }} />
      <Stack.Screen name="pay" options={{ title: 'Pay with MCGP' }} />
    </Stack>
  );
}
