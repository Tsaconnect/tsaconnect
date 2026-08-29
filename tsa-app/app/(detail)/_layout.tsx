// app/(detail)/_layout.tsx
//
// Root-level stack for the screens that belong to no single tab.
//
// These used to live inside `(dashboard)/(tabs)/(home)`. That made the Home tab
// their owner, and expo-router resolves a push by navigating to the owning
// navigator: tapping a category in the MARKETPLACE tab switched to the HOME tab
// and pushed the screen there, so back returned the user to Home rather than to
// the marketplace they came from. Same for the wallet's "Crypto Deposit"
// (`/fund`), the services hub's "Trade & Earn" (`/trade`), the cart's product
// links, and every product/seller link outside Home. That is the "back takes
// you to main instead of going back gradually" report.
//
// Hosted at the root instead, they push onto the ROOT stack from whichever tab
// is active, so back unwinds to the exact caller. Pushes BETWEEN them (category
// → product → seller) stay inside this one stack and pop one screen at a time.
//
// The route group parentheses mean the URLs are unchanged — `/product/[id]`,
// `/trade`, `/checkout` and the rest resolve exactly as before, so no call site
// moved with them. A detail screen also correctly loses the tab bar now.
import React from 'react';
import { Stack } from 'expo-router';
import { useAuth } from '@/AuthContext/AuthContext';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { HOME_TAB } from '@/constants/routes';

export default function DetailLayout() {
  const { appService } = useAuth();
  const screenOptions = useDefaultScreenOptions({ backFallback: HOME_TAB });

  return (
    <Stack screenOptions={screenOptions}>
      {/* Commerce browse chain — pushes stay in this stack, so back walks it
          back one hop at a time. */}
      <Stack.Screen name="subcategory/[subcategoryId]" options={{ title: 'Products' }} />
      <Stack.Screen name="product/[productId]" options={{ title: 'Product Details' }} />
      <Stack.Screen name="seller/[sellerId]" options={{ title: 'Seller' }} />
      <Stack.Screen name="service" options={{ title: 'Services' }} />
      {/* appService defaults to "" in AppContext, which rendered an empty
          header title when the form was opened without one being set. */}
      <Stack.Screen name="serviceaction" options={{ title: appService || 'Add Listing' }} />

      {/* Checkout draws its own header: it hides back on the Processing step,
          which a native header cannot express. */}
      <Stack.Screen name="checkout/index" options={{ headerShown: false }} />

      {/* Wallet actions reachable from Home and from the Wallet tab's sheets. */}
      <Stack.Screen name="fund" options={{ title: 'Receive' }} />
      <Stack.Screen name="send" options={{ title: 'Send' }} />
      <Stack.Screen name="swap" options={{ title: 'Swap' }} />
      <Stack.Screen name="multiswap" options={{ title: 'Multi-Asset Swap' }} />
      <Stack.Screen name="transfer" options={{ title: 'Transfer' }} />

      {/* Rewards trio — linked from Home, the services hub, the marketplace
          tab strip and the profile menu. */}
      <Stack.Screen name="trade" options={{ title: 'Trade & Earn' }} />
      <Stack.Screen name="rewards" options={{ title: 'Rewards & TP Levels' }} />
      <Stack.Screen name="cashback" options={{ title: 'Cashback' }} />
    </Stack>
  );
}
