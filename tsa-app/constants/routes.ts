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
 *  action — pushing `/products` instead duplicates the screen (see above).
 *
 *  `satisfies` rather than `as`: a cast would silently accept a typo, which is
 *  the exact failure this whole change exists to fix. This way tsc checks the
 *  literal against the generated route union AND keeps its narrow type. */
export const MARKETPLACE_TAB = '/(dashboard)/(tabs)/(marketplace)' satisfies Href;

/** The Home tab's root screen. */
export const HOME_TAB = '/home' satisfies Href;

/** The Services tab's root screen. The (services) group has no `index`, so this
 *  file-named route IS the tab root and is addressable without group segments. */
export const SERVICES_TAB = '/serviceshome' satisfies Href;

/** The Wallet tab's root screen (the tab is registered as `tokenization`). */
export const WALLET_TAB = '/tokenization' satisfies Href;

/** The TSA Pay application: the merchant-request form locked to
 *  payment_acceptance. The form itself routes to the verify-first,
 *  pending, approved or rejected screen, so every entry point pushes this. */
export const TSA_PAY_APPLY = '/merchants/merchant-request?type=payment_acceptance' satisfies Href;
