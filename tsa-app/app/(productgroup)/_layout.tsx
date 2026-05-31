// app/(productgroup)/_layout.tsx
//
// Stack layout for the product/commerce section (category products, ordering,
// product details, the payment-method / proof / detail flow, confirmation, and
// crypto details). Migrated onto the TSA Signature design system: header chrome
// (background / tint / title color / font) is now sourced from useTheme()
// tokens instead of the legacy `defaultScreenOptions` constant, so it tracks
// light & dark automatically. Screen registrations, names, order, and titles
// are preserved verbatim — the dynamic `${category} Products` title still
// derives from AppContext exactly as before.
import React, { useContext } from "react";
import { Stack } from "expo-router";
import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { AppContext } from "../../AuthContext/AuthContext";
import { useTheme, fontFamily } from "@/theme";

const Layout = () => {
  const { category } = useContext(AppContext);
  const { colors, type } = useTheme();

  const screenOptions: NativeStackNavigationOptions = {
    headerTitleAlign: "center",
    headerTintColor: colors.accent,
    headerTitleStyle: {
      fontSize: type.title3.fontSize,
      fontFamily: fontFamily["600"],
      color: colors.textPrimary,
    },
    headerStyle: {
      backgroundColor: colors.surface,
    },
    headerShadowVisible: false,
    contentStyle: {
      backgroundColor: colors.background,
    },
  };

  return (
    <Stack screenOptions={screenOptions}>
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
