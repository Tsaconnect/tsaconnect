 import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
export default function Layout() {
  return (
    <GestureHandlerRootView>
      <Stack>
        <Stack.Screen
          name="digital"
          options={{
            headerShown: false,
          }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
