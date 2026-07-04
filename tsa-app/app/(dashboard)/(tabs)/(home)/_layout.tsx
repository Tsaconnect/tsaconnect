import { GestureHandlerRootView } from "react-native-gesture-handler";
import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  const screenOptions = useDefaultScreenOptions();
  return (
    <GestureHandlerRootView>
      <Stack screenOptions={screenOptions}>
        <Stack.Screen
          name="home"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="checkout/index"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="fund"
          options={{ title: "Receive" }}
        />
        <Stack.Screen
          name="fundfiat"
          options={{ title: "Fiat Deposit" }}
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
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="rewards"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="cashback"
          options={{ headerShown: false }}
        />
        <Stack.Screen
          name="transfer"
          options={{ title: "Transfer" }}
        />
        <Stack.Screen
          name="product/[productId]"
          options={{ title: "Product Details" }}
        />
        <Stack.Screen
          name="seller/[sellerId]"
          options={{ title: "Seller" }}
        />
        <Stack.Screen
          name="subcategory/[subcategoryId]"
          options={{ title: "Products" }}
        />
      </Stack>
    </GestureHandlerRootView>
  );
}
