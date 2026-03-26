import React, { useState, useEffect, useCallback } from "react";
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
} from "react-native";
import { router } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import api from "../../components/services/api";
import { COLORS, SIZES } from "../../constants/theme";

type KYCState = "not_started" | "loading" | "sdk_active" | "in_progress" | "verified" | "rejected";

export default function KYCScreen() {
  const [state, setState] = useState<KYCState>("loading");
  const [notes, setNotes] = useState("");

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

  const startVerification = async () => {
    try {
      setState("loading");
      const response = await api.createKYCSession();

      if (!response.success || !response.data) {
        Alert.alert("Error", response.message || "Failed to start verification");
        setState("not_started");
        return;
      }

      // TODO: Launch Smile ID SDK here using response.data
      // The exact SDK integration depends on the @smile_identity/react-native API.
      // Example pattern:
      //
      // import { SmileID } from "@smile_identity/react-native";
      // const result = await SmileID.documentVerification({
      //   jobId: response.data.jobId,
      //   partnerId: response.data.partnerId,
      //   ...response.data.session,
      // });
      //
      // For now, after SDK completes:
      setState("in_progress");
      Alert.alert("Verification Started", "We're reviewing your documents. This usually takes a few minutes.");
    } catch (error: any) {
      Alert.alert("Error", api.getErrorMessage(error) || "Something went wrong");
      setState("not_started");
    }
  };

  if (state === "loading") {
    return (
      <View style={styles.container}>
        <ActivityIndicator size="large" color={COLORS.primary} />
        <Text style={styles.loadingText}>Loading...</Text>
      </View>
    );
  }

  if (state === "verified") {
    return (
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
    );
  }

  if (state === "in_progress") {
    return (
      <View style={styles.container}>
        <View style={styles.iconContainer}>
          <Ionicons name="time-outline" size={80} color={COLORS.secondary} />
        </View>
        <Text style={styles.title}>Verification In Progress</Text>
        <Text style={styles.subtitle}>
          We're reviewing your documents. This usually takes a few minutes. We'll notify you when it's done.
        </Text>
        <TouchableOpacity style={styles.secondaryButton} onPress={checkStatus}>
          <Text style={styles.secondaryButtonText}>Refresh Status</Text>
        </TouchableOpacity>
        <TouchableOpacity style={styles.button} onPress={() => router.back()}>
          <Text style={styles.buttonText}>Go Back</Text>
        </TouchableOpacity>
      </View>
    );
  }

  if (state === "rejected") {
    return (
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
    );
  }

  // not_started
  return (
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
  );
}

const styles = StyleSheet.create({
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
