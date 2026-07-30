// constants/routes.ts
//
// Hrefs that are easy to get wrong because the file path and the URL disagree.
//
// The Marketplace tab is a route GROUP, so its index resolves to `/` — which
// app/index.tsx also owns. That collision is why call sites historically pushed
// `/products` instead: it is addressable and renders the same MarketplaceScreen.
// But `/products` lives INSIDE the marketplace stack, so pushing it stacks a
// second, identical Marketplace on top of the tab root. Back then returns to a
// screen that looks the same, in a tab the user never opened.
//
// Spelling the group segments out targets the tab root itself. Group segments
// are optional when expo-router matches an href, so the explicit form is the
// one thing that distinguishes the marketplace index from app/index.tsx.
import type { Href } from 'expo-router';

/** The Marketplace tab's root screen. Use for every "go to the marketplace"
 *  action — pushing `/products` instead duplicates the screen (see above). */
export const MARKETPLACE_TAB = '/(dashboard)/(tabs)/(marketplace)' as Href;
