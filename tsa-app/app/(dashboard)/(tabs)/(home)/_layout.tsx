import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
export default function Layout() {
  return (
    <GestureHandlerRootView>
      <Stack>
        <Stack.Screen
          name="home"
          options={{
            headerShown: false,
          }}
        />
        <Stack.Screen
          name="checkout"
          options={{
            headerTitle: "Checkout",
            headerShown: true,
            headerTitleAlign: "center",
            headerTitleStyle: {
              fontSize: 24,
              fontWeight: "bold",
            },

            title: "Checkout",
          }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
