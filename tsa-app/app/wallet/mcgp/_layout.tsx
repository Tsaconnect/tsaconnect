import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { WALLET_TAB } from '@/constants/routes';

// Nested stack for the MCGP (Swap Contract B) feature: a balance hub plus the
// buy / sell / pay flows. `index` is itself a destination — the wallet's asset
// list links straight to it — so it relies on the shared headerLeft for its back
// chevron; native-stack would draw none at index 0.
export default function McgpLayout() {
  const screenOptions = useDefaultScreenOptions({ backFallback: WALLET_TAB });

  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ title: 'MCGP' }} />
      <Stack.Screen name="buy" options={{ title: 'Buy MCGP' }} />
      <Stack.Screen name="sell" options={{ title: 'Sell MCGP' }} />
      <Stack.Screen name="pay" options={{ title: 'Pay with MCGP' }} />
    </Stack>
  );
}
