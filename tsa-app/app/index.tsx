import { StyleSheet, Text, View, ActivityIndicator } from "react-native";
import React, { useEffect, useState } from "react";
import { router } from "expo-router";
import AsyncStorage from "@react-native-async-storage/async-storage";
import api from "@/components/services/api";

const Dashboard = () => {
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const initializeApp = async () => {
    try {
      setLoading(true);
      setError(null);

      // Check authentication using api service
      const isAuthenticated = await api.checkAuth();

      if (!isAuthenticated) {
        await api.clearAuth();
        router.replace("/login");
        return;
      }

      const role = await AsyncStorage.getItem("role");
      const token = await api.getStoredToken();

      if (!token) {
        await api.clearAuth();
        router.replace("/login");
        return;
      }

      api.setToken(token);

      try {
        const profileResponse = await api.getProfile();

        if (!profileResponse.success) {
          await api.clearAuth();
          router.replace("/login");
          return;
        }
      } catch (error) {
        console.error("Token verification error:", error);
        await api.clearAuth();
        router.replace("/login");
        return;
      }

      // Navigate based on role
      const role_value = role?.toLowerCase();

      if (role_value === "admin" || role_value === "superadmin") {
        router.replace("/admin/dashboard");
      } else if (role_value === "merchant") {
        router.replace("/merchants/dashboard");
      } else {
        router.replace("/home");
      }

    } catch (error: any) {
      console.error("Dashboard initialization error:", error);
      setError(error.message || "Failed to initialize app");

      await api.clearAuth();
      router.replace("/login");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    initializeApp();
  }, []);

  if (loading) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color="#0000ff" />
        <Text style={styles.loadingText}>Loading...</Text>
      </View>
    );
  }

  if (error) {
    return (
      <View style={styles.errorContainer}>
        <Text style={styles.errorTitle}>Error</Text>
        <Text style={styles.errorText}>{error}</Text>
        <Text style={styles.errorSubtext}>
          Please try again or contact support if the problem persists.
        </Text>
      </View>
    );
  }

  // Brief loading state while redirect happens
  return (
    <View style={styles.loadingContainer}>
      <ActivityIndicator size="large" color="#0000ff" />
    </View>
  );
};

export default Dashboard;

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: "#f5f5f5",
  },
  loadingText: {
    marginTop: 10,
    fontSize: 16,
    color: "#666",
  },
  errorContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    padding: 20,
    backgroundColor: "#f5f5f5",
  },
  errorTitle: {
    fontSize: 24,
    fontWeight: "bold",
    color: "#d32f2f",
    marginBottom: 10,
  },
  errorText: {
    fontSize: 16,
    color: "#666",
    textAlign: "center",
    marginBottom: 10,
  },
  errorSubtext: {
    fontSize: 14,
    color: "#999",
    textAlign: "center",
  },
});