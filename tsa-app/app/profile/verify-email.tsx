import React, { useState } from "react";
import {
  StyleSheet,
  View,
  Text,
  TextInput,
  TouchableOpacity,
  Alert,
} from "react-native";
import axios from "axios";
import { useAuth } from "../../AuthContext/AuthContext";
import { baseUrl } from "../../constants/api/apiClient";
import { router } from "expo-router";

const VerifyEmailScreen = () => {
  const { token, currentUser } = useAuth();
  const [otp, setOtp] = useState("");
  const [loading, setLoading] = useState(false);

  console.log("id", currentUser.id);
  const handleVerifyOtp = async () => {
    if (otp.length !== 6) {
      Alert.alert(
        "Invalid OTP",
        "Please enter the 6-digit OTP sent to your email."
      );
      return;
    }

    try {
      setLoading(true);
      await axios.post(
        `${baseUrl}/auth/verify-email?userId=${currentUser.id}`,
        { otp },
        {
          headers: {
            Authorization: `${token}`,
          },
        }
      );
      Alert.alert("Success", "Email verified successfully!");
      router.push("/profile");
    } catch (error) {
      Alert.alert(
        "Error",
        //@ts-ignore
        error?.response?.data?.message || "Failed to verify OTP."
      );
    } finally {
      setLoading(false);
    }
  };

  return (
    <View style={styles.container}>
      <Text style={styles.title}>Enter OTP</Text>
      <TextInput
        style={styles.input}
        value={otp}
        onChangeText={setOtp}
        keyboardType="number-pad"
        maxLength={6}
        placeholder="Enter 6-digit OTP"
      />
      <TouchableOpacity
        style={styles.button}
        onPress={handleVerifyOtp}
        disabled={loading}
      >
        <Text style={styles.buttonText}>
          {loading ? "Verifying..." : "Verify"}
        </Text>
      </TouchableOpacity>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    padding: 20,
  },
  title: {
    fontSize: 24,
    fontWeight: "bold",
    marginBottom: 20,
  },
  input: {
    width: "80%",
    padding: 10,
    borderWidth: 1,
    borderColor: "#ccc",
    borderRadius: 5,
    marginBottom: 20,
    textAlign: "center",
  },
  button: {
    backgroundColor: "#9D6B38",
    padding: 15,
    borderRadius: 5,
    width: "80%",
    alignItems: "center",
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
});

export default VerifyEmailScreen;
