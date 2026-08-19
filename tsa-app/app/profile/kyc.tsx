// app/profile/kyc.tsx
//
// KYC identity-verification flow (Didit hosted session in a WebView).
// Status check → create session → camera permission → Didit WebView →
// callback/completion detection → poll status.
import React, { useState, useCallback } from "react";
import { View, ActivityIndicator, Alert, Linking } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import { Camera } from "expo-camera";
import { WebView } from "react-native-webview";
import { useFocusEffect } from "@react-navigation/native";
import api from "../../components/services/api";
import { useTheme } from "@/theme";
import { Text, Button, IconButton, Icon } from "@/components/ui";

type KYCState =
  | "not_started"
  | "loading"
  | "webview_active"
  | "in_progress"
  | "action_required"
  | "verified"
  | "rejected";

export default function KYCScreen() {
  const { colors, spacing, radius } = useTheme();
  const [state, setState] = useState<KYCState>("loading");
  const [notes, setNotes] = useState("");
  const [sessionUrl, setSessionUrl] = useState<string | null>(null);

  const checkStatus = useCallback(async () => {
    try {
      const response = await api.getKYCStatus();
      if (response.success && response.data) {
        const { verificationStatus, actionRequired } = response.data;
        if (verificationStatus === "verified") {
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

  const requestCameraPermission = async (): Promise<boolean> => {
    const { status } = await Camera.requestCameraPermissionsAsync();
    if (status === "granted") return true;

    Alert.alert(
      "Camera Permission Required",
      "Camera access is needed for identity verification. Please enable it in your device settings.",
      [
        { text: "Cancel", style: "cancel" },
        { text: "Open Settings", onPress: () => Linking.openSettings() },
      ]
    );
    return false;
  };

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

  const handleWebViewNavigationChange = (navState: { url: string }) => {
    // Didit redirects to callback with verificationSessionId + status,
    // or to our tsaconnect://kyc/callback deep link.
    const url = navState.url.toLowerCase();
    const finished =
      url.includes("verificationsessionid=") ||
      url.includes("tsaconnect://kyc/callback") ||
      url.includes("/kyc/callback") ||
      url.includes("status=approved") ||
      url.includes("status=declined") ||
      url.includes("status=in%20review") ||
      url.includes("status=in+review");
    if (finished) {
      // Ask the API what the session actually ended as — hard-coding
      // "in progress" hides a resubmission request behind a wait screen.
      setSessionUrl(null);
      setState("loading");
      checkStatus();
    }
  };

  const Header = () => (
    <View
      style={{
        flexDirection: "row",
        alignItems: "center",
        justifyContent: "space-between",
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.sm,
        borderBottomWidth: 1,
        borderBottomColor: colors.hairline,
      }}
    >
      <IconButton
        icon="chevronLeft"
        accessibilityLabel="Go back"
        onPress={() => router.back()}
      />
      <Text variant="title3" weight="700">
        KYC Verification
      </Text>
      <View style={{ width: 44 }} />
    </View>
  );

  // Reusable centered status panel: hero icon ring + title + subtitle + actions.
  const StatusPanel = ({
    icon,
    iconColor,
    iconBg,
    title,
    subtitle,
    children,
  }: {
    icon: React.ComponentProps<typeof Icon>["name"];
    iconColor: string;
    iconBg: string;
    title: string;
    subtitle?: string;
    children?: React.ReactNode;
  }) => (
    <View
      style={{
        flex: 1,
        alignItems: "center",
        justifyContent: "center",
        paddingHorizontal: spacing.lg,
      }}
    >
      <View
        style={{
          width: 96,
          height: 96,
          borderRadius: radius.pill,
          backgroundColor: iconBg,
          alignItems: "center",
          justifyContent: "center",
          marginBottom: spacing.xl,
        }}
      >
        <Icon name={icon} size={48} color={iconColor} />
      </View>
      <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.md }}>
        {title}
      </Text>
      {subtitle && (
        <Text
          variant="bodySm"
          color="textSecondary"
          align="center"
          style={{ marginBottom: spacing.xl }}
        >
          {subtitle}
        </Text>
      )}
      {children}
    </View>
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
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <WebView
          source={{ uri: sessionUrl }}
          onNavigationStateChange={handleWebViewNavigationChange}
          javaScriptEnabled
          domStorageEnabled
          mediaPlaybackRequiresUserAction={false}
          allowsInlineMediaPlayback
          mediaCapturePermissionGrantType="grant"
          allowsBackForwardNavigationGestures={false}
          onPermissionRequest={(event: any) => event.grant()}
          style={{ flex: 1 }}
        />
      </SafeAreaView>
    );
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
          <Button title="Go Back" onPress={() => router.back()} fullWidth />
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
