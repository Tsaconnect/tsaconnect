// app/(dashboard)/(tabs)/(marketplace)/digital.tsx
//
// LEGACY ALIAS — see products.tsx. `/digital` also rendered a second copy of
// MarketplaceScreen with no filter applied, so it was indistinguishable from the
// tab root. Redirects there instead of duplicating it.
import { Redirect } from 'expo-router';
import { MARKETPLACE_TAB } from '@/constants/routes';

export default function Digital() {
  return <Redirect href={MARKETPLACE_TAB} />;
}
