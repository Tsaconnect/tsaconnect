import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={screenOptions}>
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
