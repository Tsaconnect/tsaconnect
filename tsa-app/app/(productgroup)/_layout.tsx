import { StyleSheet, Text, View } from "react-native";
import React, { useContext } from "react";
import { Stack } from "expo-router";
import { AppContext } from "../../AuthContext/AuthContext";
const Layout = () => {
  const { category } = useContext(AppContext);
  return (
    <Stack>
      <Stack.Screen
        name="categoryproducts"
        options={{
          headerTitle: `${category} Products`,
        }}
      />
      <Stack.Screen
        name="orderproduct"
        options={{
          headerTitle: `Order Product(s)`,
        }}
      />
      <Stack.Screen
        name="productdetails"
        options={{
          headerTitle: `Product Detail`,
        }}
      />
      <Stack.Screen
        name="paymenttype"
        options={{
          headerTitle: `Payment Method`,
        }}
      />
      <Stack.Screen
        name="paymentproof"
        options={{
          headerTitle: `Payment Proof`,
        }}
      />
      <Stack.Screen
        name="paymentdetail"
        options={{
          headerTitle: `Payment Details`,
        }}
      />
      <Stack.Screen
        name="confirmation"
        options={{
          headerTitle: `Confirmation Page`,
        }}
      />
      <Stack.Screen
        name="cryptodetails"
        options={{
          headerTitle: `Crypto Details`,
        }}
      />
    </Stack>
  );
};

export default Layout;

const styles = StyleSheet.create({});
