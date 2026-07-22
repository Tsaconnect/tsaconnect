// app/(dashboard)/(tabs)/(marketplace)/products.tsx
//
// The `/products` route now renders the canonical MarketplaceScreen (the
// double-column, tabbed browser used by the Marketplace tab) instead of a
// separate single-column list. This de-duplicates the two marketplace screens:
// every "browse products" entry point (checkout confirmation, home widgets,
// service tab) lands on the same screen. The route is kept — several places
// still push to `/products` — but the standalone single-column implementation
// (HeaderSearch + ProductListCard) is retired.
import React from "react";
import MarketplaceScreen from "@/screens/marketplace";

export default function Products() {
  return <MarketplaceScreen />;
}
