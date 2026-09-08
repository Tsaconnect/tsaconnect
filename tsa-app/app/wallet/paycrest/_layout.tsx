import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';

// Paycrest ramp — buy and sell stablecoins for Naira through a network of
// liquidity providers. Non-custodial on both legs: crypto is delivered to, and
// sent from, the user's own connected wallet.
export default function PaycrestLayout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="buy" options={{ title: 'Buy with Naira' }} />
      <Stack.Screen name="sell" options={{ title: 'Sell for Naira' }} />
      <Stack.Screen name="order/[id]" options={{ title: 'Order' }} />
    </Stack>
  );
}
