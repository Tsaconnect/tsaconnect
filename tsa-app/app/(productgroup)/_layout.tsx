import React, { useContext } from "react";
import { Stack } from "expo-router";
import { AppContext } from "../../AuthContext/AuthContext";
import { defaultScreenOptions } from "../../constants/navigation";

const Layout = () => {
  const { category } = useContext(AppContext);
  return (
    <Stack screenOptions={defaultScreenOptions}>
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
