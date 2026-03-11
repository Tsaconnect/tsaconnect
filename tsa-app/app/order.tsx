import { StyleSheet, Text, View } from "react-native";
import React, { useEffect } from "react";
import Order from "../components/onboarding/order";
import { router } from "expo-router";
import { useAuth } from "../AuthContext/AuthContext";

const Orders = () => {
  const { isAuthenticated } = useAuth();

  useEffect(() => {
    if (isAuthenticated) {
      //@ts-ignore
      router.push("/home");
    }
  }, []);
  return <Order />;
};

export default Orders;

const styles = StyleSheet.create({});
