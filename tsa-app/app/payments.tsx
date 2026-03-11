import { StyleSheet, Text, View } from "react-native";
import React, { useEffect } from "react";
import Payments from "../components/onboarding/payments";
import { router } from "expo-router";
import { useAuth } from "../AuthContext/AuthContext";

const Payment = () => {
  const { isAuthenticated } = useAuth();

  useEffect(() => {
    if (isAuthenticated) {
      //@ts-ignore
      router.push("/home");
    }
  }, []);
  return <Payments />;
};

export default Payment;

const styles = StyleSheet.create({});
