import { StyleSheet, Text, View } from "react-native";
import React, { useState } from "react";
import Login from "../components/onboarding/Login";
import { useAuth } from "../AuthContext/AuthContext";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { router } from "expo-router";
import { baseUrl } from "../constants/api/apiClient";
import axios from "axios";

const LoginScreen = () => {
  const { setToken } = useAuth();
  const [initialized, setInitialized] = useState(false);

  const initialize = async () => {
    if (initialized) return;
    setInitialized(true);
    try {
      const value = await AsyncStorage.getItem("token");
      if (value) {
        setToken(value);
        await fetchCategories(value);
      }
    } catch (e) {
      console.error("Failed to get token", e);
    } finally {
      setInitialized(true);
    }
  };

  const fetchCategories = async (token: any) => {
    try {
      const response = await axios.get(`${baseUrl}/category?type=Product`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      //@ts-ignore
      router.push("/home");
    } catch (error) {
      //@ts-ignore
      if (error?.response?.data?.message === "Please authenticate") {
        await AsyncStorage.removeItem("token");
        //@ts-ignore
        router.push("/login");
      }
    }
  };

  if (!initialized) {
    initialize();
  }

  return <Login />;
};

export default LoginScreen;

const styles = StyleSheet.create({});
