// app/(dashboard)/(tabs)/(marketplace)/products.tsx
//
// The `/products` route now renders the canonical MarketplaceScreen (the
// double-column, tabbed browser used by the Marketplace tab) instead of a
// separate single-column list. This de-duplicates the two marketplace screens:
// every "browse products" entry point (checkout confirmation, home widgets,
// service tab) lands on the same screen.
//
// LEGACY ALIAS — do not push this from in-app code. It renders the same screen
// as the marketplace tab ROOT, so pushing it stacks an identical Marketplace on
// top of that root and back drops the user on a screen that looks unchanged, in
// a tab they never opened. In-app callers now use MARKETPLACE_TAB
// (constants/routes.ts). The route survives so existing deep links and any
// bookmarked `/products` URL still resolve; the marketplace _layout gives it a
// "Marketplace" header with a back chevron so arriving cold isn't a dead end.
import React from "react";
import MarketplaceScreen from "@/screens/marketplace";

export default function Products() {
  return <MarketplaceScreen />;
}
