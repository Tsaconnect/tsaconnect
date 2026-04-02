import { StyleSheet, Text, View, ActivityIndicator } from "react-native";
import React, { useEffect, useState } from "react";
import { router } from "expo-router";
import * as Network from "expo-network";
import { useAuth } from "@/AuthContext/AuthContext";
import { COLORS } from "@/constants/theme";
import MaterialIcons from "@expo/vector-icons/MaterialIcons";

const EntryScreen = () => {
  const { isAuthenticated, isHydrated } = useAuth();
  const [networkStatus, setNetworkStatus] = useState<"connected" | "slow" | "offline">("connected");

  useEffect(() => {
    if (!isHydrated) return;

    if (isAuthenticated) {
      router.replace("/home");
    } else {
      router.replace("/login");
    }
  }, [isHydrated, isAuthenticated]);

  // Monitor network state while loading
  useEffect(() => {
    if (isHydrated) return;

    let mounted = true;

    async function checkNetwork() {
      try {
        const state = await Network.getNetworkStateAsync();
        if (!mounted) return;

        if (!state.isConnected || !state.isInternetReachable) {
          setNetworkStatus("offline");
          return;
        }

        setNetworkStatus("connected");
      } catch {
        // Can't determine — assume connected
      }
    }

    checkNetwork();

    // If still loading after 5s, likely slow connection
    const slowTimer = setTimeout(() => {
      if (mounted) {
        setNetworkStatus((prev) => (prev === "offline" ? "offline" : "slow"));
      }
    }, 5000);

    return () => {
      mounted = false;
      clearTimeout(slowTimer);
    };
  }, [isHydrated]);

  return (
    <View style={styles.container}>
      <View style={styles.logoCircle}>
        <Text style={styles.logoText}>TSA</Text>
      </View>

      <ActivityIndicator size="large" color={COLORS.primary} style={styles.spinner} />

      {networkStatus === "offline" ? (
        <View style={styles.statusContainer}>
          <MaterialIcons name="wifi-off" size={20} color={COLORS.danger} />
          <Text style={styles.statusTextError}>No internet connection</Text>
          <Text style={styles.statusHint}>Please check your network and restart the app</Text>
        </View>
      ) : networkStatus === "slow" ? (
        <View style={styles.statusContainer}>
          <MaterialIcons name="signal-wifi-statusbar-connected-no-internet-4" size={20} color="#f59e0b" />
          <Text style={styles.statusTextWarn}>Slow connection, please wait...</Text>
        </View>
      ) : (
        <Text style={styles.loadingText}>Loading...</Text>
      )}
    </View>
  );
};

export default EntryScreen;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: "#fff",
  },
  logoCircle: {
    width: 72,
    height: 72,
    borderRadius: 36,
    backgroundColor: COLORS.primary,
    alignItems: "center",
    justifyContent: "center",
    marginBottom: 24,
  },
  logoText: {
    color: "#fff",
    fontSize: 24,
    fontWeight: "700",
  },
  spinner: {
    marginBottom: 16,
  },
  loadingText: {
    fontSize: 16,
    color: "#666",
  },
  statusContainer: {
    alignItems: "center",
    paddingHorizontal: 32,
  },
  statusTextError: {
    marginTop: 8,
    fontSize: 16,
    color: COLORS.danger,
    fontWeight: "600",
  },
  statusTextWarn: {
    marginTop: 8,
    fontSize: 16,
    color: "#f59e0b",
    fontWeight: "500",
  },
  statusHint: {
    marginTop: 4,
    fontSize: 14,
    color: "#999",
    textAlign: "center",
  },
});
