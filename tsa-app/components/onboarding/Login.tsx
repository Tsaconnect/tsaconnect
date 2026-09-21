// Login form (rendered by app/login.tsx).
//
// Migrated onto the TSA Signature design system: legacy constants/theme styling
// replaced with @/theme tokens + @/components/ui primitives. AUTH logic
// (login API call, biometric/refresh-token session restore, credential state,
// validation, error handling, and post-login navigation) is preserved verbatim.
import React, { useState, useEffect } from "react";
import { Alert, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router, Link } from "expo-router";
import api from "../services/api";
import { useAuth } from "../../AuthContext/AuthContext";
import {
  isLockEnabled, isBiometricAvailable,
  authenticateWithBiometric, getBiometricType,
} from "../../services/localAuth";
import { useTheme } from "@/theme";
import {
  AccentCard,
  Button,
  Divider,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Text,
} from "@/components/ui";

export default function Login() {
  const { colors, spacing, radius } = useTheme();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const [emailError, setEmailError] = useState("");
  const [passwordError, setPasswordError] = useState("");
  const [generalError, setGeneralError] = useState("");
  const [showBiometric, setShowBiometric] = useState(false);
  const [bioType, setBioType] = useState("Fingerprint");
  const { setAuthenticated, setEmailVerified, setCurrentUser, setToken: setAuthToken } = useAuth();

  // Check if user can use biometric or PIN login via stored refresh token
  useEffect(() => {
    (async () => {
      const { hasRefreshToken } = await import('../../services/localAuth');
      const hasToken = await hasRefreshToken();
      if (!hasToken) return;

      const lockOn = await isLockEnabled();
      if (!lockOn) return;

      const bioAvail = await isBiometricAvailable();

      // Show biometric if hardware is available
      if (bioAvail) {
        setShowBiometric(true);
        setBioType(await getBiometricType());
      }
    })();
  }, []);

  // Restore session after biometric/PIN verification using refresh token
  const restoreSession = async () => {
    setLoading(true);
    try {
      const refreshResult = await api.refreshSession();
      if (refreshResult.success && refreshResult.data) {
        setAuthenticated(true);
        setAuthToken(refreshResult.data.accessToken);
        try {
          const response = await api.getProfile();
          if (response.success && response.data) {
            setCurrentUser(response.data);
            setEmailVerified(response.data.emailVerified ?? false);
          }
        } catch {}
        router.replace("/home");
        return;
      }

      // Refresh failed — clear and show error
      const { clearRefreshToken } = await import('../../services/localAuth');
      await clearRefreshToken();
      setShowBiometric(false);
      setGeneralError("Session expired. Please sign in with your password.");
    } catch (error) {
      setGeneralError("Failed to authenticate. Please try again.");
    } finally {
      setLoading(false);
    }
  };

  const handleBiometricLogin = async () => {
    const success = await authenticateWithBiometric();
    if (!success) return;
    await restoreSession();
  };


  function clearErrors() {
    setEmailError("");
    setPasswordError("");
    setGeneralError("");
  }

  async function handleLogin() {
    clearErrors();

    let hasError = false;
    if (!email.trim()) {
      setEmailError("Email or username is required");
      hasError = true;
    }
    if (!password) {
      setPasswordError("Password is required");
      hasError = true;
    }
    if (hasError) return;

    setLoading(true);
    try {
      const response = await api.login(email.trim(), password.trim());
      if (response.success && response.data) {
        // Sync AuthContext with login data
        setAuthenticated(true);
        setAuthToken(response.data.token);
        setEmailVerified(response.data.emailVerified ?? false);
        setCurrentUser(response.data);
        setEmail("");
        setPassword("");
        router.replace("/home");
      } else if (response.code === "ACCOUNT_DELETED_GRACE_PERIOD") {
        const daysLeft = response.data?.graceDaysLeft;
        const daysText = daysLeft ? ` (${daysLeft} day${daysLeft === 1 ? "" : "s"} left)` : "";
        Alert.alert(
          "Account Scheduled for Deletion",
          `This account was deactivated and is scheduled for permanent deletion${daysText}. Would you like to restore your account and sign in?`,
          [
            { text: "Cancel", style: "cancel" },
            {
              text: "Restore Account",
              onPress: async () => {
                setLoading(true);
                try {
                  const restoreRes = await api.reactivateAccount(email.trim(), password.trim());
                  if (restoreRes.success && restoreRes.data) {
                    setAuthenticated(true);
                    setAuthToken(restoreRes.data.token);
                    setEmailVerified(restoreRes.data.emailVerified ?? false);
                    setCurrentUser(restoreRes.data);
                    setEmail("");
                    setPassword("");
                    router.replace("/home");
                  } else {
                    setGeneralError(restoreRes.message || "Failed to restore account. Please try again.");
                  }
                } catch (err: any) {
                  setGeneralError(err.message || "Failed to restore account.");
                } finally {
                  setLoading(false);
                }
              },
            },
          ]
        );
      } else {
        setGeneralError(
          response.message || "Invalid credentials. Please try again."
        );
      }
    } catch (error: any) {
      setGeneralError(
        api.getErrorMessage(error) ||
          "Unable to connect to server. Please check your internet connection."
      );
    } finally {
      setLoading(false);
    }
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <ScrollView
        contentContainerStyle={{
          flexGrow: 1,
          justifyContent: "center",
          padding: spacing.lg,
        }}
        keyboardShouldPersistTaps="handled"
      >
        <View style={{ width: "100%", maxWidth: 400, alignSelf: "center" }}>
          <View style={{ alignItems: "center", marginBottom: spacing["3xl"] }}>
            <View
              style={{
                width: 72,
                height: 72,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
                alignItems: "center",
                justifyContent: "center",
                marginBottom: spacing.lg,
              }}
            >
              <Text variant="title2" weight="700" style={{ color: colors.onAccent }}>
                TSA
              </Text>
            </View>
            <Text variant="title1" weight="700" align="center">
              Welcome Back
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
              Sign in to your account
            </Text>
          </View>

          <View>
            <Input
              value={email}
              onChangeText={(text) => {
                setEmail(text);
                if (emailError) setEmailError("");
                if (generalError) setGeneralError("");
              }}
              placeholder="Email or Username"
              keyboardType="email-address"
              autoCapitalize="none"
              autoComplete="email"
              editable={!loading}
              error={emailError || undefined}
              containerStyle={{ marginBottom: spacing.md }}
              leftAdornment={
                <Icon
                  name="user"
                  size={20}
                  color={emailError ? colors.danger : colors.textMuted}
                />
              }
            />

            <Input
              value={password}
              placeholder="Password"
              secureTextEntry={!isPasswordVisible}
              autoCapitalize="none"
              autoComplete="password"
              onChangeText={(text) => {
                setPassword(text);
                if (passwordError) setPasswordError("");
                if (generalError) setGeneralError("");
              }}
              editable={!loading}
              error={passwordError || undefined}
              containerStyle={{ marginBottom: spacing.xs }}
              leftAdornment={
                <Icon
                  name="lock"
                  size={20}
                  color={passwordError ? colors.danger : colors.textMuted}
                />
              }
              rightAdornment={
                <IconButton
                  icon={isPasswordVisible ? "eye" : "eyeOff"}
                  size={20}
                  color={loading ? colors.textDisabled : colors.textMuted}
                  disabled={loading}
                  onPress={() => setIsPasswordVisible(!isPasswordVisible)}
                  accessibilityLabel={isPasswordVisible ? "Hide password" : "Show password"}
                  style={{ width: 32, height: 32 }}
                />
              }
            />

            <Link href="/recovery" asChild disabled={loading}>
              <PressableScale
                disabled={loading}
                style={{ alignSelf: "flex-end", marginTop: spacing.xs, marginBottom: spacing.xl }}
              >
                <Text
                  variant="bodySm"
                  weight="500"
                  color={loading ? "textDisabled" : "accent"}
                >
                  Forgot Password?
                </Text>
              </PressableScale>
            </Link>

            {generalError ? (
              <AccentCard
                railColor={colors.danger}
                padding="md"
                style={{ backgroundColor: colors.dangerBg, marginBottom: spacing.lg }}
              >
                <View style={{ flexDirection: "row", alignItems: "center", gap: spacing.sm }}>
                  <Icon name="warning" size={16} color={colors.danger} />
                  <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                    {generalError}
                  </Text>
                </View>
              </AccentCard>
            ) : null}

            <Button
              title="Login"
              onPress={handleLogin}
              loading={loading}
              disabled={loading}
              size="lg"
            />

            {showBiometric && (
              <Button
                title={`Login with ${bioType}`}
                variant="secondary"
                size="lg"
                leftIcon="fingerprint"
                disabled={loading}
                onPress={handleBiometricLogin}
                style={{ marginTop: spacing.md }}
              />
            )}

            <View
              style={{
                flexDirection: "row",
                alignItems: "center",
                marginVertical: spacing["2xl"],
              }}
            >
              <View style={{ flex: 1 }}>
                <Divider />
              </View>
              <Text variant="bodySm" weight="500" color="textMuted" style={{ marginHorizontal: spacing.lg }}>
                OR
              </Text>
              <View style={{ flex: 1 }}>
                <Divider />
              </View>
            </View>

            <View
              style={{
                flexDirection: "row",
                justifyContent: "center",
                alignItems: "center",
                gap: spacing.sm,
              }}
            >
              <Text variant="bodySm" color="textSecondary">
                New to TSA Connect?
              </Text>
              <PressableScale
                onPress={() => router.push("/signup")}
                disabled={loading}
              >
                <Text
                  variant="bodySm"
                  weight="600"
                  color={loading ? "textDisabled" : "accent"}
                >
                  Create Account
                </Text>
              </PressableScale>
            </View>
          </View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}
