// app/(dashboard)/(tabs)/(services)/_layout.tsx
//
// The Services tab hosts only its hub. `serviceaction` (the add-product /
// add-service form) moved to the root-level `(detail)` group: it is opened from
// the merchant dashboard as well as from here, and a form owned by a tab sends
// back to that tab's root instead of to the caller.
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';

export default function Layout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="serviceshome" options={{ headerShown: false }} />
    </Stack>
  );
}
