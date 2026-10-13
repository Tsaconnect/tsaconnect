// components/profile/profile.tsx
//
// User profile screen content (rendered by the profile route) — avatar/identity
// header, email-verify + KYC/verify links, account links (adverts, orders,
// wishlist, balance, referral, referrals, settings), and logout.
//
// Migrated onto the TSA Signature design system: theme tokens (useTheme) +
// @/components/ui primitives, replacing the legacy hardcoded StyleSheet/colors
// and react-native-elements Avatar/Icon. VISUAL/STRUCTURAL restyle ONLY — the
// profile fetch, token/auth gating, edit navigation, KYC/verify links, referral
// copy, and logout flow are preserved verbatim.
import React, { useCallback, useEffect, useState } from "react";
import { View, TouchableOpacity, Alert, ScrollView } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import { useFocusEffect } from "@react-navigation/native";
import * as Clipboard from "expo-clipboard";
import api from "../services/api";
import { useAuth } from "@/AuthContext/AuthContext";
import { useTheme } from "@/theme";
import { NAME_CHANGE, TSA_PAY_APPLY } from "@/constants/routes";
import { useNameChangeEntry } from "@/hooks/useNameChangeEntry";
import { useIsPaymentMerchant } from "@/hooks/useIsPaymentMerchant";
import { lpCta } from "@/utils/lpCta";
import {
  AccentCard,
  Avatar,
  Button,
  Card,
  Divider,
  Icon,
  IconButton,
  IconName,
  PressableScale,
  Spinner,
  Text,
} from "@/components/ui";

const ProfileScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const { logOut } = useAuth();
  const [user, setUser] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [token, setToken] = useState<string | null>(null);
  const nameChangeOffered = useNameChangeEntry(user?.verificationStatus === "verified");

  // Get token on component mount — redirect if not authenticated
  useEffect(() => {
    const getToken = async () => {
      try {
        const storedToken = await api.getStoredToken();
        if (!storedToken) {
          router.replace("/login");
          return;
        }
        setToken(storedToken);
      } catch (error) {
        console.error("Error getting token:", error);
        router.replace("/login");
      }
    };
    getToken();
  }, []);

  async function getLoggedInUser() {
    try {
      setLoading(true);

      // Check if we have a token
      if (!token) {
        Alert.alert("Error", "Not authenticated. Please login again.");
        router.replace("/login");
        return;
      }

      // Use API service to get profile
      const response = await api.getProfile();

      if (response.success && response.data) {
        setUser(response.data);
      } else {
        Alert.alert("Error", response.message || "Failed to load user data");
      }
    } catch (error: any) {
      console.error("Error fetching user:", error);
      Alert.alert(
        "Error",
        api.getErrorMessage(error) || "Failed to load profile"
      );
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (token) {
      getLoggedInUser();
    }
  }, [token]);

  useFocusEffect(
    useCallback(() => {
      if (token) {
        getLoggedInUser();
      }
    }, [token])
  );

  const copyToClipboard = () => {
    if (!user?.referralCode) {
      Alert.alert("Error", "No referral code available");
      return;
    }

    Clipboard.setStringAsync(user.referralCode);
    Alert.alert("Copied to clipboard", "Referral code copied to clipboard!");
  };

  const handleVerifyEmail = async () => {
    try {
      if (!token) {
        Alert.alert("Error", "Not authenticated");
        return;
      }
      const result = await api.sendOtp();
      if (result.success) {
        router.push({
          pathname: "/verify",
          params: { email: user?.email },
        });
      } else {
        Alert.alert("Error", result.message || "Failed to send verification code.");
      }
    } catch (error: any) {
      Alert.alert("Error", "An error occurred while sending verification code.");
    }
  };

  const handleKycVerification = () => {
    router.push("/profile/kyc");
  };

  // Handle logout
  // The route guard takes a signed-out user to /login.
  const handleLogout = async () => {
    try {
      await logOut();
    } catch (error) {
      console.error("Logout error:", error);
      Alert.alert("Error", "Failed to logout");
    }
  };

  // Get user initials for avatar fallback
  const getUserInitials = (name: string) => {
    if (!name) return '?';
    return name.charAt(0).toUpperCase();
  };

  // Account links rendered as a single Card list. Each row preserves its exact
  // navigation/handler; the balance + referral rows render custom right content.
  type LinkRow = {
    key: string;
    icon: IconName;
    label: string;
    onPress?: () => void;
  };
  const linkRows: LinkRow[] = [
    { key: "adverts", icon: "list", label: "My Adverts", onPress: () => router.push("/myAdverts") },
    { key: "orders", icon: "list", label: "Orders", onPress: () => router.push("/orderlist") },
    { key: "wishlist", icon: "star", label: "Wishlist" },
  ];
  // TSA Liquidity lives here and on the merchant dashboard, not on the P2P
  // screens: apply, or open your liquidity once you are an LP.
  const pm = useIsPaymentMerchant();
  const liquidity = lpCta({ isPm: pm.isPm, kind: pm.kind, lpStatus: pm.lpStatus, liquidityAvailable: pm.liquidityAvailable, loading: pm.loading });
  const lowerLinkRows: LinkRow[] = [
    { key: "trade", icon: "trendingUp", label: "Trade & Earn", onPress: () => router.push("/trade") },
    { key: "referrals", icon: "user", label: "My Referrals", onPress: () => router.push("/referrals") },
    { key: "tsaPay", icon: "card", label: "Accept payments with TSA Pay", onPress: () => router.push(TSA_PAY_APPLY) },
    ...(liquidity
      ? [{ key: "liquidity", icon: "wallet" as IconName, label: liquidity.title, onPress: () => router.push(liquidity.route as never) }]
      : []),
    { key: "settings", icon: "construction", label: "Settings", onPress: () => router.push("/(dashboard)/settings") },
  ];

  const Row: React.FC<{
    icon: IconName;
    onPress?: () => void;
    children: React.ReactNode;
    showChevron?: boolean;
    last?: boolean;
  }> = ({ icon, onPress, children, showChevron, last }) => (
    <PressableScale onPress={onPress} disabled={!onPress}>
      <View
        style={{
          flexDirection: "row",
          alignItems: "center",
          gap: spacing.md,
          paddingVertical: spacing.md,
        }}
      >
        <Icon name={icon} size={20} color={colors.accent} />
        <View style={{ flex: 1 }}>{children}</View>
        {showChevron && onPress && <Icon name="chevronRight" size={18} color={colors.textMuted} />}
      </View>
      {!last && <Divider />}
    </PressableScale>
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      {/* Header is the ROUTE's job, not this component's: two routes render
          this screen. app/(dashboard)/profile.tsx is a drawer destination and
          already gets the drawer's native "Profile" header, so drawing one here
          stacked two nav bars. Only the standalone app/profile/index.tsx (which
          sits outside the drawer with headerShown:false) needs one. */}
      {loading || !user ? (
        <View style={{ flex: 1, justifyContent: "center", alignItems: "center" }}>
          <Spinner size="large" label="Loading profile..." />
        </View>
      ) : (
        <ScrollView
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing["4xl"] }}
        >
          {/* Identity header */}
          <AccentCard padding="xl">
            <View style={{ flexDirection: "row", justifyContent: "flex-end" }}>
              <IconButton
                icon="edit"
                accessibilityLabel="Edit profile"
                onPress={() => router.push({ pathname: "/profile/edit", params: user })}
              />
            </View>
            <View style={{ alignItems: "center", justifyContent: "center" }}>
              <Avatar
                name={getUserInitials(user.name)}
                uri={user?.profilePicture || undefined}
                size={88}
              />
              <View style={{ alignItems: "center", marginTop: spacing.md }}>
                <Text variant="title2" weight="700" align="center">
                  {user.name}
                </Text>
                {user?.username ? (
                  <Text variant="bodySm" weight="600" color="accent" align="center">
                    @{user.username}
                  </Text>
                ) : null}
                {user?.phoneNumber ? (
                  <Text variant="bodySm" color="textSecondary" align="center">
                    {user.phoneNumber}
                  </Text>
                ) : null}
              </View>
            </View>

            {!user.emailVerified && (
              <Button
                title="Verify Email"
                leftIcon="mail"
                onPress={handleVerifyEmail}
                style={{ marginTop: spacing.lg }}
              />
            )}

            {user.verificationStatus === 'verified' ? (
              <View
                style={{
                  flexDirection: "row",
                  alignItems: "center",
                  justifyContent: "center",
                  gap: spacing.sm,
                  marginTop: spacing.md,
                }}
              >
                <Icon name="verified" size={20} color={colors.success} />
                <Text variant="bodyMed" weight="600" color="success">
                  Verified
                </Text>
                {/* A verified name is the one on her ID: it changes by verifying again. */}
                {nameChangeOffered ? (
                  <Button
                    title="Change name"
                    size="sm"
                    variant="ghost"
                    fullWidth={false}
                    onPress={() => router.push(NAME_CHANGE)}
                  />
                ) : null}
              </View>
            ) : user.verificationStatus === 'in_review' ? (
              <PressableScale onPress={handleKycVerification} style={{ marginTop: spacing.md }}>
                <View
                  style={{
                    flexDirection: "row",
                    alignItems: "center",
                    justifyContent: "center",
                    gap: spacing.sm,
                    paddingVertical: spacing.md,
                    borderRadius: radius.md,
                    backgroundColor: colors.warningBg,
                  }}
                >
                  <Icon name="clock" size={20} color={colors.warning} />
                  <Text variant="bodyMed" weight="600" color="warning">
                    Verification In Progress
                  </Text>
                </View>
              </PressableScale>
            ) : (
              <Button
                title={
                  user.verificationStatus === 'rejected'
                    ? 'Verification Failed - Try Again'
                    : 'Verify Identity'
                }
                leftIcon="shield"
                onPress={handleKycVerification}
                style={{ marginTop: spacing.lg }}
              />
            )}
          </AccentCard>

          {/* Account links */}
          <Card padding="lg" style={{ marginTop: spacing.lg }}>
            {linkRows.map((r) => (
              <Row key={r.key} icon={r.icon} onPress={r.onPress} showChevron>
                <Text variant="body" weight="500">
                  {r.label}
                </Text>
              </Row>
            ))}

            {/* Balance row */}
            <Row icon="wallet">
              <View style={{ flexDirection: "row", alignItems: "center", justifyContent: "space-between" }}>
                <Text variant="body" weight="500">
                  My Balance
                </Text>
                <Text variant="bodyMed" weight="700" numeric>
                  {user?.balance || "0"} USD
                </Text>
              </View>
            </Row>

            {/* Referral code row */}
            <Row icon="gift" last>
              <View style={{ flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: spacing.sm }}>
                <View style={{ flex: 1 }}>
                  <Text variant="caption" color="textMuted">
                    Referral Code
                  </Text>
                  <Text variant="bodyMed" weight="600" numeric numberOfLines={1}>
                    {user?.referralCode || "N/A"}
                  </Text>
                </View>
                <Button title="Copy" size="sm" variant="secondary" leftIcon="copy" fullWidth={false} onPress={copyToClipboard} />
              </View>
            </Row>
          </Card>

          {/* Secondary links */}
          <Card padding="lg" style={{ marginTop: spacing.lg }}>
            {lowerLinkRows.map((r) => (
              <Row key={r.key} icon={r.icon} onPress={r.onPress} showChevron last={r.key === "settings"}>
                <Text variant="body" weight="500">
                  {r.label}
                </Text>
              </Row>
            ))}
          </Card>

          {/* Logout */}
          <Button
            title="Logout"
            variant="danger"
            leftIcon="close"
            onPress={handleLogout}
            style={{ marginTop: spacing.lg }}
          />
        </ScrollView>
      )}
    </SafeAreaView>
  );
};

export default ProfileScreen;
