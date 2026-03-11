import { StyleSheet, Text, View, ActivityIndicator } from "react-native";
import React, { useEffect, useState } from "react";
import Products from "../components/onboarding/products";
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
        // Not authenticated, redirect to login
        await api.clearAuth(); // Clear any stale data
       // router.replace("/login");
        return;
      }

      // Get user role from AsyncStorage
      const role = await AsyncStorage.getItem("role");
      const token = await api.getStoredToken();

      if (!token) {
        // No token found, redirect to login
        await api.clearAuth();
       // router.replace("/login");
        return;
      }

      // Set token in api service
      api.setToken(token);

      // Verify token by making a simple API call
      try {
        // You can use any endpoint that requires authentication
        // Here we'll use the getProfile endpoint
        const profileResponse = await api.getProfile();

        if (!profileResponse.success) {
          // Token is invalid or expired
          await api.clearAuth();
         // router.replace("/login");
          return;
        }
      } catch (error) {
        console.error("Token verification error:", error);
        await api.clearAuth();
      //  router.replace("/login");
        return;
      }

      // Navigate based on role
      console.log('User role:', role);

      if (role === "admin") {
        router.replace("/admin/dashboard");
      } else if (role === "merchant") {
        router.replace("/merchants/dashboard");
      } else {
        router.replace("/home");
      }

    } catch (error: any) {
      console.error("Dashboard initialization error:", error);
      setError(error.message || "Failed to initialize app");

      // Clear auth data and redirect to login on error
      await api.clearAuth();
     // router.replace("/login");
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

  // This will only render briefly before redirecting
  return (
    <View style={styles.container}>
      <Products />
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