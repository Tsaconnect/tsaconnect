// app/(dashboard)/(tabs)/(home)/_layout.tsx
//
// The Home tab hosts exactly one screen. Everything it links to (product,
// seller, subcategory, checkout, the wallet actions, the rewards trio) lives in
// the root-level `(detail)` group instead — see app/(detail)/_layout.tsx for
// why. Keeping the tab a single screen is what guarantees "back" from a detail
// page returns to the tab the user actually came from.
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';

export default function Layout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={screenOptions}>
        <Stack.Screen name="home" options={{ headerShown: false }} />
      </Stack>
    </GestureHandlerRootView>
  );
}
