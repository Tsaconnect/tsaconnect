// app/index.tsx
//
// Root entry — auth check + role-based redirect. The redirect logic (auth/lock
// gating, network monitoring) is preserved verbatim; only the visible splash /
// loading chrome is restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives). CRITICAL ROUTING: every redirect target is unchanged.
import React, { useEffect, useState } from "react";
import { View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import * as Network from "expo-network";
import { useAuth } from "@/AuthContext/AuthContext";
import { isLockEnabled } from "@/services/localAuth";
import { goAfterAuth } from "@/utils/postAuthRoute";
import { useTheme } from "@/theme";
import { Icon, Spinner, Text } from "@/components/ui";

const EntryScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const { isAuthenticated, isHydrated } = useAuth();
  const [networkStatus, setNetworkStatus] = useState<"connected" | "slow" | "offline">("connected");

  useEffect(() => {
    if (!isHydrated) return;

    (async () => {
      if (isAuthenticated) {
        const lockOn = await isLockEnabled();
        if (lockOn) {
          router.replace("/lock");
        } else {
          await goAfterAuth(router);
        }
      } else {
        router.replace("/login");
      }
    })();
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <View
        style={{
          flex: 1,
          justifyContent: "center",
          alignItems: "center",
          paddingHorizontal: spacing["3xl"],
        }}
      >
        <View
          style={{
            width: 72,
            height: 72,
            borderRadius: radius.pill,
            backgroundColor: colors.accent,
            alignItems: "center",
            justifyContent: "center",
            marginBottom: spacing["2xl"],
          }}
        >
          <Text variant="title2" weight="800" style={{ color: colors.onAccent }}>
            TSA
          </Text>
        </View>

        <View style={{ marginBottom: spacing.lg }}>
          <Spinner size="large" />
        </View>

        {networkStatus === "offline" ? (
          <View style={{ alignItems: "center", gap: spacing.xs }}>
            <Icon name="wifiOff" size={20} color={colors.danger} />
            <Text variant="bodyMed" weight="600" color="danger" align="center">
              No internet connection
            </Text>
            <Text variant="bodySm" color="textMuted" align="center">
              Please check your network and restart the app
            </Text>
          </View>
        ) : networkStatus === "slow" ? (
          <View style={{ alignItems: "center", gap: spacing.xs }}>
            <Icon name="wifi" size={20} color={colors.warning} />
            <Text variant="bodyMed" weight="500" color="warning" align="center">
              Slow connection, please wait...
            </Text>
          </View>
        ) : (
          <Text variant="body" color="textSecondary" align="center">
            Loading...
          </Text>
        )}
      </View>
    </SafeAreaView>
  );
};

export default EntryScreen;
