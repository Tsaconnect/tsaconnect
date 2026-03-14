import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { defaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={defaultScreenOptions}>
        <Stack.Screen
          name="digital"
          options={{ headerShown: false }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
