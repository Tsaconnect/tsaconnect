import React, { useState, useEffect, useCallback } from "react";
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
  Linking,
} from "react-native";
import { router } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import { Camera } from "expo-camera";
import { WebView } from "react-native-webview";
import api from "../../components/services/api";
import { COLORS, SIZES } from "../../constants/theme";

type KYCState = "not_started" | "loading" | "webview_active" | "in_progress" | "verified" | "rejected";

export default function KYCScreen() {
  const [state, setState] = useState<KYCState>("loading");
  const [notes, setNotes] = useState("");
  const [inquiryUrl, setInquiryUrl] = useState<string | null>(null);

  const checkStatus = useCallback(async () => {
    try {
      const response = await api.getKYCStatus();
      if (response.success && response.data) {
        const { verificationStatus } = response.data;
        if (verificationStatus === "verified") {
          setState("verified");
        } else if (verificationStatus === "rejected") {
          setState("rejected");
          setNotes(response.data.verificationNotes || "Verification failed. Please try again.");
        } else if (verificationStatus === "in_review") {
          setState("in_progress");
        } else {
          setState("not_started");
        }
      }
    } catch (error) {
      setState("not_started");
    }
  }, []);

  useEffect(() => {
    checkStatus();
  }, [checkStatus]);

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

      setInquiryUrl(response.data.inquiryUrl);
      setState("webview_active");
    } catch (error: any) {
      Alert.alert("Error", api.getErrorMessage(error) || "Something went wrong");
      setState("not_started");
    }
  };

  const handleWebViewNavigationChange = (navState: { url: string }) => {
    // Persona redirects to a completion page when the inquiry is done
    if (navState.url.includes("/done") || navState.url.includes("completed")) {
      setInquiryUrl(null);
      setState("in_progress");
    }
  };

  const Header = () => (
    <View style={styles.header}>
      <TouchableOpacity onPress={() => router.back()} style={styles.headerBackButton}>
        <Ionicons name="arrow-back" size={24} color="#111" />
      </TouchableOpacity>
      <Text style={styles.headerTitle}>KYC Verification</Text>
      <View style={{ width: 24 }} />
    </View>
  );

  if (state === "loading") {
    return (
      <View style={styles.screenContainer}>
        <Header />
        <View style={styles.container}>
          <ActivityIndicator size="large" color={COLORS.primary} />
          <Text style={styles.loadingText}>Loading...</Text>
        </View>
      </View>
    );
  }

  if (state === "webview_active" && inquiryUrl) {
    return (
      <View style={styles.webviewContainer}>
        <WebView
          source={{ uri: inquiryUrl }}
          onNavigationStateChange={handleWebViewNavigationChange}
          javaScriptEnabled
          domStorageEnabled
          mediaPlaybackRequiresUserAction={false}
          allowsInlineMediaPlayback
          mediaCapturePermissionGrantType="grant"
          allowsBackForwardNavigationGestures={false}
          onPermissionRequest={(event: any) => event.grant()}
          style={styles.webview}
        />
      </View>
    );
  }

  if (state === "verified") {
    return (
      <View style={styles.screenContainer}>
        <Header />
        <View style={styles.container}>
          <View style={styles.iconContainer}>
            <Ionicons name="checkmark-circle" size={80} color={COLORS.primary} />
          </View>
          <Text style={styles.title}>Identity Verified</Text>
          <Text style={styles.subtitle}>Your identity has been successfully verified.</Text>
          <TouchableOpacity style={styles.button} onPress={() => router.back()}>
            <Text style={styles.buttonText}>Go Back</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  if (state === "in_progress") {
    return (
      <View style={styles.screenContainer}>
        <Header />
        <View style={styles.container}>
          <View style={styles.iconContainer}>
            <Ionicons name="time-outline" size={80} color={COLORS.secondary} />
          </View>
          <Text style={styles.title}>Verification In Progress</Text>
          <Text style={styles.subtitle}>
            Your verification is being reviewed. If you didn't finish submitting, you can start again.
          </Text>
          <TouchableOpacity style={styles.button} onPress={startVerification}>
            <Text style={styles.buttonText}>Continue Verification</Text>
          </TouchableOpacity>
          <TouchableOpacity style={styles.secondaryButton} onPress={checkStatus}>
            <Text style={styles.secondaryButtonText}>Refresh Status</Text>
          </TouchableOpacity>
          <TouchableOpacity style={styles.secondaryButton} onPress={() => router.back()}>
            <Text style={styles.secondaryButtonText}>Go Back</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  if (state === "rejected") {
    return (
      <View style={styles.screenContainer}>
        <Header />
        <View style={styles.container}>
          <View style={styles.iconContainer}>
            <Ionicons name="close-circle" size={80} color="#EF4444" />
          </View>
          <Text style={styles.title}>Verification Failed</Text>
          <Text style={styles.subtitle}>{notes}</Text>
          <TouchableOpacity style={styles.button} onPress={startVerification}>
            <Text style={styles.buttonText}>Try Again</Text>
          </TouchableOpacity>
          <TouchableOpacity style={styles.secondaryButton} onPress={() => router.back()}>
            <Text style={styles.secondaryButtonText}>Go Back</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  // not_started
  return (
    <View style={styles.screenContainer}>
      <Header />
      <View style={styles.container}>
        <View style={styles.iconContainer}>
          <Ionicons name="shield-checkmark-outline" size={80} color={COLORS.primary} />
        </View>
        <Text style={styles.title}>Verify Your Identity</Text>
        <Text style={styles.subtitle}>
          Complete identity verification to unlock all features including buying, selling, and sending funds.
        </Text>
        <Text style={styles.infoText}>
          You'll need:{"\n"}
          {"\u2022"} A valid government-issued ID{"\n"}
          {"\u2022"} A well-lit environment for a selfie
        </Text>
        <TouchableOpacity style={styles.button} onPress={startVerification}>
          <Text style={styles.buttonText}>Start Verification</Text>
        </TouchableOpacity>
        <TouchableOpacity style={styles.secondaryButton} onPress={() => router.back()}>
          <Text style={styles.secondaryButtonText}>Do This Later</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  screenContainer: {
    flex: 1,
    backgroundColor: "#fff",
  },
  header: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    paddingHorizontal: 16,
    paddingTop: 50,
    paddingBottom: 12,
    borderBottomWidth: 1,
    borderBottomColor: "#eee",
  },
  headerBackButton: {
    padding: 4,
  },
  headerTitle: {
    fontSize: 16,
    fontWeight: "600",
    color: "#111",
  },
  webviewContainer: {
    flex: 1,
    backgroundColor: "#fff",
  },
  webview: {
    flex: 1,
  },
  container: {
    flex: 1,
    backgroundColor: "#fff",
    alignItems: "center",
    justifyContent: "center",
    paddingHorizontal: SIZES.large,
  },
  iconContainer: {
    marginBottom: 24,
  },
  title: {
    fontSize: 22,
    fontWeight: "700",
    color: "#111",
    marginBottom: 12,
    textAlign: "center",
  },
  subtitle: {
    fontSize: 15,
    color: "#666",
    textAlign: "center",
    marginBottom: 20,
    lineHeight: 22,
  },
  infoText: {
    fontSize: 14,
    color: "#444",
    marginBottom: 32,
    lineHeight: 22,
  },
  loadingText: {
    marginTop: 12,
    fontSize: 15,
    color: "#666",
  },
  button: {
    backgroundColor: COLORS.primary,
    paddingVertical: 14,
    paddingHorizontal: 32,
    borderRadius: 10,
    width: "100%",
    alignItems: "center",
    marginBottom: 12,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
    fontWeight: "600",
  },
  secondaryButton: {
    paddingVertical: 14,
    paddingHorizontal: 32,
    borderRadius: 10,
    width: "100%",
    alignItems: "center",
    borderWidth: 1,
    borderColor: COLORS.primary,
    marginBottom: 12,
  },
  secondaryButtonText: {
    color: COLORS.primary,
    fontSize: 16,
    fontWeight: "600",
  },
});
