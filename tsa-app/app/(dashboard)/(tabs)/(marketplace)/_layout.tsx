import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={screenOptions}>
        {/* index / products / digital all render the self-contained
            MarketplaceScreen (own top chrome), so the native Stack header would
            be a stray second header with a "back" chevron on a tab root. Hide
            it for these; secondary screens keep the default header. */}
        <Stack.Screen
          name="index"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="products"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="digital"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="pm"
          options={{ headerShown: false }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
