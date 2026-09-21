import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';

// Paycrest ramp — buy and sell stablecoins for local fiat (NGN, KES, UGX, …)
// through a network of liquidity providers. Non-custodial on both legs: crypto
// is delivered to, and sent from, the user's own connected wallet. The titles
// here are placeholders; buy/sell set a currency-specific one on mount.
export default function PaycrestLayout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="buy" options={{ title: 'Buy' }} />
      <Stack.Screen name="sell" options={{ title: 'Sell' }} />
      <Stack.Screen name="order/[id]" options={{ title: 'Order' }} />
    </Stack>
  );
}
