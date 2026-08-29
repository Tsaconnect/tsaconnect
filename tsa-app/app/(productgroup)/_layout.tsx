// app/(productgroup)/_layout.tsx
//
// Stack layout for the product/commerce section (cart, category products,
// ordering, product details, the payment-method / proof / detail flow,
// confirmation, and crypto details). Header chrome comes from the shared
// useDefaultScreenOptions(); the dynamic `${category} Products` title still
// derives from AppContext exactly as before.
//
// `cart/index` was NOT registered here, so its header fell back to the route
// name. It is the group's most-linked entry point (product detail, product card,
// the cart badge), which also makes it route 0 of a fresh stack — the shared
// headerLeft is what gives it a back chevron.
import React, { useContext } from "react";
import { Stack } from "expo-router";
import { AppContext } from "../../AuthContext/AuthContext";
import { useDefaultScreenOptions } from "@/constants/navigation";
import { MARKETPLACE_TAB } from "@/constants/routes";

const Layout = () => {
  const { category } = useContext(AppContext);
  const screenOptions = useDefaultScreenOptions({ backFallback: MARKETPLACE_TAB });

  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen
        name="cart/index"
        options={{ title: "Cart" }}
      />
      <Stack.Screen
        name="categoryproducts"
        options={{ title: `${category} Products` }}
      />
      <Stack.Screen
        name="orderproduct"
        options={{ title: "Order Product(s)" }}
      />
      <Stack.Screen
        name="productdetails"
        options={{ title: "Product Detail" }}
      />
      <Stack.Screen
        name="paymenttype"
        options={{ title: "Payment Method" }}
      />
      <Stack.Screen
        name="paymentproof"
        options={{ title: "Payment Proof" }}
      />
      <Stack.Screen
        name="paymentdetail"
        options={{ title: "Payment Details" }}
      />
      <Stack.Screen
        name="confirmation"
        options={{ title: "Confirmation" }}
      />
      <Stack.Screen
        name="cryptodetails"
        options={{ title: "Crypto Details" }}
      />
    </Stack>
  );
};

export default Layout;
