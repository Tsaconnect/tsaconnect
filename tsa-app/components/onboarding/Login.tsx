import React, { useState } from "react";
import {
  StyleSheet,
  Text,
  View,
  TextInput,
  Pressable,
  Alert,
  ActivityIndicator,
} from "react-native";
import { COLORS } from "../../constants/theme";
import { router } from "expo-router";
import { LinearProgress } from "react-native-elements";
import Icon from "@expo/vector-icons/MaterialCommunityIcons";
import api from "../services/api";
import AsyncStorage from "@react-native-async-storage/async-storage";

export default function Login() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const [isError, setIsError] = useState(false);

  function handlePasswordRecovery() {
    router.push("/recovery");
  }

  function Signup() {
    router.push("/signup");
  }

  async function handleLogin() {
    if (!email || !password) {
      Alert.alert("Error", "All credentials must be filled");
      return;
    }

    setLoading(true);
    setIsError(false);

    try {
      const response = await api.login(email.trim(), password.trim());
      if (response.success) {
        setEmail("");
        setPassword("");
        router.replace("/home");
      } else {
        setIsError(true);
        Alert.alert(
          "Login Failed",
          response.message || "Invalid credentials. Please try again.",
          [{ text: "OK" }]
        );
      }
    } catch (error: any) {
      setIsError(true);
      console.error("Login error:", error);

      Alert.alert(
        "Connection Error",
        api.getErrorMessage(error) || "Unable to connect to server. Please check your internet connection.",
        [{ text: "OK" }]
      );
    } finally {
      setLoading(false);
    }
  }

  return (
    <View style={styles.container}>
      <Text style={styles.loginText}>Login</Text>

      <View style={styles.formContainer}>
        {/* Email/Username Input */}
        <TextInput
          style={[
            styles.input,
            isError && styles.inputError
          ]}
          value={email}
          onChangeText={(text) => {
            setEmail(text);
            setIsError(false);
          }}
          placeholder="Email or Username"
          keyboardType="email-address"
          autoCapitalize="none"
          autoComplete="email"
          editable={!loading}
          placeholderTextColor="#999"
        />

        {/* Password Input */}
        <View style={[
          styles.passwordContainer,
          isError && styles.inputError
        ]}>
          <TextInput
            style={styles.passwordInput}
            value={password}
            placeholder="Password"
            secureTextEntry={!isPasswordVisible}
            autoCapitalize="none"
            autoComplete="password"
            onChangeText={(text) => {
              setPassword(text);
              setIsError(false);
            }}
            editable={!loading}
            placeholderTextColor="#999"
          />
          <Pressable
            style={styles.icon}
            onPress={() => setIsPasswordVisible(!isPasswordVisible)}
            disabled={loading}
          >
            <Icon
              name={isPasswordVisible ? "eye-off" : "eye"}
              size={24}
              color={loading ? "#cccccc" : "gray"}
            />
          </Pressable>
        </View>

        {/* Loading Indicator */}
        {loading && (
          <View style={styles.progressContainer}>
            <LinearProgress color={COLORS.primary} />
            <Text style={styles.loadingText}>Signing in...</Text>
          </View>
        )}

        {/* Forgot Password */}
        <Pressable
          onPress={handlePasswordRecovery}
          disabled={loading}
          style={styles.forgotPasswordButton}
        >
          <Text style={[
            styles.forgotPassword,
            loading && styles.disabledText
          ]}>
            Forgot Password?
          </Text>
        </Pressable>

        {/* Login Button */}
        <Pressable
          onPress={handleLogin}
          style={({ pressed }) => [
            styles.button,
            loading && styles.buttonLoading,
            isError && styles.buttonError,
            pressed && styles.buttonPressed
          ]}
          disabled={loading}
        >
          {loading ? (
            <ActivityIndicator size="small" color="#fff" />
          ) : (
            <Text style={styles.buttonText}>Login</Text>
          )}
        </Pressable>

        {/* Separator */}
        <View style={styles.separatorContainer}>
          <View style={styles.separatorLine} />
          <Text style={styles.orText}>OR</Text>
          <View style={styles.separatorLine} />
        </View>

        {/* Signup Option */}
        <View style={styles.signupContainer}>
          <Text style={styles.question}>New To Tsa?</Text>
          <Pressable
            onPress={Signup}
            disabled={loading}
            style={({ pressed }) => [
              pressed && styles.signupPressed
            ]}
          >
            <Text style={[
              styles.signUpText,
              loading && styles.disabledText
            ]}>
              Create Account
            </Text>
          </Pressable>
        </View>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: "center",
    padding: 20,
    backgroundColor: "#fff",
    alignItems: "center",
  },
  formContainer: {
    width: "100%",
    maxWidth: 400,
    alignItems: "center",
  },
  loginText: {
    fontSize: 28,
    fontWeight: "bold",
    marginBottom: 30,
    color: COLORS.primary,
    textAlign: "center",
    width: "100%",
  },
  input: {
    width: "100%",
    height: 56,
    borderColor: "#ddd",
    borderWidth: 1,
    marginBottom: 16,
    paddingHorizontal: 16,
    borderRadius: 12,
    fontSize: 16,
    backgroundColor: "#f9f9f9",
  },
  inputError: {
    borderColor: "#ff4444",
    borderWidth: 2,
    backgroundColor: "#fff5f5",
  },
  passwordContainer: {
    flexDirection: "row",
    alignItems: "center",
    width: "100%",
    height: 56,
    borderColor: "#ddd",
    borderWidth: 1,
    marginBottom: 16,
    borderRadius: 12,
    paddingHorizontal: 16,
    backgroundColor: "#f9f9f9",
  },
  passwordInput: {
    flex: 1,
    fontSize: 16,
    height: "100%",
  },
  icon: {
    padding: 8,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: "100%",
    alignItems: "center",
    justifyContent: "center",
    marginTop: 20,
    height: 56,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.primary,
    shadowColor: "#000",
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    elevation: 3,
  },
  buttonPressed: {
    opacity: 0.9,
    transform: [{ scale: 0.99 }],
  },
  buttonLoading: {
    backgroundColor: "#d4ba92",
    opacity: 0.8,
  },
  buttonError: {
    borderColor: "#ff4444",
  },
  buttonText: {
    color: "#fff",
    fontSize: 18,
    fontWeight: "600",
  },
  forgotPasswordButton: {
    alignSelf: "flex-end",
    marginTop: 4,
    marginBottom: 20,
  },
  forgotPassword: {
    color: COLORS.primary,
    fontWeight: "500",
    fontSize: 14,
  },
  disabledText: {
    color: "#cccccc",
  },
  separatorContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginVertical: 24,
    width: "100%",
  },
  separatorLine: {
    flex: 1,
    height: 1,
    backgroundColor: "#ddd",
  },
  orText: {
    marginHorizontal: 16,
    color: "#666",
    fontSize: 14,
    fontWeight: "500",
  },
  signupContainer: {
    flexDirection: "row",
    justifyContent: "center",
    alignItems: "center",
    marginTop: 16,
  },
  signupPressed: {
    opacity: 0.7,
  },
  question: {
    color: "#666",
    fontSize: 14,
    marginRight: 8,
  },
  signUpText: {
    color: COLORS.primary,
    fontWeight: "600",
    fontSize: 14,
  },
  progressContainer: {
    width: "100%",
    marginTop: 10,
    alignItems: "center",
  },
  loadingText: {
    marginTop: 10,
    fontSize: 14,
    color: "#666",
  },
});