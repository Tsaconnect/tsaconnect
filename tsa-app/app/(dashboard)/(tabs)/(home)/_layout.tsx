import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { defaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={defaultScreenOptions}>
        <Stack.Screen
          name="home"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="checkout"
          options={{ title: "Checkout" }}
        />
        <Stack.Screen
          name="fund"
          options={{ title: "Fund Wallet" }}
        />
        <Stack.Screen
          name="fundfiat"
          options={{ title: "Deposit Fiat" }}
        />
        <Stack.Screen
          name="send"
          options={{ title: "Send" }}
        />
        <Stack.Screen
          name="sendfiat"
          options={{ title: "Withdraw Fiat" }}
        />
        <Stack.Screen
          name="swap"
          options={{ title: "Swap" }}
        />
        <Stack.Screen
          name="multiswap"
          options={{ title: "Multi-Asset Swap" }}
        />
        <Stack.Screen
          name="trade"
          options={{ title: "Trade" }}
        />
        <Stack.Screen
          name="transfer"
          options={{ title: "Transfer" }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
