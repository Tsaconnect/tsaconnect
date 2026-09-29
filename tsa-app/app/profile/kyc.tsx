// app/profile/kyc.tsx
//
// KYC identity-verification flow (Didit hosted session in a WebView).
// Status check → create session → camera permission → Didit WebView →
// callback/completion detection → poll status. Verified, a customer who was
// sent here from a pay link (a remembered pending payment) is offered the way
// straight back to it, through goAfterAuth.
import React, { useState, useCallback } from "react";
import { View, ActivityIndicator, Alert } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router, useLocalSearchParams } from "expo-router";
import { useFocusEffect } from "@react-navigation/native";
import api from "../../components/services/api";
import { useTheme } from "@/theme";
import { Text, Button, Icon, ScreenHeader } from "@/components/ui";
import { DiditWebView, requestCameraPermission } from "@/components/kyc/DiditSession";
import { StatusPanel } from "@/components/kyc/StatusPanel";
import { goAfterAuth, hasPendingPayment } from "@/utils/postAuthRoute";

type KYCState =
  | "not_started"
  | "loading"
  | "webview_active"
  | "in_progress"
  | "action_required"
  | "verified"
  | "rejected";

export default function KYCScreen() {
  const { colors, spacing } = useTheme();
  const [state, setState] = useState<KYCState>("loading");
  const [notes, setNotes] = useState("");
  const [sessionUrl, setSessionUrl] = useState<string | null>(null);
  // A payment she was on her way to make when KYC stopped her. Sent here from
  // that payment's screen (?returnTo=pay), it is still underneath: go back to
  // it. Otherwise (a later launch, the Didit callback) open it afresh.
  const [paymentWaiting, setPaymentWaiting] = useState(false);
  const { returnTo } = useLocalSearchParams<{ returnTo?: string }>();
  const continueToPayment = () => {
    if (returnTo === "pay" && router.canGoBack()) router.back();
    else void goAfterAuth(router);
  };

  const checkStatus = useCallback(async () => {
    try {
      const response = await api.getKYCStatus();
      if (response.success && response.data) {
        const { verificationStatus, actionRequired } = response.data;
        if (verificationStatus === "verified") {
          setPaymentWaiting(await hasPendingPayment());
          setState("verified");
        } else if (verificationStatus === "rejected") {
          setState("rejected");
          setNotes(response.data.verificationNotes || "Verification failed. Please try again.");
        } else if (verificationStatus === "in_review") {
          // in_review covers both "Didit is reviewing" and "Didit sent a step
          // back for resubmission" — only the latter needs the user to act.
          setState(actionRequired ? "action_required" : "in_progress");
        } else {
          setState("not_started");
        }
      }
    } catch (error) {
      setState("not_started");
    }
  }, []);

  useFocusEffect(
    useCallback(() => {
      checkStatus();
    }, [checkStatus])
  );

  const startVerification = async () => {
    try {
      const hasPermission = await requestCameraPermission();
      if (!hasPermission) return;

      setState("loading");
      const response = await api.createKYCSession();

      if (!response.success || !response.data) {
        Alert.alert("Error", response.message || "Failed to start verification");
        setState("not_started");
        return;
      }

      const url = response.data.sessionUrl || response.data.inquiryUrl;
      if (!url) {
        Alert.alert("Error", "Failed to start verification");
        setState("not_started");
        return;
      }
      setSessionUrl(url);
      setState("webview_active");
    } catch (error: any) {
      Alert.alert("Error", api.getErrorMessage(error) || "Something went wrong");
      setState("not_started");
    }
  };

  // Ask the API what the session actually ended as — hard-coding
  // "in progress" hides a resubmission request behind a wait screen.
  const handleSessionFinished = () => {
    setSessionUrl(null);
    setState("loading");
    checkStatus();
  };

  // Reachable cold from the Didit deep link (/kyc/callback redirects here), so
  // back needs a real destination rather than a no-op pop.
  const Header = () => (
    <ScreenHeader title="KYC Verification" fallbackRoute="/profile" />
  );

  if (state === "loading") {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <View style={{ flex: 1, alignItems: "center", justifyContent: "center", gap: spacing.md }}>
          <ActivityIndicator size="large" color={colors.accent} />
          <Text variant="bodySm" color="textSecondary">
            Loading...
          </Text>
        </View>
      </SafeAreaView>
    );
  }

  if (state === "webview_active" && sessionUrl) {
    return <DiditWebView url={sessionUrl} onFinished={handleSessionFinished} />;
  }

  if (state === "verified") {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <StatusPanel
          icon="checkCircle"
          iconColor={colors.success}
          iconBg={colors.successBg}
          title="Identity Verified"
          subtitle="Your identity has been successfully verified."
        >
          {paymentWaiting ? (
            <View style={{ width: "100%", gap: spacing.md }}>
              <Button title="Continue to payment" onPress={continueToPayment} fullWidth />
              <Button title="Go Back" variant="ghost" onPress={() => router.back()} fullWidth />
            </View>
          ) : (
            <Button title="Go Back" onPress={() => router.back()} fullWidth />
          )}
        </StatusPanel>
      </SafeAreaView>
    );
  }

  if (state === "action_required") {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <StatusPanel
          icon="upload"
          iconColor={colors.warning}
          iconBg={colors.warningBg}
          title="One More Step"
          subtitle="Your documents need another look — usually a clearer photo. Continue where you left off to resubmit."
        >
          <View style={{ width: "100%", gap: spacing.md }}>
            <Button title="Resubmit Documents" onPress={startVerification} fullWidth />
            <Button title="Refresh Status" variant="secondary" onPress={checkStatus} fullWidth />
            <Button title="Go Back" variant="ghost" onPress={() => router.back()} fullWidth />
          </View>
        </StatusPanel>
      </SafeAreaView>
    );
  }

  if (state === "in_progress") {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <StatusPanel
          icon="clock"
          iconColor={colors.warning}
          iconBg={colors.warningBg}
          title="Verification In Progress"
          subtitle="Your verification is being reviewed. If you didn't finish submitting, you can start again."
        >
          <View style={{ width: "100%", gap: spacing.md }}>
            <Button title="Continue Verification" onPress={startVerification} fullWidth />
            <Button title="Refresh Status" variant="secondary" onPress={checkStatus} fullWidth />
            <Button title="Go Back" variant="ghost" onPress={() => router.back()} fullWidth />
          </View>
        </StatusPanel>
      </SafeAreaView>
    );
  }

  if (state === "rejected") {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <StatusPanel
          icon="closeCircle"
          iconColor={colors.danger}
          iconBg={colors.dangerBg}
          title="Verification Failed"
          subtitle={notes}
        >
          <View style={{ width: "100%", gap: spacing.md }}>
            <Button title="Try Again" onPress={startVerification} fullWidth />
            <Button title="Go Back" variant="secondary" onPress={() => router.back()} fullWidth />
          </View>
        </StatusPanel>
      </SafeAreaView>
    );
  }

  // not_started
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <Header />
      <StatusPanel
        icon="shield"
        iconColor={colors.accent}
        iconBg={colors.accentMuted}
        title="Verify Your Identity"
        subtitle="Complete identity verification to unlock all features including buying, selling, and sending funds."
      >
        <View style={{ width: "100%", gap: spacing.sm, marginBottom: spacing.xl }}>
          {[
            "A valid government-issued ID",
            "A well-lit environment for a selfie",
          ].map((line) => (
            <View key={line} style={{ flexDirection: "row", alignItems: "center", gap: spacing.sm }}>
              <Icon name="checkCircle" size={16} color={colors.accent} />
              <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                {line}
              </Text>
            </View>
          ))}
        </View>
        <View style={{ width: "100%", gap: spacing.md }}>
          <Button title="Start Verification" onPress={startVerification} fullWidth />
          <Button title="Do This Later" variant="ghost" onPress={() => router.back()} fullWidth />
        </View>
      </StatusPanel>
    </SafeAreaView>
  );
}
