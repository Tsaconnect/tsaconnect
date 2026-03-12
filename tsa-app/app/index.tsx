import { StyleSheet, Text, View, ActivityIndicator } from "react-native";
import React, { useEffect, useState } from "react";
import { router } from "expo-router";
import AsyncStorage from "@react-native-async-storage/async-storage";
import api from "@/components/services/api";

const Dashboard = () => {
  const [loading, setLoading] = useState(true);

  const initializeApp = async () => {
    try {
      const token = await api.getStoredToken();

      if (!token) {
        router.replace("/login");
        return;
      }

      api.setToken(token);

      // Single API call to verify token + get profile
      const profileResponse = await api.getProfile();

      if (!profileResponse.success) {
        await api.clearAuth();
        router.replace("/login");
        return;
      }

      // Navigate based on role
      const role = (await AsyncStorage.getItem("role"))?.toLowerCase();

      if (role === "admin" || role === "superadmin") {
        router.replace("/admin/dashboard");
      } else if (role === "merchant") {
        router.replace("/merchants/dashboard");
      } else {
        router.replace("/home");
      }
    } catch (error: any) {
      console.error("App init error:", error);
      await api.clearAuth();
      router.replace("/login");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    initializeApp();
  }, []);

  return (
    <View style={styles.container}>
      <ActivityIndicator size="large" color="#9D6B38" />
      {loading && <Text style={styles.loadingText}>Loading...</Text>}
    </View>
  );
};

export default Dashboard;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: "#fff",
  },
  loadingText: {
    marginTop: 10,
    fontSize: 16,
    color: "#666",
  },
});
