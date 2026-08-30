// app/(dashboard)/(tabs)/(marketplace)/products.tsx
//
// LEGACY ALIAS. `/products` rendered the same MarketplaceScreen as the
// marketplace tab ROOT, so pushing it stacked an identical Marketplace on top of
// that root and back dropped the user on a screen that looked unchanged, in a
// tab they never opened. In-app callers use MARKETPLACE_TAB
// (constants/routes.ts); the route survives only so existing deep links and any
// bookmarked `/products` URL still resolve — as a redirect to the real tab.
import { Redirect } from 'expo-router';
import { MARKETPLACE_TAB } from '@/constants/routes';

export default function Products() {
  return <Redirect href={MARKETPLACE_TAB} />;
}
