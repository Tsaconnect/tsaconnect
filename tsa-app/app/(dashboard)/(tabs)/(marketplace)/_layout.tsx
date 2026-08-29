// app/(dashboard)/(tabs)/(marketplace)/_layout.tsx
//
// The Marketplace tab. `index` is the tab ROOT — the drawer header covers it, so
// a native header here would be a stray second bar with a back chevron on a tab
// root.
//
// Everything else in this group is now either a legacy alias that redirects
// (`products`, `digital` rendered the SAME MarketplaceScreen as the tab root, so
// pushing one stacked an identical Marketplace on top of it) or a rail redirect
// (`pm`, `buyp2p`, `sellp2p`). None of them paint, so none of them take a header.
//
// `service` — the service-category browse list — moved to the root-level
// `(detail)` group: it is opened from the Services tab, and a screen owned by
// the Marketplace tab sends back to Marketplace rather than to its caller.
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "@/constants/navigation";
import { MARKETPLACE_TAB } from "@/constants/routes";

export default function Layout() {
  const screenOptions = useDefaultScreenOptions({ backFallback: MARKETPLACE_TAB });
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={screenOptions}>
        <Stack.Screen name="index" options={{ headerShown: false }} />
        <Stack.Screen name="favourites" options={{ title: "Favourites" }} />
        <Stack.Screen name="products" options={{ headerShown: false }} />
        <Stack.Screen name="digital" options={{ headerShown: false }} />
        <Stack.Screen name="pm" options={{ headerShown: false }} />
        <Stack.Screen name="buyp2p" options={{ headerShown: false }} />
        <Stack.Screen name="sellp2p" options={{ headerShown: false }} />
      </Stack>
    </GestureHandlerRootView>
  );
}
