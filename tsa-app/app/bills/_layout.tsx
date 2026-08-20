import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

// Utility bill payments — airtime/data/electricity/cable-TV/betting/exam
// e-PINs, paid with spendable MCGP.
export default function BillsLayout() {
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
      <Stack.Screen name="index" options={{ title: 'Pay Bills' }} />
      <Stack.Screen name="orders" options={{ title: 'Bill History' }} />
      <Stack.Screen name="order/[id]" options={{ title: 'Bill Details' }} />
      <Stack.Screen name="[category]" options={{ title: 'Pay Bill' }} />
    </Stack>
  );
}
