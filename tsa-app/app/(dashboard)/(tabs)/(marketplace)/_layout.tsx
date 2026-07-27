import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={screenOptions}>
        {/* `index` is the tab ROOT: the drawer header covers it, so a native
            header here would be a stray second bar with a back chevron on a
            tab root. `products` and `digital` render the same self-contained
            MarketplaceScreen but are PUSHED — and that screen's chrome has no
            back button, so without a header they'd be a dead end. `pm`
            redirects on mount; a header would only flash. */}
        <Stack.Screen
          name="index"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="products"
          options={{ title: "Marketplace" }}
        />
        <Stack.Screen
          name="digital"
          options={{ title: "Digital Products" }}
        />
        <Stack.Screen
          name="pm"
          options={{ headerShown: false }}
        />
        {/* Unregistered routes fall back to the route NAME as their title, which
            now reads as "favourites"/"service" on the only header the screen
            has. buyp2p/sellp2p redirect on mount, so they get no header. */}
        <Stack.Screen
          name="favourites"
          options={{ title: "Favourites" }}
        />
        <Stack.Screen
          name="service"
          options={{ title: "Services" }}
        />
        <Stack.Screen
          name="buyp2p"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="sellp2p"
          options={{ headerShown: false }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
