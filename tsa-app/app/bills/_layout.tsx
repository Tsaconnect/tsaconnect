import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { SERVICES_TAB } from '@/constants/routes';

// Utility bill payments — airtime/data/electricity/cable-TV/betting/exam
// e-PINs, paid with spendable MCGP. Entered from the Services hub, so `index`
// is a pushed destination rather than a stack root the user arrived at
// gradually; the shared headerLeft is what gives it a back chevron.
export default function BillsLayout() {
  const screenOptions = useDefaultScreenOptions({ backFallback: SERVICES_TAB });

  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ title: 'Pay Bills' }} />
      <Stack.Screen name="orders" options={{ title: 'Bill History' }} />
      <Stack.Screen name="order/[id]" options={{ title: 'Bill Details' }} />
      <Stack.Screen name="[category]" options={{ title: 'Pay Bill' }} />
    </Stack>
  );
}
