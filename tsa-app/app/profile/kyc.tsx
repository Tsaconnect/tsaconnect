// app/profile/kyc.tsx
//
// KYC identity-verification flow (Didit hosted session in a WebView).
// Status check → name as on the ID → create session → camera permission → Didit WebView →
// callback/completion detection → poll status. Verified, a customer who was
// sent here from a pay link (a remembered pending payment) is offered the way
// straight back to it, through goAfterAuth.
import React, { useState, useCallback } from "react";
import { View, ActivityIndicator, Alert, ScrollView, KeyboardAvoidingView, Platform } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router, useLocalSearchParams } from "expo-router";
import { useFocusEffect } from "@react-navigation/native";
import api from "../../components/services/api";
import { useTheme } from "@/theme";
import { Text, Button, Icon, Input, ScreenHeader } from "@/components/ui";
import { DiditWebView, requestCameraPermission } from "@/components/kyc/DiditSession";
import { StatusPanel } from "@/components/kyc/StatusPanel";
import { goAfterAuth, hasPendingPayment } from "@/utils/postAuthRoute";

type KYCState =
  | "not_started"
  | "name_entry"
  | "loading"
  | "webview_active"
  | "in_progress"
  | "under_review"
  | "action_required"
  | "verified"
  | "rejected";

export default function KYCScreen() {
  const { colors, spacing } = useTheme();
  const [state, setState] = useState<KYCState>("loading");
  const [notes, setNotes] = useState("");
  const [sessionUrl, setSessionUrl] = useState<string | null>(null);
  // The name exactly as printed on her ID. Didit is asked to compare it with
  // the document, and when Didit can't read the document (the NIN slip) an
  // admin checks it against the image — it is never trusted on its own.
  const [givenNames, setGivenNames] = useState("");
  const [surname, setSurname] = useState("");
  const [nameError, setNameError] = useState("");
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
        const { verificationStatus, actionRequired, diditStatus } = response.data;
        if (response.data.declaredGivenNames) setGivenNames(response.data.declaredGivenNames);
        if (response.data.declaredSurname) setSurname(response.data.declaredSurname);
        if (verificationStatus === "verified") {
          setPaymentWaiting(await hasPendingPayment());
          setState("verified");
        } else if (verificationStatus === "rejected") {
          setState("rejected");
          setNotes(response.data.verificationNotes || "Verification failed. Please try again.");
        } else if (verificationStatus === "in_review") {
          // in_review covers both "Didit is reviewing" and "Didit sent a step
          // back for resubmission" — only the latter needs the user to act.
          // While Didit's reviewer has it there is nothing to submit, and the
          // API refuses a new session (it would only queue a duplicate).
          if (actionRequired) setState("action_required");
          else if (diditStatus === "In Review") setState("under_review");
          else setState("in_progress");
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

  // Every new session starts by asking for the name on the ID; a
  // resubmission only resumes the session Didit already has.
  const beginVerification = () => {
    setNameError("");
    setState("name_entry");
  };

  const startVerification = async (declared?: { givenNames: string; surname: string }) => {
    try {
      const hasPermission = await requestCameraPermission();
      if (!hasPermission) return;

      setState("loading");
      const response = await api.createKYCSession(declared);

      if (response.code === "kyc_in_review") {
        setState("under_review");
        return;
      }
      if (response.code?.startsWith("legal_name_")) {
        setNameError(response.message || "Check your name and try again.");
        setState("name_entry");
        return;
      }
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
            <Button title="Resubmit Documents" onPress={() => startVerification()} fullWidth />
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
            <Button title="Continue Verification" onPress={beginVerification} fullWidth />
            <Button title="Refresh Status" variant="secondary" onPress={checkStatus} fullWidth />
            <Button title="Go Back" variant="ghost" onPress={() => router.back()} fullWidth />
          </View>
        </StatusPanel>
      </SafeAreaView>
    );
  }

  if (state === "under_review") {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <StatusPanel
          icon="clock"
          iconColor={colors.warning}
          iconBg={colors.warningBg}
          title="Verification Under Review"
          subtitle="We're reviewing your documents. There's nothing more to submit — your status will update here as soon as the review is done."
        >
          <View style={{ width: "100%", gap: spacing.md }}>
            <Button title="Refresh Status" onPress={checkStatus} fullWidth />
            <Button title="Go Back" variant="ghost" onPress={() => router.back()} fullWidth />
          </View>
        </StatusPanel>
      </SafeAreaView>
    );
  }

  if (state === "name_entry") {
    const declared = { givenNames: givenNames.trim(), surname: surname.trim() };
    const complete = declared.givenNames !== "" && declared.surname !== "";
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
        <Header />
        <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
          <ScrollView
            contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
            keyboardShouldPersistTaps="handled"
          >
            <View style={{ gap: spacing.xs }}>
              <Text variant="title3">Your name on your ID</Text>
              <Text variant="bodySm" color="textSecondary">
                Enter your name exactly as it is printed on the ID you are about to scan. Payments you send us are
                matched to this name, so it should also be the name on your bank account.
              </Text>
            </View>
            <Input
              label="First and middle names"
              placeholder="e.g. Chinaza Juliet"
              value={givenNames}
              onChangeText={(v) => { setGivenNames(v); setNameError(""); }}
              autoCapitalize="words"
              autoCorrect={false}
              autoComplete="name-given"
              textContentType="givenName"
              maxLength={80}
            />
            <Input
              label="Surname"
              placeholder="e.g. Odah"
              value={surname}
              onChangeText={(v) => { setSurname(v); setNameError(""); }}
              autoCapitalize="words"
              autoCorrect={false}
              autoComplete="name-family"
              textContentType="familyName"
              maxLength={80}
              error={nameError || undefined}
            />
            <View style={{ gap: spacing.md }}>
              <Button title="Continue" onPress={() => startVerification(declared)} disabled={!complete} fullWidth />
              <Button title="Back" variant="ghost" onPress={() => checkStatus()} fullWidth />
            </View>
          </ScrollView>
        </KeyboardAvoidingView>
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
            <Button title="Try Again" onPress={beginVerification} fullWidth />
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
          <Button title="Start Verification" onPress={beginVerification} fullWidth />
          <Button title="Do This Later" variant="ghost" onPress={() => router.back()} fullWidth />
        </View>
      </StatusPanel>
    </SafeAreaView>
  );
}
