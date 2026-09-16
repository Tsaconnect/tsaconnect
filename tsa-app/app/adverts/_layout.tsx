// Adverts — category product listings. Previously declared `headerShown: true`
// with no options at all, so it rendered React Navigation's unstyled default
// header ON TOP of a screen that already drew its own back row. The screen's row
// is gone; this is now the only header, styled like every other section.
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { MARKETPLACE_TAB } from '@/constants/routes';

export default function AdvertsLayout() {
  const screenOptions = useDefaultScreenOptions({ backFallback: MARKETPLACE_TAB });
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="products/[productCategory]/index" options={{ title: 'Products' }} />
    </Stack>
  );
}
